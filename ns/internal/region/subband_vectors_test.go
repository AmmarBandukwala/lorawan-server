package region_test

import (
	"strings"
	"testing"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/region"
)

func TestEU868SubBands(t *testing.T) {
	r, err := region.New("EU868")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cases := []struct {
		freqHz uint32
		want   float64 // duty cycle; -1 means "no sub-band defined"
	}{
		{433_500_000, 0.001},
		{863_000_000, 0.001}, // lower bound inclusive
		{864_999_999, 0.001},
		{865_000_000, 0.01}, // range boundary moves to the next range
		{868_100_000, 0.01}, // the default uplink channels
		{868_500_000, 0.01},
		{868_599_999, 0.01},
		{868_650_000, -1}, // gap: not a LoRaWAN channel
		{868_700_000, 0.001},
		{869_200_000, -1},  // gap
		{869_525_000, 0.1}, // RX2, the generous range
		{869_649_999, 0.1},
		{869_700_000, 0.01},
		{870_000_000, -1}, // upper bound exclusive
	}
	for _, tc := range cases {
		sb, ok := r.SubBandFor(tc.freqHz)
		if tc.want < 0 {
			if ok {
				t.Errorf("SubBandFor(%d) = %v, want no sub-band", tc.freqHz, sb)
			}
			continue
		}
		if !ok {
			t.Errorf("SubBandFor(%d) found nothing, want duty cycle %v", tc.freqHz, tc.want)
			continue
		}
		if sb.DutyCycle != tc.want {
			t.Errorf("SubBandFor(%d).DutyCycle = %v, want %v", tc.freqHz, sb.DutyCycle, tc.want)
		}
	}
}

// TestSubBandBudgetIsInTimeNotPackets is the whole point of tracking airtime: a
// 1% duty cycle buys 36 seconds of transmission per hour, and how many frames
// that is depends entirely on their data rate.
func TestSubBandBudgetIsInTimeNotPackets(t *testing.T) {
	r, _ := region.New("EU868")
	window := time.Hour

	for _, tc := range []struct {
		freqHz uint32
		want   time.Duration
	}{
		{868_100_000, 36 * time.Second},        // 1% of an hour
		{869_525_000, 6 * time.Minute},         // 10% of an hour
		{864_000_000, 3600 * time.Millisecond}, // 0.1% of an hour
	} {
		sb, ok := r.SubBandFor(tc.freqHz)
		if !ok {
			t.Fatalf("no sub-band at %d", tc.freqHz)
		}
		if got := sb.Budget(window); got != tc.want {
			t.Errorf("budget at %d = %v, want %v", tc.freqHz, got, tc.want)
		}
	}

	// How many SF12 join-accepts fit the 1% budget on an 868 MHz channel: a
	// 17-byte join-accept at DR0 costs 1.319 s (computed above), and 36 s of
	// hourly budget therefore holds 27 of them. That is the number an operator
	// needs when a deployment grows past it.
	sb, _ := r.SubBandFor(868_100_000)
	dr, _ := r.DataRate(0)
	toa, err := region.TimeOnAir(dr, 17) // join-accept length
	if err != nil {
		t.Fatalf("TimeOnAir: %v", err)
	}
	if want := 1318912 * time.Microsecond; toa != want {
		t.Fatalf("join-accept airtime = %v, want %v", toa, want)
	}
	if frames := sb.Budget(window) / toa; frames != 27 {
		t.Errorf("the 1%% hourly budget holds %d SF12 join-accepts, want 27", frames)
	}
}

func TestUS915SubBandsAreUnlimited(t *testing.T) {
	r, _ := region.New("US915")
	for _, freq := range []uint32{903_000_000, 914_899_999, 923_900_000, 927_499_999} {
		sb, ok := r.SubBandFor(freq)
		if !ok {
			t.Errorf("no sub-band at %d", freq)
			continue
		}
		if sb.DutyCycle != 1.0 {
			t.Errorf("duty cycle at %d = %v, want 1.0 (no limit)", freq, sb.DutyCycle)
		}
		// An unlimited duty cycle must consume no budget decision at all.
		if got := sb.Budget(time.Hour); got != time.Hour {
			t.Errorf("budget = %v, want the whole window", got)
		}
	}
	if _, ok := r.SubBandFor(928_000_000); ok {
		t.Error("928 MHz is above the band and must not resolve")
	}
}

// TestAS923VariantsShiftTheSubBand: the variants move the whole channel plan by
// a fixed offset, so a sub-band left at the base frequencies would refuse the
// variant's own RX2 channel as "outside the band" while permitting a frequency
// nobody is allowed to use.
func TestAS923VariantsShiftTheSubBand(t *testing.T) {
	base, err := region.New("AS923")
	if err != nil {
		t.Fatalf("region.New(AS923): %v", err)
	}
	if sb, ok := base.SubBandFor(923_200_000); !ok || sb.DutyCycle != 0.01 {
		t.Errorf("AS923 RX2 923.2 MHz = %+v (found=%v), want the 1%% range", sb, ok)
	}

	// AS923-2 shifts everything down by 1.8 MHz, taking its RX2 to 921.4 MHz.
	js1, err := region.New("AS923-2")
	if err != nil {
		t.Fatalf("region.New(AS923-2): %v", err)
	}
	if sb, ok := js1.SubBandFor(921_400_000); !ok || sb.DutyCycle != 0.01 {
		t.Errorf("AS923-2 RX2 921.4 MHz = %+v (found=%v), want the shifted 1%% range", sb, ok)
	}
	if _, ok := js1.SubBandFor(923_200_000); ok {
		t.Error("AS923-2 must not resolve the unshifted RX2 frequency")
	}
}

func TestSubBandString(t *testing.T) {
	r, _ := region.New("EU868")
	sb, ok := r.SubBandFor(869_525_000)
	if !ok {
		t.Fatal("no sub-band at 869.525 MHz")
	}
	got := sb.String()
	for _, want := range []string{"869.400", "869.650", "10%"} {
		if !strings.Contains(got, want) {
			t.Errorf("String() = %q, missing %q", got, want)
		}
	}
}

// TestSubBandsDoNotOverlap catches a table edit that introduces an ambiguous
// range: the first match wins in the lookup, so an overlap silently changes
// which limit applies.
func TestSubBandsDoNotOverlap(t *testing.T) {
	for _, name := range []string{"EU868", "US915", "AS923", "AS923-2", "AS923-3", "AS923-4"} {
		r, err := region.New(name)
		if err != nil {
			t.Fatalf("region.New(%s): %v", name, err)
		}
		bands := r.SubBands()
		if len(bands) == 0 {
			t.Errorf("%s has no sub bands; duty cycle cannot be enforced", name)
			continue
		}
		for i := range bands {
			if bands[i].MaxHz <= bands[i].MinHz {
				t.Errorf("%s: sub-band %v has an empty range", name, bands[i])
			}
			for j := i + 1; j < len(bands); j++ {
				if bands[i].Contains(bands[j].MinHz) || bands[j].Contains(bands[i].MinHz) {
					t.Errorf("%s: %v overlaps %v", name, bands[i], bands[j])
				}
			}
		}
	}
}
