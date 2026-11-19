package region

import "fmt"

// ChannelMask is a device's set of enabled uplink channels, one entry per
// channel of the band and index-aligned with UplinkChannels.
//
// It is per-device state, not band state: a device may be restricted to a
// subset by LinkADRReq, and the network may have added user channels with
// NewChannelReq. The band only defines how many channels exist, which defaults
// are on, and what a ChMaskCntl value means.
type ChannelMask []bool

// Clone returns an independent copy, so applying a mask never mutates the
// session's current one before the device has accepted it.
func (m ChannelMask) Clone() ChannelMask {
	if m == nil {
		return nil
	}
	out := make(ChannelMask, len(m))
	copy(out, m)
	return out
}

// Count returns how many channels are enabled.
func (m ChannelMask) Count() int {
	n := 0
	for _, on := range m {
		if on {
			n++
		}
	}
	return n
}

// Indices returns the numbers of the enabled channels, which is the form the
// LinkADRReq payload builder needs.
func (m ChannelMask) Indices() []int {
	out := make([]int, 0, m.Count())
	for i, on := range m {
		if on {
			out = append(out, i)
		}
	}
	return out
}

// Enabled reports whether channel n is on. Out-of-range is false.
func (m ChannelMask) Enabled(n int) bool {
	return n >= 0 && n < len(m) && m[n]
}

// String renders the mask as hex nibbles for logs and events, e.g. "0f00".
// Bit 0 of a nibble is the lowest channel, matching the ChMask wire order.
func (m ChannelMask) String() string {
	const hexDigits = "0123456789abcdef"
	out := make([]byte, 0, (len(m)+3)/4)
	for i := 0; i < len(m); i += 4 {
		nibble := byte(0)
		for bit := 0; bit < 4 && i+bit < len(m); bit++ {
			if m[i+bit] {
				nibble |= 1 << bit
			}
		}
		out = append(out, hexDigits[nibble])
	}
	return string(out)
}

// maskStyle selects how a LinkADRReq ChMask/ChMaskCntl pair is interpreted.
// The bands disagree on this, and applying the wrong reading silently disables
// channels a device is still listening on, so it is explicit per band.
type maskStyle int

const (
	// maskStyleBlock reads channel = ChMaskCntl*16 + bit, the reading RP002
	// gives bands with more than 16 channels.
	maskStyleBlock maskStyle = iota

	// maskStyleRFRegion is the EU868 reading: ChMaskCntl 0b000 addresses
	// channels 0..15, and the values 0b100, 0b110 and 0b111 are not block
	// selectors but whole-band commands (all on, RF region 2+3, RF region 1).
	maskStyleRFRegion

	// maskStyleAllOn is the AS923 reading: 0b000 addresses channels 0..15 and
	// 0b100 enables all of them. The RF-region values EU868 defines have no
	// meaning here, so they are refused rather than guessed at.
	maskStyleAllOn
)

// ChannelCount is the number of uplink channels the band defines, including
// user-channel slots that are still empty.
func (b *base) ChannelCount() int { return len(b.uplinkChannels) }

// DefaultChannelMask returns the channels a device starts with, taken from the
// band table's Enabled flags.
func (b *base) DefaultChannelMask() ChannelMask {
	m := make(ChannelMask, len(b.uplinkChannels))
	for i, ch := range b.uplinkChannels {
		m[i] = ch.Enabled
	}
	return m
}

// ApplyChannelMask returns the mask a LinkADRReq with this ChMask/ChMaskCntl
// asks the device to use. current may be nil, in which case the band defaults
// are the starting point.
//
// A bit that addresses a channel the band does not have is not an error when it
// is 0 — masks are wider than the channel plan for forward and backward
// compatibility — but enabling a channel that does not exist is rejected,
// because a device transmitting there is invisible to the network.
func (b *base) ApplyChannelMask(current ChannelMask, chMask uint16, chMaskCntl byte) (ChannelMask, error) {
	out := current.Clone()
	if out == nil {
		out = b.DefaultChannelMask()
	}
	if len(out) != len(b.uplinkChannels) {
		// The band grew or the session predates a channel-plan change; keep the
		// bits that still map to a channel and take the rest from the defaults.
		fresh := b.DefaultChannelMask()
		copy(fresh, out)
		out = fresh
	}

	switch b.maskStyle {
	case maskStyleRFRegion:
		return b.applyRFRegionMask(out, chMask, chMaskCntl)
	case maskStyleAllOn:
		return b.applyAllOnMask(out, chMask, chMaskCntl)
	default:
		return b.applyBlockMask(out, chMask, chMaskCntl)
	}
}

