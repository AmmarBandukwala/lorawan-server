package region

import "fmt"

// UserChannels is a per-device view of a band with dynamic channels added.
//
// LoRaWAN lets the network define channels beyond the band's fixed plan with
// NewChannelReq (RP002 5.5.2.3). Those channels belong to one device, not to the
// band: two devices on the same frequency plan routinely have different channel
// tables, and sharing them would let one device's plan steer another's data
// rate. So the band object stays immutable and this wrapper layers one device's
// additions on top of it.
//
// Everything not listed here delegates to the wrapped band. The methods that are
// overridden are the ones whose answers depend on the channel list — and the
// band's own implementations read its channel slice directly, so overriding
// UplinkChannels alone would not be enough.
type UserChannels struct {
	Region

	channels []Channel
}

// MaxUserChannelIndex is the highest channel index a device can address with
// NewChannelReq (RP002: the ChIndex field is 8 bits, and the specification
// reserves 0..71 for the fixed plans).
const MaxUserChannelIndex = 255

// WithUserChannels returns a view of reg in which the given channels replace or
// extend the band's uplink plan. Channels whose index is below the band's own
// count overwrite a fixed channel; higher indices are appended.
//
// Overwriting a fixed channel is legal and is how a device that already knows a
// different plan is brought back in line, but it is rare; the returned view is
// what the engine must use for that device's data-rate and frequency decisions.
func WithUserChannels(reg Region, user map[int]Channel) (Region, error) {
	if len(user) == 0 {
		return reg, nil
	}
	base := reg.UplinkChannels()
	channels := make([]Channel, len(base))
	copy(channels, base)

	for idx, ch := range user {
		if idx < 0 {
			return nil, fmt.Errorf("region: user channel index %d is negative", idx)
		}
		if idx > MaxUserChannelIndex {
			return nil, fmt.Errorf("region: user channel index %d above the protocol maximum %d",
				idx, MaxUserChannelIndex)
		}
		if ch.FrequencyHz == 0 {
			return nil, fmt.Errorf("region: user channel %d has no frequency", idx)
		}
		if idx < len(channels) {
			channels[idx] = Channel{
				FrequencyHz: ch.FrequencyHz,
				DataRates:   ch.DataRates,
				// A redefined channel keeps whatever enable state the device had.
				Enabled:     channels[idx].Enabled,
				UserDefined: true,
			}
			continue
		}
		// Indices beyond the fixed plan must be added in order; a gap would make
		// the mask length ambiguous, and a device cannot enable a channel it has
		// never been told about.
		if idx != len(channels) {
			return nil, fmt.Errorf("region: user channel index %d leaves a gap (next free is %d)",
				idx, len(channels))
		}
		channels = append(channels, Channel{
			FrequencyHz: ch.FrequencyHz,
			DataRates:   ch.DataRates,
			Enabled:     true,
			UserDefined: true,
		})
	}
	return &UserChannels{Region: reg, channels: channels}, nil
}

// UplinkChannels returns the band plan with the device's user channels merged in.
func (u *UserChannels) UplinkChannels() []Channel { return u.channels }

// ChannelCount counts the merged plan, so a device's mask covers its user
// channels too.
func (u *UserChannels) ChannelCount() int { return len(u.channels) }

// DefaultChannelMask enables everything, including the user channels: a channel
// the network asked the device to add is a channel the network wants in use.
func (u *UserChannels) DefaultChannelMask() ChannelMask {
	m := make(ChannelMask, len(u.channels))
	for i, ch := range u.channels {
		m[i] = ch.Enabled
	}
	return m
}

// EnabledUplinkChannels lists the channels a mask turns on.
func (u *UserChannels) EnabledUplinkChannels(m ChannelMask) []Channel {
	out := make([]Channel, 0, m.Count())
	for i, ch := range u.channels {
		if m.Enabled(i) {
			out = append(out, ch)
		}
	}
	return out
}

// MaxDataRateForMask is the highest uplink data rate an enabled channel carries.
func (u *UserChannels) MaxDataRateForMask(m ChannelMask) int {
	best := -1
	for i, ch := range u.channels {
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

// SupportsDataRateOnChannel reports whether a channel can carry a data rate,
// which is what makes a device's user channel usable by ADR.
func (u *UserChannels) SupportsDataRateOnChannel(channelIndex, dataRateIndex int) bool {
	if channelIndex < 0 || channelIndex >= len(u.channels) {
		return false
	}
	for _, i := range u.channels[channelIndex].DataRates {
		if i == dataRateIndex {
			return true
		}
	}
	return false
}

// ApplyChannelMask delegates to the band, then grows the mask when the device
// has user channels beyond the band's fixed plan.
//
// ChMaskCntl only ever addresses the fixed plan (blocks of 16 starting at 0), so
// a mask coming from a LinkADRReq says nothing about channels 64 and up. Those
// keep their enabled state instead of being switched off by a mask that never
// mentioned them — which is the difference between a device losing one channel
// and losing its whole user plan on the next ADR command.
func (u *UserChannels) ApplyChannelMask(current ChannelMask, chMask uint16, chMaskCntl byte) (ChannelMask, error) {
	fixed := u.Region.ChannelCount()
	in := current
	if len(in) > fixed {
		in = in[:fixed]
	}
	out, err := u.Region.ApplyChannelMask(in, chMask, chMaskCntl)
	if err != nil {
		return nil, err
	}
	if len(out) == fixed && len(u.channels) > fixed {
		extended := make(ChannelMask, len(u.channels))
		copy(extended, out)
		for i := fixed; i < len(u.channels); i++ {
			extended[i] = current.Enabled(i)
		}
		return extended, nil
	}
	return out, nil
}
