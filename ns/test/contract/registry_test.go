package contract_test

import (
	"strings"
	"testing"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/region"
)

// The contract every implemented band must satisfy. "Supports multiple regions"
// is only worth claiming if each band is usable end to end rather than merely
// constructible: a band with no channels, or whose RX1 mapping fails on its own
// frequencies, joins and then never receives.

// TestDefaultRegionIsUS915 pins the deployment default.
//
// It is a product decision, so it is asserted as one: the value is written here
// rather than derived from the constant, and changing the default of a network
// that is already deployed should have to break a test on purpose.
func TestDefaultRegionIsUS915(t *testing.T) {
	if region.DefaultRegion != "US915" {
		t.Errorf("DefaultRegion = %q, want US915", region.DefaultRegion)
	}
	// The default must be a band this build implements, or a deployment that
	// forgets NS_REGION fails at startup instead of serving.
	if !region.Supported(region.DefaultRegion) {
		t.Errorf("DefaultRegion %q is not implemented", region.DefaultRegion)
	}
	// And it must be first in the list the console offers, so the band a
	// deployment runs is the one an operator sees at the top of the dropdown.
	if got := region.Names(); len(got) == 0 || got[0] != region.DefaultRegion {
		t.Errorf("Names() = %v, want the default band first", got)
	}
}

// TestNamesMatchConstructor is the drift guard. Names() is what the console
// offers; New() is what the server accepts. When the two disagree an operator
// either cannot select a band that works, or selects one the server rejects
// after saving.
func TestNamesMatchConstructor(t *testing.T) {
	names := region.Names()
	if len(names) == 0 {
		t.Fatal("Names() returned no bands")
	}
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			t.Errorf("%s is listed twice", name)
		}
		seen[name] = true

		r, err := region.New(name)
		if err != nil {
			t.Errorf("New(%q) failed although Names() advertises it: %v", name, err)
			continue
		}
		if r.CommonName() != name {
			t.Errorf("New(%q).CommonName() = %q", name, r.CommonName())
		}
	}

	// A band outside the registry must be refused, and the error must name what
	// is available: an operator who types AU915 needs to learn that this build
	// does not have it, not just that the string was wrong.
	_, err := region.New("AU915")
	if err == nil {
		t.Fatal("New(\"AU915\") succeeded although it is not advertised")
	}
	if !strings.Contains(err.Error(), "AU915") {
		t.Errorf("error %q does not name the rejected band", err)
	}
}

// TestRegionNamesAreSpellingTolerant covers the operator path: band names get
// typed into a .env file, where lower case and underscores are normal.
func TestRegionNamesAreSpellingTolerant(t *testing.T) {
	for _, spelling := range []string{"us915", " US915 ", "us_915", "US_915"} {
		r, err := region.New(spelling)
		if err != nil {
			t.Errorf("New(%q): %v", spelling, err)
			continue
		}
		if r.CommonName() != "US915" {
			t.Errorf("New(%q).CommonName() = %q, want US915", spelling, r.CommonName())
		}
	}
	for _, spelling := range []string{"as923-2", "AS923_2", "as9232"} {
		if !region.Supported(spelling) {
			t.Errorf("Supported(%q) = false, want true", spelling)
		}
	}
	// A hyphen typed where the canonical name has none is accepted for the same
	// reason: the separator is dropped on both sides, and nothing collides.
	if !region.Supported("EU-868") {
		t.Error(`Supported("EU-868") = false, want true`)
	}
	for _, bad := range []string{"", "  ", "EU", "EU968", "AU915", "US916", "AS923-9"} {
		if region.Supported(bad) {
			t.Errorf("Supported(%q) = true, want false", bad)
		}
	}
}

// TestNormalizedNamesDoNotCollide is the precondition for spelling tolerance:
// dropping separators is only safe while no two bands share a normalized form.
// Adding, say, AS923-2 and AS923_2 as distinct bands would break this.
func TestNormalizedNamesDoNotCollide(t *testing.T) {
	seen := map[string]string{}
	for _, name := range region.Names() {
		key := strings.ToUpper(strings.NewReplacer("-", "", "_", "", " ", "").Replace(name))
		if other, dup := seen[key]; dup {
			t.Errorf("%s and %s both normalize to %s; the spelling-tolerant lookup "+
				"cannot tell them apart", name, other, key)
		}
		seen[key] = name
	}
}

