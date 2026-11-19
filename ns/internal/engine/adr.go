package engine

import (
	"fmt"

	"github.com/fieldnode/lorawan-server/ns/internal/region"
)

// Adaptive data-rate control.
//
// This implements the approach described in the LoRaWAN ADR application note:
// the server decides the data rate and transmit power, the device decides when to
// back off. The variant here matches what widely deployed servers do with the
// same inputs, so a facility retuned from one to the other sees comparable
// decisions rather than a different radio policy overnight.
//
// The shape of it: every accepted uplink contributes an SNR sample to a 20
// sample window. The best SNR in that window is compared with the SNR the
// current data rate needs; the surplus, less an installation margin, divided by
// 3 dB is how many steps the link can afford. A step is a data-rate increase,
// and once the data rate is at its ceiling a step becomes a transmit-power
// reduction. A deficit buys a transmit-power increase first — lowering the data
// rate is the device's own job (its ADRACKRUN counter), because a server that
// lowers it here and there makes the link oscillate.

// adrHistorySize is how many uplinks the algorithm looks at. Twenty is the
// window the application note's worked example uses, and a partial window
// deliberately does not trigger a power change.
const adrHistorySize = 20

// adrStepDB is the SNR headroom one step costs.
const adrStepDB = 3.0

// DefaultADRInstallationMargin is the fade-margin safety band kept between the
// measured link and the data rate chosen: 10 dB, as the application note
// recommends.
const DefaultADRInstallationMargin = 10.0

// DefaultADRACKLimit and DefaultADRACKDelay are RP002's defaults for the
// device-side counters (2048 uplinks before the device starts retransmitting on
// its own, then 32 before it falls back).
const (
	DefaultADRACKLimit = 2048
	DefaultADRACKDelay = 32
)

// DefaultLinkADRMaxRetries is RP002's cap on re-sending an unanswered
// LinkADRReq.
const DefaultLinkADRMaxRetries = 32

// UplinkHistoryItem is one ADR sample: what the network measured for one
// uplink of the device.
type UplinkHistoryItem struct {
	FCnt         uint32  `json:"f_cnt"`
	SNR          float64 `json:"snr"`
	DataRate     int     `json:"data_rate"`
	TxPowerIndex int     `json:"tx_power_index"`
}

// ADRDecision is what the algorithm returns. It never mutates anything: the
// caller turns it into a LinkADRReq and applies it only once the device
// acknowledges, which is the difference between "the server believes" and "the
// device is doing".
type ADRDecision struct {
	DataRate     int
	TxPowerIndex int
	NbTrans      int

	// Changed reports whether the decision differs from the state the device is
	// currently running.
	Changed bool
	// Reason explains the decision in one line, for the event log.
	Reason string
}

// adrInput is everything the algorithm needs, assembled by the engine. Keeping
// it a plain struct makes the algorithm a pure function and therefore testable
// without a store, a gateway or a clock.
type adrInput struct {
	// Region resolves data rates. The algorithm only steps through 125 kHz
	// LoRa rates, which it can only know from the band table.
	Region region.Region

	// Enabled is false when ADR is switched off for the device or the device
	// told us it is not following ADR (FCtrl.ADR clear).
	Enabled bool

	DataRate        int
	TxPowerIndex    int
	NbTrans         int
	MaxTxPowerIndex int

	// MaxDataRate is the band and device-profile ceiling already narrowed to a
	// single number.
	MaxDataRate int

	InstallationMargin float64
	History            []UplinkHistoryItem
}

// requiredSNRForSF returns the demodulation floor of a spreading factor: the
// published sensitivity of a LoRa demodulator at each SF, which is where the
// 2.5 dB-per-step spacing comes from. SF5 is included because some bands enable
// it.
func requiredSNRForSF(sf int) float64 {
	switch sf {
	case 5:
		return -2.5
	case 6:
		return -5.0
	case 7:
		return -7.5
	case 8:
		return -10.0
	case 9:
		return -12.5
	case 10:
		return -15.0
	case 11:
		return -17.5
	case 12:
		return -20.0
	default:
		// FSK and LR-FHSS have no spreading factor. Returning 0 makes the
		// margin the raw SNR, which is the conservative reading: the algorithm
		// will not chase a data rate it cannot reason about.
		return 0
	}
}

// requiredSNRForDataRate resolves the floor of a band data rate.
func requiredSNRForDataRate(reg region.Region, dataRate int) float64 {
	dr, err := reg.DataRate(dataRate)
	if err != nil {
		return 0
	}
	return requiredSNRForSF(dr.SpreadingFactor)
}

// max125kHzUplinkDataRate returns the highest uplink data rate at or below
// upper that is a 125 kHz LoRa rate.
//
// The narrowing matters: the algorithm reasons in 3 dB steps of LoRa spreading
// factor, so stepping into an FSK or 500 kHz rate would change the link by far
// more than one step. The clamp has to happen before the first step, not after.
func max125kHzUplinkDataRate(reg region.Region, upper int) int {
	best := -1
	for i := 0; i <= upper; i++ {
		dr, err := reg.DataRate(i)
		if err != nil || !dr.Uplink || dr.Modulation != region.ModulationLoRa {
			continue
		}
		if dr.BandwidthHz == 125_000 {
			best = i
		}
	}
	return best
}

