package region_test

import (
	"testing"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/region"
)

func mustRegion(t *testing.T, name string) region.Region {
	t.Helper()
	r, err := region.New(name)
	if err != nil {
		t.Fatalf("region.New(%q): %v", name, err)
	}
	return r
}

func TestEU868(t *testing.T) {
	r := mustRegion(t, "EU868")
	d := r.Defaults()
	if d.RX2FrequencyHz != 869_525_000 || d.RX2DataRate != 0 {
		t.Errorf("RX2 = %d Hz DR%d, want 869525000 Hz DR0", d.RX2FrequencyHz, d.RX2DataRate)
	}
	if d.RX1Delay != time.Second || d.RX2Delay != 2*time.Second {
		t.Errorf("delays = %v/%v, want 1s/2s", d.RX1Delay, d.RX2Delay)
	}
	if d.JoinAcceptDelay1 != 5*time.Second || d.JoinAcceptDelay2 != 6*time.Second {
		t.Errorf("join-accept delays = %v/%v, want 5s/6s", d.JoinAcceptDelay1, d.JoinAcceptDelay2)
	}

	cases := []struct{ upDR, offset, want int }{
		{5, 0, 5}, {5, 2, 3}, {0, 5, 0}, {13, 1, 12}, {12, 5, 1},
	}
	for _, c := range cases {
		got, err := r.RX1DataRate(c.upDR, c.offset)
		if err != nil || got != c.want {
			t.Errorf("RX1DataRate(%d, %d) = %d/%v, want %d", c.upDR, c.offset, got, err, c.want)
		}
	}
	if _, err := r.RX1DataRate(5, 6); err == nil {
		t.Error("RX1DROffset 6 must be rejected for EU868 (RFU)")
	}

	if got := r.DownlinkTXPowerdBm(869_525_000); got != 29 {
		t.Errorf("EIRP at 869.525 MHz = %d, want 29", got)
	}
	if got := r.DownlinkTXPowerdBm(868_100_000); got != 16 {
		t.Errorf("EIRP at 868.1 MHz = %d, want 16", got)
	}
	// The RX2 channel sits in the one EU868 range with a generous duty cycle,
	// which is why it stays available when the 868 MHz channels are exhausted.
	if sb, ok := r.SubBandFor(869_525_000); !ok || sb.DutyCycle != 0.1 {
		t.Errorf("sub-band at 869.525 MHz = %+v (found=%v), want 10%% duty cycle", sb, ok)
	}
	if sb, ok := r.SubBandFor(868_100_000); !ok || sb.DutyCycle != 0.01 {
		t.Errorf("sub-band at 868.1 MHz = %+v (found=%v), want 1%% duty cycle", sb, ok)
	}
	f, err := r.RX1FrequencyHz(868_300_000)
	if err != nil || f != 868_300_000 {
		t.Errorf("RX1Frequency(868.3) = %d/%v, want 868300000", f, err)
	}
	if len(r.UplinkChannels()) != 3 {
		t.Errorf("default uplink channels = %d, want 3", len(r.UplinkChannels()))
	}
}

func TestUS915(t *testing.T) {
	r := mustRegion(t, "US915")
	d := r.Defaults()
	if d.RX2FrequencyHz != 923_300_000 || d.RX2DataRate != 8 {
		t.Errorf("RX2 = %d Hz DR%d, want 923300000 Hz DR8", d.RX2FrequencyHz, d.RX2DataRate)
	}
	if len(r.UplinkChannels()) != 72 || len(r.DownlinkChannels()) != 8 {
		t.Errorf("channels = %d up / %d down, want 72/8", len(r.UplinkChannels()), len(r.DownlinkChannels()))
	}
	if got := r.UplinkChannels()[0].FrequencyHz; got != 902_300_000 {
		t.Errorf("uplink ch0 = %d, want 902300000", got)
	}
	if got := r.UplinkChannels()[64].FrequencyHz; got != 903_000_000 {
		t.Errorf("uplink ch64 = %d, want 903000000", got)
	}
	if got := r.DownlinkChannels()[7].FrequencyHz; got != 927_500_000 {
		t.Errorf("downlink ch7 = %d, want 927500000", got)
	}

	cases := []struct{ upDR, offset, want int }{
		{0, 0, 10}, {0, 2, 8}, {3, 1, 12}, {8, 0, 0}, {4, 3, 11},
	}
	for _, c := range cases {
		got, err := r.RX1DataRate(c.upDR, c.offset)
		if err != nil || got != c.want {
			t.Errorf("RX1DataRate(%d, %d) = %d/%v, want %d", c.upDR, c.offset, got, err, c.want)
		}
	}

	// Uplink channel 0 (902.3) -> downlink channel 0 (923.3);
	// uplink channel 9 (904.1) -> downlink channel 1 (923.9).
	if f, err := r.RX1FrequencyHz(902_300_000); err != nil || f != 923_300_000 {
		t.Errorf("RX1Frequency(902.3) = %d/%v, want 923300000", f, err)
	}
	if f, err := r.RX1FrequencyHz(904_100_000); err != nil || f != 923_900_000 {
		t.Errorf("RX1Frequency(904.1) = %d/%v, want 923900000", f, err)
	}
	// US915 imposes no duty cycle, and that must be an explicit value in the
	// table rather than a missing entry: a missing entry means "our channel plan
	// is wrong", not "transmit freely".
	if sb, ok := r.SubBandFor(923_900_000); !ok || sb.DutyCycle != 1.0 {
		t.Errorf("sub-band at 923.9 MHz = %+v (found=%v), want unlimited duty cycle", sb, ok)
	}
}