// TestDefaultRegionDelayMatchesBand guards the one place a wrong default is
// invisible: the engine's fallback band and the process default must be the same
// band, or a session created before the region is resolved gets the wrong RX
// timing.
func TestDefaultRegionDelayMatchesBand(t *testing.T) {
	r := mustRegion(t, region.DefaultRegion)
	if r.Defaults().RX1Delay != time.Second {
		t.Errorf("%s RX1 delay = %v, want 1s", region.DefaultRegion, r.Defaults().RX1Delay)
	}
}

// TestEveryBandIsOperable walks each advertised band through the operations the
// engine performs on it. A band that fails any of these can accept a join and
// then fail to schedule the reply, which is the hardest class of bug to see from
// the outside.
func TestEveryBandIsOperable(t *testing.T) {
	for _, name := range region.Names() {
		t.Run(name, func(t *testing.T) {
			r := mustRegion(t, name)
			def := r.Defaults()

			up := r.UplinkChannels()
			if len(up) == 0 {
				t.Fatal("no uplink channels")
			}
			if len(r.DownlinkChannels()) == 0 {
				t.Fatal("no downlink channels")
			}
			if def.MinUplinkDataRate > def.MaxUplinkDataRate {
				t.Errorf("data-rate range is inverted: DR%d..DR%d",
					def.MinUplinkDataRate, def.MaxUplinkDataRate)
			}
			if def.RX2FrequencyHz == 0 {
				t.Error("RX2 has no frequency, so a device listening there would be ignored")
			}
			if def.RX1Delay <= 0 || def.RX2Delay <= def.RX1Delay {
				t.Errorf("RX delays are not ordered: RX1 %v, RX2 %v", def.RX1Delay, def.RX2Delay)
			}
			if def.JoinAcceptDelay1 <= 0 || def.JoinAcceptDelay2 <= def.JoinAcceptDelay1 {
				t.Errorf("join-accept delays are not ordered: %v then %v",
					def.JoinAcceptDelay1, def.JoinAcceptDelay2)
			}
			if len(r.TXPowerOffsets()) == 0 {
				t.Error("no transmit-power steps, so LinkADRReq cannot set one")
			}

			// Every data rate in the advertised range must resolve, because ADR
			// may be asked to move a device to any of them.
			for dr := 0; dr <= 15; dr++ {
				if _, err := r.DataRate(dr); err != nil && dr <= def.MaxUplinkDataRate {
					t.Errorf("DataRate(%d) fails inside the advertised range: %v", dr, err)
				}
			}

			// The mask the band hands out must be accepted by the band and must
			// leave something enabled.
			mask := r.DefaultChannelMask()
			if got := r.EnabledUplinkChannels(mask); len(got) == 0 {
				t.Error("the default channel mask enables nothing")
			}
			if max := r.MaxDataRateForMask(mask); max < def.MinUplinkDataRate {
				t.Errorf("the default mask carries up to DR%d, below the band minimum DR%d",
					max, def.MinUplinkDataRate)
			}

			// RX1 mapping must work for every channel the band can receive on.
			// A gap here means a device that transmits on that channel never
			// hears the reply.
			for i, ch := range up {
				if !ch.Enabled {
					continue
				}
				dr, err := r.RX1DataRate(def.MinUplinkDataRate, 0)
				if err != nil {
					t.Fatalf("RX1DataRate(DR%d, 0) on channel %d: %v", def.MinUplinkDataRate, i, err)
				}
				if _, err := r.DataRate(dr); err != nil {
					t.Errorf("RX1 mapped to DR%d, which the band does not define", dr)
				}
				f, err := r.RX1FrequencyHz(ch.FrequencyHz)
				if err != nil {
					t.Errorf("RX1FrequencyHz(%.3f MHz): %v", float64(ch.FrequencyHz)/1e6, err)
					continue
				}
				if f == 0 {
					t.Errorf("channel %d at %.3f MHz maps to an RX1 frequency of 0",
						i, float64(ch.FrequencyHz)/1e6)
				}
			}

			// Duty-cycle accounting must be able to answer for the band's own
			// RX2 frequency, which is where most transmissions go.
			if _, ok := r.SubBandFor(def.RX2FrequencyHz); !ok {
				t.Errorf("RX2 at %.3f MHz falls outside every sub-band, so duty cycle "+
					"cannot be accounted for it", float64(def.RX2FrequencyHz)/1e6)
			}
			if r.UplinkDwellTime() < 0 || r.DownlinkDwellTime() < 0 {
				t.Error("dwell times must not be negative")
			}
			// A downlink data rate must have a maximum payload, or the engine
			// cannot tell whether a frame fits in the window.
			for dr := 0; dr <= 15; dr++ {
				d, err := r.DataRate(dr)
				if err != nil || !d.Downlink {
					continue
				}
				if _, ok := r.MaxDownlinkPayloadSize(dr); !ok {
					t.Errorf("downlink DR%d has no maximum payload size", dr)
				}
			}
		})
	}
}