// adrDecide runs the algorithm. It is pure: same input, same decision.
func adrDecide(in adrInput) ADRDecision {
	resp := ADRDecision{
		DataRate:     in.DataRate,
		TxPowerIndex: in.TxPowerIndex,
		NbTrans:      in.NbTrans,
		Reason:       "ADR disabled",
	}
	if !in.Enabled {
		return resp
	}

	// Clamp the ceiling to a 125 kHz LoRa rate, then keep the device inside it.
	// A device left above the ceiling after a band or profile change is lowered
	// before anything else is considered.
	ceiling := in.MaxDataRate
	if max125 := max125kHzUplinkDataRate(in.Region, in.MaxDataRate); max125 >= 0 && max125 < ceiling {
		ceiling = max125
	}
	if resp.DataRate > ceiling {
		resp.DataRate = ceiling
		resp.Reason = fmt.Sprintf("DR%d is above the ceiling DR%d, lowering", in.DataRate, ceiling)
	}

	// Retransmissions follow the measured loss rate, independently of the
	// data-rate decision.
	resp.NbTrans = nbTransFor(in.NbTrans, packetLossPercentage(in.History))

	if len(in.History) == 0 {
		resp.Reason = "no uplink history yet"
		return finishADR(resp, in)
	}

	snrMax := maxSNR(in.History)
	need := requiredSNRForDataRate(in.Region, in.DataRate)
	margin := snrMax - need - in.InstallationMargin
	steps := int(margin / adrStepDB) // truncates toward zero, so a partial step buys nothing

	if steps < 0 && countAtTxPower(in.History, in.TxPowerIndex) < adrHistorySize {
		// The link looks short of headroom, but the window is not yet filled
		// with frames at this power level. Raising power now invites the
		// oscillation the guard exists to prevent: up, down, up.
		resp.Reason = fmt.Sprintf("margin %.1f dB would raise power; waiting for %d samples at TX%d, have %d",
			margin, adrHistorySize, in.TxPowerIndex, countAtTxPower(in.History, in.TxPowerIndex))
		return finishADR(resp, in)
	}

	resp.TxPowerIndex, resp.DataRate = idealSteps(steps, in.TxPowerIndex, resp.DataRate,
		in.MaxTxPowerIndex, ceiling)
	resp.Reason = fmt.Sprintf("best SNR %.1f dB, need %.1f dB, margin %.1f dB, %d step(s) -> DR%d TX%d",
		snrMax, need, margin, steps, resp.DataRate, resp.TxPowerIndex)

	return finishADR(resp, in)
}

func finishADR(d ADRDecision, in adrInput) ADRDecision {
	d.Changed = d.DataRate != in.DataRate || d.TxPowerIndex != in.TxPowerIndex || d.NbTrans != in.NbTrans
	return d
}

// idealSteps spends the steps the link can afford.
//
// A positive step increases the data rate until the ceiling, and only then
// reduces transmit power (a higher index means lower power). A negative step
// increases transmit power first and never lowers the data rate: per RP002 the
// device lowers its own data rate when its ADRACKRUN counter expires, and a
// server doing it too makes the link oscillate.
func idealSteps(steps, txPowerIndex, dataRate, maxTxPowerIndex, maxDataRate int) (int, int) {
	for steps > 0 {
		if dataRate < maxDataRate {
			dataRate++
		} else if txPowerIndex < maxTxPowerIndex {
			txPowerIndex++
		} else {
			break // at both ceilings; spending more steps would do nothing
		}
		steps--
	}
	for steps < 0 {
		if txPowerIndex == 0 {
			break // already at maximum power
		}
		txPowerIndex--
		steps++
	}
	return txPowerIndex, dataRate
}

// maxSNR is the best SNR in the window. The best, not the mean: ADR optimises
// for the link the device can sustain at its best moment, which is what keeps a
// device that moves in and out of interference from being pushed to SF12.
func maxSNR(h []UplinkHistoryItem) float64 {
	best := -999.0
	for _, item := range h {
		if item.SNR > best {
			best = item.SNR
		}
	}
	return best
}

// countAtTxPower is how many samples were taken at the given power index.
func countAtTxPower(h []UplinkHistoryItem, txPowerIndex int) int {
	n := 0
	for _, item := range h {
		if item.TxPowerIndex == txPowerIndex {
			n++
		}
	}
	return n
}

// packetLossPercentage estimates the loss rate from gaps in the uplink counter
// over a full window. It returns 0 until the window is full, because a partial
// window has no meaningful denominator.
func packetLossPercentage(h []UplinkHistoryItem) float64 {
	if len(h) < adrHistorySize {
		return 0
	}
	var lost uint32
	previous := h[0].FCnt
	for _, item := range h[1:] {
		// Consecutive counters differ by exactly 1; anything larger is a gap.
		// The subtraction cannot underflow in practice because the history is
		// ordered by counter, and the guard keeps it safe if it is not.
		if item.FCnt > previous+1 {
			lost += item.FCnt - previous - 1
		}
		previous = item.FCnt
	}
	return float64(lost) / float64(len(h)) * 100
}

// nbTransFor maps a packet-loss rate onto a retransmission count. Rows are loss
// bands (<5%, <10%, <30%, above) and columns are the current NbTrans (1, 2, 3),
// so the count ramps up as loss worsens and never drops below what the link has
// already shown it needs.
func nbTransFor(current int, lossPercent float64) int {
	table := [4][3]int{{1, 1, 2}, {1, 2, 3}, {2, 3, 3}, {3, 3, 3}}

	current = min(max(current, 1), 3)
	row := 3
	switch {
	case lossPercent < 5:
		row = 0
	case lossPercent < 10:
		row = 1
	case lossPercent < 30:
		row = 2
	}
	return table[row][current-1]
}
