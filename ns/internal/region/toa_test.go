package region

import (
	"math"
	"testing"
	"time"
)

// Hand-computed vectors. Each shows the arithmetic it pins, because a duty-cycle
// budget that is quietly wrong shows up as regulatory non-compliance rather than
// as a failing test, so these numbers have to be checkable by hand.

func TestTimeOnAirLoRa(t *testing.T) {
	eu868, err := New("EU868")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cases := []struct {
		name string
		dr   int
		pl   int
		want time.Duration
	}{
		{
			// SF12/125 kHz: low-data-rate optimization is on, so the denominator
			// is 4*(12-2)=40. num = 8*12 - 48 + 44 = 92; ceil(92/40)=3; 3*5=15;
			// payload symbols 23; total 35.25; Tsym 32.768 ms -> 1155.072 ms.
			name: "DR0 SF12 12 bytes", dr: 0, pl: 12, want: 1155072 * time.Microsecond,
		},
		{
			// SF7/125 kHz: denominator 28. num = 96 - 28 + 44 = 112; 112/28 = 4
			// exactly; 4*5 = 20; payload 28; total 40.25; Tsym 1.024 ms -> 41.216 ms.
			name: "DR5 SF7 12 bytes", dr: 5, pl: 12, want: 41216 * time.Microsecond,
		},
		{
			// An empty FRMPayload still costs the header, MIC and preamble: 8
			// mandatory payload symbols. num = -4, whose ceiling rounds to 0.
			name: "DR0 SF12 empty frame", dr: 0, pl: 0, want: 663552 * time.Microsecond,
		},
		{
			// The largest EU868 DR5 frame, which is the case that decides whether
			// a downlink fits the duty-cycle budget at all.
			name: "DR5 SF7 255 bytes", dr: 5, pl: 255, want: 399616 * time.Microsecond,
		},
	}
	for _, tc := range cases {
		dr, err := eu868.DataRate(tc.dr)
		if err != nil {
			t.Fatalf("DataRate(%d): %v", tc.dr, err)
		}
		got, err := TimeOnAir(dr, tc.pl)
		if err != nil {
			t.Fatalf("TimeOnAir(%s): %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s: ToA = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestTimeOnAirMonotonicWithSpreadingFactor guards the shape of the table: each
// step up in spreading factor roughly doubles the occupancy, and a bug in the
// symbol formula usually breaks monotonicity before it breaks an absolute value.
func TestTimeOnAirMonotonicWithSpreadingFactor(t *testing.T) {
	eu868, _ := New("EU868")
	previous := time.Duration(0)
	for _, drIndex := range []int{5, 4, 3, 2, 1, 0} { // SF7 down to SF12
		dr, err := eu868.DataRate(drIndex)
		if err != nil {
			t.Fatalf("DataRate(%d): %v", drIndex, err)
		}
		toa, err := TimeOnAir(dr, 20)
		if err != nil {
			t.Fatalf("TimeOnAir: %v", err)
		}
		if toa <= previous {
			t.Errorf("DR%d airtime %v does not exceed the previous %v", drIndex, toa, previous)
		}
		previous = toa
	}
}

// TestTimeOnAirGrowsWithPayload: airtime must never shrink as the frame grows.
func TestTimeOnAirGrowsWithPayload(t *testing.T) {
	eu868, _ := New("EU868")
	dr, _ := eu868.DataRate(0) // SF12, the slowest, where one byte is most visible
	previous := time.Duration(0)
	for pl := 0; pl <= 59; pl++ { // EU868 DR0 maximum frame
		toa, err := TimeOnAir(dr, pl)
		if err != nil {
			t.Fatalf("TimeOnAir(%d): %v", pl, err)
		}
		if toa < previous {
			t.Fatalf("%d-byte frame costs %v, less than the %d-byte frame's %v", pl, toa, pl-1, previous)
		}
		previous = toa
	}
	if first, _ := TimeOnAir(dr, 0); previous <= first {
		t.Errorf("a 59-byte frame costs the same as an empty one: %v", previous)
	}
}

// TestTimeOnAirRejectsLRFHSS: LR-FHSS frequency-hops across a block of channels,
// so a single-channel airtime cannot be derived from these tables. Returning a
// plausible-looking number would let a duty-cycle budget silently under-count.
func TestTimeOnAirRejectsLRFHSS(t *testing.T) {
	eu868, _ := New("EU868")
	dr, err := eu868.DataRate(8)
	if err != nil {
		t.Fatalf("DataRate(8): %v", err)
	}
	if _, err := TimeOnAir(dr, 20); err == nil {
		t.Error("LR-FHSS must be refused rather than guessed at")
	}
}

func TestTimeOnAirRejectsBadInput(t *testing.T) {
	if _, err := TimeOnAir(DataRate{Modulation: ModulationLoRa, SpreadingFactor: 13,
		BandwidthHz: 125_000, CodingRate: "4/5"}, 10); err == nil {
		t.Error("SF13 must be refused")
	}
	if _, err := TimeOnAir(DataRate{Modulation: ModulationLoRa, SpreadingFactor: 12,
		BandwidthHz: 0, CodingRate: "4/5"}, 10); err == nil {
		t.Error("zero bandwidth must be refused")
	}
	if _, err := TimeOnAir(DataRate{Modulation: ModulationLoRa, SpreadingFactor: 12,
		BandwidthHz: 125_000, CodingRate: "2/3"}, 10); err == nil {
		t.Error("a coding rate outside 4/5..4/8 must be refused")
	}
	if _, err := TimeOnAir(DataRate{Modulation: ModulationLoRa, SpreadingFactor: 12,
		BandwidthHz: 125_000, CodingRate: "4/5"}, -1); err == nil {
		t.Error("a negative payload length must be refused")
	}
}

func TestCodingRateDenominator(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"4/5", 5}, {"4/6", 6}, {"4/7", 7}, {"4/8", 8},
		{"4/5LI", 5}, {" 4/6li ", 6},
	} {
		got, err := codingRateDenominator(tc.in)
		if err != nil {
			t.Errorf("codingRateDenominator(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("codingRateDenominator(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestTimeOnAirMatchesSymbolArithmetic recomputes the formula from the
// modulation parameters rather than from pinned values, so a refactor that
// changes the arithmetic is caught even if every hand-pinned value were updated
// along with it.
func TestTimeOnAirMatchesSymbolArithmetic(t *testing.T) {
	eu868, _ := New("EU868")
	for _, drIndex := range []int{0, 1, 2, 3, 4, 5} {
		dr, err := eu868.DataRate(drIndex)
		if err != nil {
			t.Fatalf("DataRate(%d): %v", drIndex, err)
		}
		for _, pl := range []int{0, 1, 11, 51, 242} {
			got, err := TimeOnAir(dr, pl)
			if err != nil {
				t.Fatalf("TimeOnAir: %v", err)
			}
			sf := float64(dr.SpreadingFactor)
			de := 0.0
			if dr.BandwidthHz == 125_000 && dr.SpreadingFactor >= 11 {
				de = 1
			}
			symbolSeconds := math.Pow(2, sf) / float64(dr.BandwidthHz)
			symbols := 12.25 + 8 +
				math.Ceil((8*float64(pl)-4*sf+44)/(4*(sf-2*de)))*5
			want := time.Duration(math.Round(symbols * symbolSeconds * float64(time.Second)))
			// A microsecond of slack: the implementation rounds the duration it
			// returns, this recomputation rounds the nanoseconds.
			if diff := math.Abs(float64(got - want)); diff > float64(time.Microsecond) {
				t.Errorf("DR%d PL%d: ToA = %v, recomputed %v", drIndex, pl, got, want)
			}
		}
	}
}

func TestTimeOnAirFSK(t *testing.T) {
	eu868, _ := New("EU868")
	dr, err := eu868.DataRate(7) // FSK 50 kbit/s
	if err != nil {
		t.Fatalf("DataRate(7): %v", err)
	}
	// (12 + 5 preamble + 3 sync + 1 length + 2 CRC) bytes * 8 / 50000 = 3.68 ms.
	got, err := TimeOnAir(dr, 12)
	if err != nil {
		t.Fatalf("TimeOnAir FSK: %v", err)
	}
	if want := 3680 * time.Microsecond; got != want {
		t.Errorf("FSK ToA = %v, want %v", got, want)
	}
}
