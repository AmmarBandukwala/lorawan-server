package contract_test

import (
	"testing"

	"github.com/fieldnode/lorawan-server/ns/internal/region"
)

func ch(freq uint32, rates ...int) region.Channel {
	return region.Channel{FrequencyHz: freq, DataRates: rates, Enabled: true}
}

// TestWithUserChannelsExtendsThePlan covers the ordinary case: a channel beyond
// the band's fixed plan is appended, enabled, and marked user-defined.
func TestWithUserChannelsExtendsThePlan(t *testing.T) {
	eu868, err := region.New("EU868")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	fixed := eu868.ChannelCount()

	reg, err := region.WithUserChannels(eu868, map[int]region.Channel{
		fixed: {FrequencyHz: 869400000, DataRates: []int{0, 1, 2}},
	})
	if err != nil {
		t.Fatalf("WithUserChannels: %v", err)
	}
	if reg.ChannelCount() != fixed+1 {
		t.Fatalf("ChannelCount = %d, want %d", reg.ChannelCount(), fixed+1)
	}
	extra := reg.UplinkChannels()[fixed]
	if extra.FrequencyHz != 869400000 || !extra.Enabled || !extra.UserDefined {
		t.Errorf("user channel = %+v", extra)
	}
	// The band object itself must be untouched: two devices on the same band
	// routinely have different plans.
	if eu868.ChannelCount() != fixed {
		t.Error("the wrapped band was mutated")
	}

	// The default mask covers the added channel, and the added channel's data
	// rates are visible to ADR.
	mask := reg.DefaultChannelMask()
	if len(mask) != fixed+1 || !mask.Enabled(fixed) {
		t.Errorf("default mask = %s, want the user channel enabled", mask)
	}
	// The default EU868 channels carry up to DR5, so the whole plan still allows
	// it; the new channel's own ceiling only shows when the mask leaves it as
	// the only channel enabled.
	if got := reg.MaxDataRateForMask(mask); got != 5 {
		t.Errorf("MaxDataRateForMask = %d, want 5 (the default channels still allow DR5)", got)
	}
	only := make(region.ChannelMask, len(mask))
	only[fixed] = true
	if got := reg.MaxDataRateForMask(only); got != 2 {
		t.Errorf("MaxDataRateForMask over the user channel alone = %d, want 2", got)
	}
	if !reg.SupportsDataRateOnChannel(fixed, 1) || reg.SupportsDataRateOnChannel(fixed, 5) {
		t.Error("SupportsDataRateOnChannel disagrees with the channel's data-rate list")
	}
}

// TestWithUserChannelsOverwritesAFixedChannel is legal and rare: bringing a
// device that learned a different plan back in line.
func TestWithUserChannelsOverwritesAFixedChannel(t *testing.T) {
	eu868, _ := region.New("EU868")
	reg, err := region.WithUserChannels(eu868, map[int]region.Channel{
		0: {FrequencyHz: 867700000, DataRates: []int{0, 1, 2, 3, 4, 5}},
	})
	if err != nil {
		t.Fatalf("WithUserChannels: %v", err)
	}
	if reg.ChannelCount() != eu868.ChannelCount() {
		t.Errorf("ChannelCount = %d, want the band's %d", reg.ChannelCount(), eu868.ChannelCount())
	}
	first := reg.UplinkChannels()[0]
	if first.FrequencyHz != 867700000 || !first.UserDefined {
		t.Errorf("channel 0 = %+v", first)
	}
}

// TestWithUserChannelsRejectsAGap: a missing index would make the mask length
// ambiguous, and a device cannot enable a channel it was never told about.
func TestWithUserChannelsRejectsAGap(t *testing.T) {
	eu868, _ := region.New("EU868")
	fixed := eu868.ChannelCount()
	if _, err := region.WithUserChannels(eu868, map[int]region.Channel{
		fixed + 3: {FrequencyHz: 869400000, DataRates: []int{0}},
	}); err == nil {
		t.Error("a gap in the user channel indices must be refused")
	}
	if _, err := region.WithUserChannels(eu868, map[int]region.Channel{
		fixed: {DataRates: []int{0}},
	}); err == nil {
		t.Error("a user channel with no frequency must be refused")
	}
	if _, err := region.WithUserChannels(eu868, map[int]region.Channel{
		-1: {FrequencyHz: 869400000},
	}); err == nil {
		t.Error("a negative channel index must be refused")
	}
}

// TestUserChannelsMaskKeepsUserChannelsEnabled is the subtle one. A LinkADRReq
// carries a 16-bit ChMask plus a block selector, so it can only ever address the
// fixed plan. Applying it must not switch off channels it never mentioned —
// otherwise every ADR command would wipe a device's user plan.
func TestUserChannelsMaskKeepsUserChannelsEnabled(t *testing.T) {
	eu868, _ := region.New("EU868")
	fixed := eu868.ChannelCount()
	reg, err := region.WithUserChannels(eu868, map[int]region.Channel{
		fixed: {FrequencyHz: 869400000, DataRates: []int{0, 1, 2}},
	})
	if err != nil {
		t.Fatalf("WithUserChannels: %v", err)
	}
	current := reg.DefaultChannelMask()

	// Turn channel 0 off through block 0 and leave the rest of the fixed plan on.
	out, err := reg.ApplyChannelMask(current, 0xFFFE, 0)
	if err != nil {
		t.Fatalf("ApplyChannelMask: %v", err)
	}
	if len(out) != fixed+1 {
		t.Fatalf("mask length = %d, want %d", len(out), fixed+1)
	}
	if out.Enabled(0) {
		t.Error("channel 0 should have been switched off")
	}
	if !out.Enabled(fixed) {
		t.Error("the user channel must survive a mask that never addressed it")
	}
}

// TestUserChannelsDelegatesEverythingElse guards the embedding: the rates, RX1
// mapping, power table and payload limits still come from the band.
func TestUserChannelsDelegatesEverythingElse(t *testing.T) {
	eu868, _ := region.New("EU868")
	reg, err := region.WithUserChannels(eu868, map[int]region.Channel{
		eu868.ChannelCount(): {FrequencyHz: 869400000, DataRates: []int{0}},
	})
	if err != nil {
		t.Fatalf("WithUserChannels: %v", err)
	}
	if got := reg.Defaults().RX2FrequencyHz; got != eu868.Defaults().RX2FrequencyHz {
		t.Errorf("RX2 frequency = %d, want the band's %d", got, eu868.Defaults().RX2FrequencyHz)
	}
	if len(reg.TXPowerOffsets()) != len(eu868.TXPowerOffsets()) {
		t.Error("the transmit-power table must come from the band")
	}
	if got, err := reg.RX1DataRate(5, 0); err != nil {
		t.Errorf("RX1DataRate: %v", err)
	} else if want, _ := eu868.RX1DataRate(5, 0); got != want {
		t.Errorf("RX1DataRate = %d, want the band's %d", got, want)
	}
	// No user channels means the band itself, unchanged.
	same, err := region.WithUserChannels(eu868, nil)
	if err != nil || same != eu868 {
		t.Errorf("region.WithUserChannels with nothing to add should return the band: %v/%v", same, err)
	}
}