func TestAS923(t *testing.T) {
	r := mustRegion(t, "AS923")
	d := r.Defaults()
	if d.RX2FrequencyHz != 923_200_000 || d.RX2DataRate != 2 {
		t.Errorf("RX2 = %d Hz DR%d, want 923200000 Hz DR2", d.RX2FrequencyHz, d.RX2DataRate)
	}
	if r.UplinkDwellTime() != 400*time.Millisecond {
		t.Errorf("uplink dwell = %v, want 400ms", r.UplinkDwellTime())
	}
	// Dwell-time RX1 table floors everything at DR2 for low uplink DRs.
	if got, err := r.RX1DataRate(0, 0); err != nil || got != 2 {
		t.Errorf("RX1DataRate(0,0) = %d/%v, want 2", got, err)
	}
	if got, err := r.RX1DataRate(5, 1); err != nil || got != 4 {
		t.Errorf("RX1DataRate(5,1) = %d/%v, want 4", got, err)
	}

	// Frequency-offset variants.
	for name, wantRX2 := range map[string]uint32{
		"AS923-2": 921_400_000,
		"AS923-3": 916_600_000,
		"AS923-4": 917_300_000,
	} {
		v := mustRegion(t, name)
		if got := v.Defaults().RX2FrequencyHz; got != wantRX2 {
			t.Errorf("%s RX2 = %d, want %d", name, got, wantRX2)
		}
	}

	if _, err := region.New("KR920"); err == nil {
		t.Error("KR920 must not be supported yet")
	}
}

// TestMaxDownlinkPayloadSize checks the maximum downlink PHY-payload sizes the
// engine relies on to decide whether a frame fits a receive window.
//
// The numbers are lrwn's max_dl_payload_size_per_dr "m" values, not the LoRaWAN
// maximum of 255. Getting these wrong in the generous direction is the dangerous
// direction: the server then transmits a frame the device must discard, and the
// only symptom is a downlink that never arrives.
func TestMaxDownlinkPayloadSize(t *testing.T) {
	eu868 := mustRegion(t, "EU868")
	for dr, want := range map[int]int{0: 59, 2: 59, 3: 123, 4: 230, 5: 230, 6: 230, 7: 230} {
		got, ok := eu868.MaxDownlinkPayloadSize(dr)
		if !ok || got != want {
			t.Errorf("EU868 DR%d max payload = %d/%v, want %d", dr, got, ok, want)
		}
	}

	us915 := mustRegion(t, "US915")
	for dr, want := range map[int]int{8: 41, 9: 117, 10: 230, 13: 230} {
		got, ok := us915.MaxDownlinkPayloadSize(dr)
		if !ok || got != want {
			t.Errorf("US915 DR%d max payload = %d/%v, want %d", dr, got, ok, want)
		}
	}

	// DR8 is the US915 RX2 rate, so its limit is worth pinning on its own: it is
	// the single value most able to silently cost a deployment its downlinks.
	if got, _ := us915.MaxDownlinkPayloadSize(us915.Defaults().RX2DataRate); got != 41 {
		t.Errorf("US915 RX2 data rate max payload = %d, want 41", got)
	}

	as923 := mustRegion(t, "AS923")
	for dr, want := range map[int]int{0: 59, 3: 123, 4: 250, 7: 250} {
		if got, ok := as923.MaxDownlinkPayloadSize(dr); !ok || got != want {
			t.Errorf("AS923 DR%d max payload = %d/%v, want %d", dr, got, ok, want)
		}
	}

	// An uplink-only data rate has no downlink limit. The caller must see
	// "unknown" rather than a zero that would silently block every window.
	if got, ok := eu868.MaxDownlinkPayloadSize(15); ok {
		t.Errorf("EU868 DR15 max payload = %d/%v, want unknown", got, ok)
	}
	// LR-FHSS (EU868 DR8..DR11) is uplink-only, and so are the reserved DR12 and
	// DR13: none of them may be scheduled as a downlink.
	for _, dr := range []int{8, 9, 10, 11, 12, 13} {
		if d, err := eu868.DataRate(dr); err == nil && d.Downlink {
			t.Errorf("EU868 DR%d is marked downlink-capable but is uplink-only or reserved", dr)
		}
	}
}