// applyAllOnMask implements the AS923 reading of ChMaskCntl.
func (b *base) applyAllOnMask(out ChannelMask, chMask uint16, chMaskCntl byte) (ChannelMask, error) {
	switch chMaskCntl {
	case 0b000:
		for i := 0; i < 16 && i < len(out); i++ {
			out[i] = chMask&(1<<i) != 0
		}
		return out, nil
	case 0b100:
		for i := range out {
			out[i] = true
		}
		return out, nil
	default:
		return out, fmt.Errorf("region: %s does not define ChMaskCntl %d (0b%03b)",
			b.name, chMaskCntl, chMaskCntl)
	}
}

func (b *base) applyBlockMask(out ChannelMask, chMask uint16, chMaskCntl byte) (ChannelMask, error) {
	start := int(chMaskCntl) * 16
	for i := 0; i < 16; i++ {
		on := chMask&(1<<i) != 0
		idx := start + i
		if idx >= len(out) {
			if on {
				return out, fmt.Errorf("region: %s has no channel %d (ChMaskCntl %d, bit %d)",
					b.name, idx, chMaskCntl, i)
			}
			continue
		}
		out[idx] = on
	}
	return out, nil
}

// applyRFRegionMask implements the EU868/AS923 reading of ChMaskCntl.
func (b *base) applyRFRegionMask(out ChannelMask, chMask uint16, chMaskCntl byte) (ChannelMask, error) {
	switch chMaskCntl {
	case 0b000:
		for i := 0; i < 16 && i < len(out); i++ {
			out[i] = chMask&(1<<i) != 0
		}
		return out, nil
	case 0b100: // all channels on
		for i := range out {
			out[i] = true
		}
		return out, nil
	case 0b110: // RF region 2 and 3: channels 0..5
		for i := range out {
			out[i] = i < 6
		}
		return out, nil
	case 0b111: // RF region 1: the three default channels
		for i := range out {
			out[i] = i < 3
		}
		return out, nil
	default:
		return out, fmt.Errorf("region: %s does not define ChMaskCntl %d (0b%03b)",
			b.name, chMaskCntl, chMaskCntl)
	}
}

// EnabledUplinkChannels returns the band channels the mask enables, in order.
func (b *base) EnabledUplinkChannels(m ChannelMask) []Channel {
	out := make([]Channel, 0, m.Count())
	for i, ch := range b.uplinkChannels {
		if m.Enabled(i) {
			out = append(out, ch)
		}
	}
	return out
}

// SupportsDataRateOnChannel reports whether the band allows this data rate on
// the given channel index.
func (b *base) SupportsDataRateOnChannel(channelIndex, dataRateIndex int) bool {
	if channelIndex < 0 || channelIndex >= len(b.uplinkChannels) {
		return false
	}
	for _, i := range b.uplinkChannels[channelIndex].DataRates {
		if i == dataRateIndex {
			return true
		}
	}
	return false
}

// MaxDataRateForMask reports the highest uplink data rate that at least one
// enabled channel supports, or -1 when no enabled channel carries data.
//
// ADR must not raise a device above this: the device would either refuse with a
// negative LinkADRAns or keep transmitting on a data rate no enabled channel is
// provisioned for.
func (b *base) MaxDataRateForMask(m ChannelMask) int {
	best := -1
	for i, ch := range b.uplinkChannels {
		if !m.Enabled(i) {
			continue
		}
		for _, dr := range ch.DataRates {
			if dr > best {
				best = dr
			}
		}
	}
	return best
}
