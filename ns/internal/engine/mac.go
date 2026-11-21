package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
	"github.com/fieldnode/lorawan-server/ns/internal/region"
)

// ErrPendingDownlinkExists is returned by the queueing commands when a device
// already has a downlink waiting; the caller decides whether to wait, replace it
// or report a conflict.
var ErrPendingDownlinkExists = errors.New("engine: device already has a pending downlink")

// processMACCommands runs the uplink MAC commands the device sent and returns
// the answers to carry back. Unknown or unimplemented commands are answered
// conservatively rather than ignored, because a device blocks on some answers.
//
// gwCnt is how many gateways received the uplink the commands arrived in, which
// LinkCheckAns reports back to the device.
func (e *Engine) processMACCommands(ctx context.Context, dev *Device, session *Session,
	cmds lorawan.MACCommandSet, reg region.Region, gwCnt int) (lorawan.MACCommandSet, error) {

	var replies lorawan.MACCommandSet

	for _, cmd := range cmds {
		switch cmd.CID {
		case lorawan.CIDLinkADR: // uplink: LinkADRAns
			p, err := lorawan.ParseLinkADRAns(cmd.Payload)
			if err != nil {
				e.malformed(ctx, session, "LinkADRAns", err)
				continue
			}
			session.AwaitingLinkADRAns = false
			// The device answered, so however many attempts it took, the
			// exchange is over and the budget starts again for the next one.
			session.LinkADRRetries = 0
			if p.PowerACK && p.DataRateACK && p.ChannelACK {
				if req := session.PendingADR; req != nil {
					session.DataRate = req.DataRate
					session.TxPower = req.TxPower
					session.NbTrans = req.NbTrans
					session.PendingADR = nil
				}
				e.recordEvent(ctx, Event{
					Kind: EventMAC, DevEUI: session.DevEUI, DevAddr: session.DevAddr,
					Detail: fmt.Sprintf("LinkADR accepted: DR%d TX%d NbTrans%d",
						session.DataRate, session.TxPower, session.NbTrans),
				})
			} else {
				session.PendingADR = nil
				e.recordEvent(ctx, Event{
					Kind: EventMAC, DevEUI: session.DevEUI, DevAddr: session.DevAddr,
					Reason: ReasonLinkADRRejected,
					Detail: fmt.Sprintf("LinkADR refused (power=%t data-rate=%t channel=%t), keeping DR%d TX%d",
						p.PowerACK, p.DataRateACK, p.ChannelACK, session.DataRate, session.TxPower),
				})
			}

		case lorawan.CIDDevStatus: // uplink: DevStatusAns
			p, err := lorawan.ParseDevStatusAns(cmd.Payload)
			if err != nil {
				e.malformed(ctx, session, "DevStatusAns", err)
				continue
			}
			session.HasDeviceStatus = true
			session.Battery = p.Battery
			session.Margin = p.SNR
			e.recordEvent(ctx, Event{
				Kind: EventMAC, DevEUI: session.DevEUI, DevAddr: session.DevAddr,
				Detail: fmt.Sprintf("device status: battery %d/255, margin %d dB", p.Battery, p.SNR),
			})

		case lorawan.CIDRXParamSetup: // uplink: RXParamSetupAns
			p, err := lorawan.ParseRXParamSetupAns(cmd.Payload)
			if err != nil {
				e.malformed(ctx, session, "RXParamSetupAns", err)
				continue
			}
			if req := session.PendingRX2; req != nil {
				if p.RX2Frequency {
					session.Rx2FrequencyHz = req.FrequencyHz
					session.Rx2DataRate = req.DataRate
				}
				session.PendingRX2 = nil
			}
			e.recordEvent(ctx, Event{
				Kind: EventMAC, DevEUI: session.DevEUI, DevAddr: session.DevAddr,
				Detail: fmt.Sprintf("RXParamSetup answer (channel=%t rx2-freq=%t rx1-dr-offset=%t), RX2 now %d Hz / DR%d",
					p.Channel, p.RX2Frequency, p.RX1DROffset, session.Rx2FrequencyHz, session.Rx2DataRate),
			})

		case lorawan.CIDRXTimingSetup: // uplink: RXTimingSetupAns (no payload)
			// The answer carries no status bits, so receiving it is the
			// confirmation. Applying the delay here rather than when the request
			// was sent keeps the server's idea of RX1 in step with the device's.
			if req := session.PendingRX1Delay; req != nil {
				session.Rx1Delay = req.DelaySeconds
				session.PendingRX1Delay = nil
			}
			e.recordEvent(ctx, Event{
				Kind: EventMAC, DevEUI: session.DevEUI, DevAddr: session.DevAddr,
				Detail: fmt.Sprintf("RXTimingSetup acknowledged (RX1 delay %d s)", session.Rx1Delay),
			})

		case lorawan.CIDDutyCycle: // uplink: DutyCycleAns (no payload)
			if req := session.PendingDutyCycle; req != nil {
				// RP002 Table 13: the index is a power-of-two divisor of the
				// device's unrestricted duty cycle, index 0 meaning "as the
				// device is". Storing the fraction lets the scheduler reason in
				// percentages without re-reading the table.
				session.DutyCycle = 1.0 / float64(uint32(1)<<req.MaxDutyCycleIndex)
				session.PendingDutyCycle = nil
				e.recordEvent(ctx, Event{
					Kind: EventMAC, DevEUI: session.DevEUI, DevAddr: session.DevAddr,
					Detail: fmt.Sprintf("duty cycle limited to 1/%d", uint32(1)<<req.MaxDutyCycleIndex),
				})
			}

		case lorawan.CIDTXParamSetup: // uplink: TXParamSetupAns (no payload)
			if req := session.PendingTXParams; req != nil {
				session.UplinkDwell400 = req.UplinkDwell400
				session.DownlinkDwell400 = req.DownlinkDwell400
				session.MaxEIRPIndex = req.MaxEIRPIndex
				session.PendingTXParams = nil
				e.recordEvent(ctx, Event{
					Kind: EventMAC, DevEUI: session.DevEUI, DevAddr: session.DevAddr,
					Detail: fmt.Sprintf("TX parameters acknowledged (uplink dwell 400ms=%t, downlink dwell 400ms=%t, max EIRP index %d)",
						session.UplinkDwell400, session.DownlinkDwell400, session.MaxEIRPIndex),
				})
			}

		case lorawan.CIDLinkCheck: // uplink: LinkCheckReq
			// Margin is the headroom above the demodulation floor of the data
			// rate the device is using, in dB, and GwCnt is how many gateways
			// heard it. Both come from the aggregated copies: a device that two
			// gateways can hear has a better network than one that reports a
			// single gateway, and its margin should reflect the best of the two
			// links rather than whichever packet arrived first.
			margin := int8(roundSNR(session.LastSNR - requiredSNRForDataRate(reg, session.DataRate)))
			if margin < 0 {
				margin = 0 // the field is unsigned in spirit; a negative margin
				// means "at the edge", which 0 already tells the device.
			}
			if gwCnt < 1 {
				gwCnt = 1
			}
			replies = append(replies, lorawan.MACCommand{
				CID:     lorawan.CIDLinkCheck,
				Payload: lorawan.LinkCheckAnsPayload{Margin: margin, GwCnt: byte(gwCnt)}.Marshal(),
			})

		case lorawan.CIDNewChannel: // uplink: NewChannelAns
			p, err := lorawan.ParseNewChannelAns(cmd.Payload)
			if err != nil {
				e.malformed(ctx, session, "NewChannelAns", err)
				continue
			}
			// NewChannelAns answers a NewChannelReq, so it gets no reply of its
			// own: CID 0x07 on an uplink is the device reporting, and the only
			// thing the network owes it is to remember the answer.
			req := session.PendingChannel
			if req == nil {
				e.recordEvent(ctx, Event{
					Kind: EventMAC, DevEUI: session.DevEUI, DevAddr: session.DevAddr,
					Reason: ReasonUnexpectedMACAnswer,
					Detail: fmt.Sprintf("NewChannelAns with no request outstanding (frequency-ok=%t data-rate-ok=%t)",
						p.ChannelFreqOK, p.DataRateRangeOK),
				})
				continue
			}
			if p.ChannelFreqOK && p.DataRateRangeOK {
				if session.UserChannels == nil {
					session.UserChannels = map[int]region.Channel{}
				}
				rates := make([]int, 0, req.MaxDataRate-req.MinDataRate+1)
				for i := req.MinDataRate; i <= req.MaxDataRate; i++ {
					rates = append(rates, i)
				}
				session.UserChannels[req.Index] = region.Channel{
					FrequencyHz: req.FrequencyHz,
					DataRates:   rates,
					Enabled:     true,
					UserDefined: true,
				}
				session.PendingChannel = nil
				e.recordEvent(ctx, Event{
					Kind: EventMAC, DevEUI: session.DevEUI, DevAddr: session.DevAddr,
					Detail: fmt.Sprintf("channel %d accepted: %d Hz, DR%d..DR%d",
						req.Index, req.FrequencyHz, req.MinDataRate, req.MaxDataRate),
				})
				continue
			}
			// A refusal is final for this request: the device has said it cannot
			// use this channel, and asking again in the next window would be a
			// loop that never ends.
			session.PendingChannel = nil
			e.recordEvent(ctx, Event{
				Kind: EventMAC, DevEUI: session.DevEUI, DevAddr: session.DevAddr,
				Reason: ReasonNewChannelRefused,
				Detail: fmt.Sprintf("device refused channel %d (%d Hz, DR%d..DR%d): frequency-ok=%t data-rate-ok=%t",
					req.Index, req.FrequencyHz, req.MinDataRate, req.MaxDataRate,
					p.ChannelFreqOK, p.DataRateRangeOK),
			})

		default:
			e.log.Debug("unhandled uplink MAC command",
				"dev_eui", session.DevEUI, "cid", fmt.Sprintf("0x%02x", byte(cmd.CID)))
		}
	}
	return replies, nil
}

