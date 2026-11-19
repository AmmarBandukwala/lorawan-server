package region

import (
	"fmt"
	"time"
)

// SubBand is a frequency range with its own regulatory limits.
//
// Duty cycle is a fraction of time, not a percentage: 0.01 means the
// transmitter may occupy the channel for 1% of the time, and 1.0 means the
// regulation imposes no duty cycle at all. The ranges come from the regional
// parameters document, which transcribes the underlying ETSI/FCC limits.
//
// These limits bind *us*, not only the device. A network server that schedules
// downlinks without tracking airtime can put a gateway out of compliance, and
// the gateway has no way to tell us: it either transmits anyway or drops the
// frame with a TX_ACK error that looks like a radio fault.
type SubBand struct {
	MinHz uint32
	MaxHz uint32
	// DutyCycle is the allowed fraction of channel occupancy, 1.0 for none.
	DutyCycle float64
	// MaxEIRPdBm is the radiated-power ceiling for the range. It is
	// informational here: the antenna installation decides the real number, so
	// the gateway configuration remains responsible for staying under it.
	MaxEIRPdBm float64
}

// Contains reports whether freqHz falls inside the range. The upper bound is
// exclusive, matching how the regulation writes the ranges out.
func (b SubBand) Contains(freqHz uint32) bool {
	return freqHz >= b.MinHz && freqHz < b.MaxHz
}

// Budget returns how much channel occupancy the duty cycle allows inside a
// measurement window.
func (b SubBand) Budget(window time.Duration) time.Duration {
	return time.Duration(b.DutyCycle * float64(window))
}

// String renders the range and its limit for logs and the status endpoint.
func (b SubBand) String() string {
	return fmt.Sprintf("%.3f-%.3f MHz @ %.4g%% duty cycle",
		float64(b.MinHz)/1e6, float64(b.MaxHz)/1e6, b.DutyCycle*100)
}

// subBandFor finds the sub-band governing a frequency. ok is false when the
// frequency falls in a gap the regulation does not define for this band, which
// means our channel plan is wrong; the caller must treat that as an error
// rather than as "unlimited", because transmitting there is the one case where
// assuming good faith is the expensive mistake.
func subBandFor(bands []SubBand, freqHz uint32) (SubBand, bool) {
	for _, b := range bands {
		if b.Contains(freqHz) {
			return b, true
		}
	}
	return SubBand{}, false
}

// eu868SubBands is the EU868 table from RP002 (which reproduces EN 300 220-1).
//
// The 869.4-869.65 MHz range is the one that matters most in practice: it
// carries the default RX2 channel at 869.525 MHz and allows ten times the
// occupancy of the rest of the band, which is why RX2 stays available when the
// 868 MHz channels have run out of budget.
//
// The gaps between ranges (868.6-868.7, 869.2-869.4, above 870) are not
// LoRaWAN channels; a transmission planned there is a bug in the channel plan.
var eu868SubBands = []SubBand{
	{MinHz: 433_050_000, MaxHz: 434_790_000, DutyCycle: 0.001, MaxEIRPdBm: 12},
	{MinHz: 863_000_000, MaxHz: 865_000_000, DutyCycle: 0.001, MaxEIRPdBm: 16},
	{MinHz: 865_000_000, MaxHz: 868_000_000, DutyCycle: 0.01, MaxEIRPdBm: 16},
	{MinHz: 868_000_000, MaxHz: 868_600_000, DutyCycle: 0.01, MaxEIRPdBm: 16},
	{MinHz: 868_700_000, MaxHz: 869_200_000, DutyCycle: 0.001, MaxEIRPdBm: 16},
	{MinHz: 869_400_000, MaxHz: 869_650_000, DutyCycle: 0.1, MaxEIRPdBm: 29},
	{MinHz: 869_700_000, MaxHz: 870_000_000, DutyCycle: 0.01, MaxEIRPdBm: 16},
}

// us915SubBands: the US band imposes no duty cycle, so the ranges exist only to
// carry the EIRP limits and to make "no duty cycle" an explicit, checked fact
// rather than an absence of code.
var us915SubBands = []SubBand{
	{MinHz: 902_300_000, MaxHz: 914_900_000, DutyCycle: 1.0, MaxEIRPdBm: 23},
	{MinHz: 923_300_000, MaxHz: 927_500_000, DutyCycle: 1.0, MaxEIRPdBm: 28},
}

// as923SubBands is the base AS923 range. AS923 limits are usually expressed as
// a 400 ms dwell time rather than a duty cycle, and both are modelled: the
// dwell time from the band's own settings and this 1% range from RP002, because
// several AS923 countries apply both.
//
// The variants (AS923-2/-3/-4) shift the channel plan by a fixed offset, so the
// range shifts with it. Which limits actually apply is a per-country question
// that the M6 bench pass must confirm against local regulation; the shift keeps
// the enforcement conservative in the meantime rather than silently wrong.
var as923SubBands = []SubBand{
	{MinHz: 923_000_000, MaxHz: 923_500_000, DutyCycle: 0.01, MaxEIRPdBm: 16},
}

// shiftSubBands moves a table by a frequency offset, for the AS923 variants.
func shiftSubBands(bands []SubBand, offsetHz uint32, negative bool) []SubBand {
	out := make([]SubBand, len(bands))
	copy(out, bands)
	if offsetHz == 0 {
		return out
	}
	for i := range out {
		if negative {
			out[i].MinHz -= offsetHz
			out[i].MaxHz -= offsetHz
			continue
		}
		out[i].MinHz += offsetHz
		out[i].MaxHz += offsetHz
	}
	return out
}
