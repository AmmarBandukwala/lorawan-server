package region

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Channel occupancy, which is what duty cycle and dwell-time limits are actually
// measured in.
//
// A duty cycle of 1% does not mean "1% of the packets"; it means the radio may
// occupy the channel for 1% of the time, so deciding whether a downlink may go
// out requires the airtime of that specific frame. The formulas here are the
// ones in the Semtech LoRa design guide (and reproduced in RP002): symbol time
// from spreading factor and bandwidth, a symbol count for the payload, and the
// two multiplied together.
//
// The payload length argument is the whole PHYPayload as it goes over the air —
// MHDR, FHDR, FRMPayload and MIC — because every one of those bytes occupies the
// channel. Passing the application payload length would understate the
// occupancy by roughly five bytes per frame.

// TimeOnAir returns how long phyPayloadBytes occupy the channel at dr.
func TimeOnAir(dr DataRate, phyPayloadBytes int) (time.Duration, error) {
	if phyPayloadBytes < 0 {
		return 0, fmt.Errorf("region: negative payload length %d", phyPayloadBytes)
	}
	switch dr.Modulation {
	case ModulationLoRa:
		return timeOnAirLoRa(dr, phyPayloadBytes)
	case ModulationFSK:
		return timeOnAirFSK(dr, phyPayloadBytes)
	default:
		// LR-FHSS occupies a frequency-hopping pattern rather than one channel,
		// so a single-channel duty-cycle budget cannot be computed from these
		// tables. It is also uplink-only in every band we support, so nothing
		// we schedule needs it.
		return 0, fmt.Errorf("region: no airtime model for %s modulation", dr.Modulation)
	}
}

// timeOnAirLoRa implements the design-guide formula:
//
//	Tsym       = 2^SF / BW
//	Npreamble  = 8 + 4.25                       (SF7..SF12; explicit preamble)
//	Npayload   = 8 + ceil((8·PL − 4·SF + 44) / (4·(SF − 2·de))) · CR
//	ToA        = (Npreamble + Npayload) · Tsym
//
// The numerator's constant is 28 for the physical header plus 16 for the
// frame's CRC, which assumes an explicit header and CRC enabled — both fixed by
// the LoRaWAN 1.0 specification, so there is no configuration to model here.
//
// de is the low-data-rate optimization flag. The transceiver turns it on
// automatically at SF11/SF12 with 125 kHz bandwidth, which is exactly the
// combination RP002 uses those settings for, so it is derived rather than
// configured. It lengthens each symbol's payload capacity denominator and thus
// the airtime; forgetting it understates SF12 frames by around 10%.
func timeOnAirLoRa(dr DataRate, phyPayloadBytes int) (time.Duration, error) {
	if dr.SpreadingFactor < 5 || dr.SpreadingFactor > 12 {
		return 0, fmt.Errorf("region: spreading factor %d outside 5..12", dr.SpreadingFactor)
	}
	if dr.BandwidthHz <= 0 {
		return 0, fmt.Errorf("region: bandwidth %d Hz is not usable", dr.BandwidthHz)
	}
	cr, err := codingRateDenominator(dr.CodingRate)
	if err != nil {
		return 0, err
	}

	sf := float64(dr.SpreadingFactor)
	bwKHz := float64(dr.BandwidthHz) / 1000.0

	de := 0.0
	if dr.BandwidthHz == 125_000 && (dr.SpreadingFactor == 11 || dr.SpreadingFactor == 12) {
		de = 1.0
	}

	symbolTime := math.Pow(2, sf) / bwKHz * float64(time.Millisecond)
	numerator := 8*float64(phyPayloadBytes) - 4*sf + 28 + 16
	denominator := 4 * (sf - 2*de)

	// The ceiling applies to the raw symbol count before the coding rate
	// multiplies it: the interleaver works on whole symbols, so rounding after
	// multiplying would claim airtime the radio does not use.
	payloadSymbols := 8 + math.Max(0, math.Ceil(numerator/denominator)*float64(cr))
	symbols := payloadSymbols + 8 + 4.25

	return time.Duration(math.Round(symbols * symbolTime)), nil
}

// timeOnAirFSK counts the frame's bits over the bitrate, plus the fixed
// overhead a receiver needs before it can read the frame: 5 preamble bytes, a
// 3-byte sync word, the 1-byte length field and a 2-byte CRC.
func timeOnAirFSK(dr DataRate, phyPayloadBytes int) (time.Duration, error) {
	if dr.Bitrate <= 0 {
		return 0, fmt.Errorf("region: FSK data rate without a bitrate")
	}
	bits := 8 * (phyPayloadBytes + 5 + 3 + 1 + 2)
	return time.Duration(math.Round(float64(bits) / float64(dr.Bitrate) * float64(time.Second))), nil
}

// codingRateDenominator reads a "4/5" style coding rate. Only 4/x rates exist
// in LoRaWAN 1.0; the implicit form ("4/5LI") that some transceivers report is
// accepted because it carries the same redundancy.
func codingRateDenominator(cr string) (int, error) {
	_, den, found := strings.Cut(strings.ToUpper(strings.TrimSpace(cr)), "/")
	if !found {
		return 0, fmt.Errorf("region: coding rate %q is not N/D", cr)
	}
	den = strings.TrimSuffix(den, "LI")
	n, err := strconv.Atoi(strings.TrimSpace(den))
	if err != nil {
		return 0, fmt.Errorf("region: coding rate %q has a non-numeric denominator", cr)
	}
	if n < 5 || n > 8 {
		return 0, fmt.Errorf("region: coding rate 4/%d outside the LoRaWAN range 4/5..4/8", n)
	}
	return n, nil
}