// TestBandsDifferWhereTheyShould is a sanity check on the registry itself: the
// AS923 variants exist only because their frequencies differ, so a refactor that
// made them identical would still pass every structural test above.
//
// The offsets follow ChirpStack's lrwn, which applies the variant offset to the
// RX2 frequency as well as to the uplink channels (as923.rs:
// `rx2_frequency: 923200000 - self.freq_offset`). RP002 itself keeps RX2 at
// 921.5 MHz for AS923-2 and AS923-3, so this is a deliberate choice to be
// behaviourally equivalent to ChirpStack rather than to the letter of the
// regional parameters.
func TestBandsDifferWhereTheyShould(t *testing.T) {
	seen := map[uint32]string{}
	for _, name := range []string{"AS923", "AS923-2", "AS923-3", "AS923-4"} {
		r := mustRegion(t, name)
		f := r.Defaults().RX2FrequencyHz
		if other, dup := seen[f]; dup {
			t.Errorf("%s and %s share RX2 %.3f MHz; the variants are meant to differ",
				name, other, float64(f)/1e6)
		}
		seen[f] = name

		// The offset must also move the uplink plan, or a device would transmit
		// on a frequency the variant does not license.
		if up := r.UplinkChannels(); len(up) > 0 {
			if up[0].FrequencyHz >= 923_200_000 && name != "AS923" {
				t.Errorf("%s uplink channel 0 is %.3f MHz; the frequency offset was not applied",
					name, float64(up[0].FrequencyHz)/1e6)
			}
		}
	}

	// US915 is the band whose behaviour differs most: its RX1 frequency is not
	// the uplink frequency. If that ever regressed to identity, US915 devices
	// would join and never be heard from again.
	us915 := mustRegion(t, "US915")
	up := us915.UplinkChannels()[0]
	rx1, err := us915.RX1FrequencyHz(up.FrequencyHz)
	if err != nil {
		t.Fatalf("RX1FrequencyHz: %v", err)
	}
	if rx1 == up.FrequencyHz {
		t.Errorf("US915 RX1 equals the uplink frequency %.3f MHz; US915 uses a channel map",
			float64(up.FrequencyHz)/1e6)
	}
	// 902.3 MHz uplink maps to downlink channel 0 at 923.3 MHz (RP002 1.0.3
	// table 6: 923.3 + (channel mod 8) * 0.6 MHz).
	if want := uint32(923_300_000); rx1 != want {
		t.Errorf("US915 channel 0 RX1 = %.3f MHz, want %.3f MHz",
			float64(rx1)/1e6, float64(want)/1e6)
	}

	// EU868 is the opposite case: RX1 is on the uplink channel.
	eu868 := mustRegion(t, "EU868")
	if got, err := eu868.RX1FrequencyHz(868_100_000); err != nil || got != 868_100_000 {
		t.Errorf("EU868 RX1(868.1 MHz) = %d Hz (err %v), want 868100000", got, err)
	}
}