func (e *Engine) malformed(ctx context.Context, session *Session, name string, err error) {
	e.recordEvent(ctx, Event{
		Kind: EventUplinkDrop, Reason: ReasonMalformedMACPayload,
		Detail: fmt.Sprintf("%s: %v", name, err),
		DevEUI: session.DevEUI, DevAddr: session.DevAddr,
	})
}

func roundSNR(snr float64) int {
	if snr >= 0 {
		return int(snr + 0.5)
	}
	return int(snr - 0.5)
}

// --- FOpts packing ----------------------------------------------------------

type droppedCommands struct {
	count int
	cids  []lorawan.CID
}

func (d *droppedCommands) names() string {
	parts := make([]string, 0, len(d.cids))
	for _, c := range d.cids {
		parts = append(parts, fmt.Sprintf("0x%02x", byte(c)))
	}
	return strings.Join(parts, ", ")
}

// fitMACCommands concatenates command sets into the 15-byte FOpts field,
// answers-first. Commands that do not fit are reported so the caller can defer
// them instead of silently dropping radio state.
func fitMACCommands(sets ...lorawan.MACCommandSet) (lorawan.MACCommandSet, *droppedCommands) {
	const maxFOpts = 15

	var out lorawan.MACCommandSet
	var dropped *droppedCommands
	total := 0

	for _, set := range sets {
		for _, cmd := range set {
			size := 1 + len(cmd.Payload)
			if total+size > maxFOpts {
				if dropped == nil {
					dropped = &droppedCommands{}
				}
				dropped.count++
				dropped.cids = append(dropped.cids, cmd.CID)
				continue
			}
			out = append(out, cmd)
			total += size
		}
	}
	return out, dropped
}

func pendingMACCommands(p *PendingDownlink) lorawan.MACCommandSet {
	if p == nil {
		return nil
	}
	return p.MACCommands
}

func cloneMACSet(set lorawan.MACCommandSet) lorawan.MACCommandSet {
	if set == nil {
		return nil
	}
	out := make(lorawan.MACCommandSet, len(set))
	for i, c := range set {
		out[i] = lorawan.MACCommand{CID: c.CID, Payload: append([]byte(nil), c.Payload...)}
	}
	return out
}
