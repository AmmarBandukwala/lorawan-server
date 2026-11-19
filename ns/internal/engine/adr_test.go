package engine

import (
	"testing"

	"github.com/fieldnode/lorawan-server/ns/internal/region"
)

func mustRegionTB(t *testing.T, name string) region.Region {
	t.Helper()
	reg, err := region.New(name)
	if err != nil {
		t.Fatalf("region.New(%s): %v", name, err)
	}
	return reg
}

// historyAt builds an ADR window at one power level, with consecutive counters
// starting at 1.
func historyAt(n int, snr float64, dataRate, txPower int) []UplinkHistoryItem {
	out := make([]UplinkHistoryItem, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, UplinkHistoryItem{
			FCnt: uint32(i + 1), SNR: snr, DataRate: dataRate, TxPowerIndex: txPower,
		})
	}
	return out
}

// TestRequiredSNRForSF pins the demodulation floors. These are the published
// receiver sensitivities for each spreading factor at a nominal noise figure;
// drift here silently makes every ADR decision in a deployment one step too eager
// or too timid.
func TestRequiredSNRForSF(t *testing.T) {
	for _, tc := range []struct {
		sf   int
		want float64
	}{
		{5, -2.5}, {6, -5.0}, {7, -7.5}, {8, -10.0},
		{9, -12.5}, {10, -15.0}, {11, -17.5}, {12, -20.0},
		// FSK and LR-FHSS have no spreading factor.
		{0, 0}, {13, 0},
	} {
		if got := requiredSNRForSF(tc.sf); got != tc.want {
			t.Errorf("requiredSNRForSF(%d) = %.1f, want %.1f", tc.sf, got, tc.want)
		}
	}
}

// TestMax125kHzUplinkDataRate covers the clamp that keeps ADR stepping in 3 dB
// increments: stepping into an FSK or 500 kHz rate would change the link by far
// more than the algorithm has accounted for.
func TestMax125kHzUplinkDataRate(t *testing.T) {
	for _, tc := range []struct {
		region string
		upper  int
		want   int
	}{
		// EU868 DR0..DR5 are SF12..SF7 at 125 kHz; DR6 is SF7BW258, DR7 FSK.
		{"EU868", 5, 5},
		{"EU868", 7, 5},
		// US915 DR4 is SF8BW500, so the 125 kHz ladder stops at DR3.
		{"US915", 4, 3},
		{"US915", 5, 3},
		{"AS923", 5, 5},
	} {
		reg := mustRegionTB(t, tc.region)
		if got := max125kHzUplinkDataRate(reg, tc.upper); got != tc.want {
			t.Errorf("%s: max125kHzUplinkDataRate(%d) = %d, want %d", tc.region, tc.upper, got, tc.want)
		}
	}
}

func TestADRDecideDisabledAndEmpty(t *testing.T) {
	eu868 := mustRegionTB(t, "EU868")

	off := adrDecide(adrInput{Region: eu868, Enabled: false, DataRate: 0, History: historyAt(20, 12, 0, 0)})
	if off.Changed {
		t.Errorf("a disabled device must not move: %+v", off)
	}

	// NbTrans is set to what a session actually runs: an unset value is
	// normalised to 1, and that normalisation is itself a change worth sending.
	empty := adrDecide(adrInput{
		Region: eu868, Enabled: true, DataRate: 0, TxPowerIndex: 0, NbTrans: 1,
		MaxTxPowerIndex: 5, MaxDataRate: 5,
		InstallationMargin: DefaultADRInstallationMargin,
	})
	if empty.Changed {
		t.Errorf("with no measurements nothing is known: %+v", empty)
	}
}

// TestADRDecideClimbsToTheCeilingThenTrimsPower is the main path: a device with
// plenty of headroom first raises its data rate, and once the band's ceiling is
// reached it spends the rest on transmit power, which is the interference
// friendly order.
func TestADRDecideClimbsToTheCeilingThenTrimsPower(t *testing.T) {
	eu868 := mustRegionTB(t, "EU868")
	// SF12 needs -20 dB. 12 dB measured with 10 dB kept as margin leaves 22 dB of
	// surplus, and 3 dB per step is 7 steps: five take it from DR0 to DR5, the
	// remaining two cut transmit power twice.
	got := adrDecide(adrInput{
		Region: eu868, Enabled: true,
		DataRate: 0, TxPowerIndex: 0, NbTrans: 1, MaxTxPowerIndex: 5, MaxDataRate: 5,
		InstallationMargin: DefaultADRInstallationMargin,
		History:            historyAt(20, 12.0, 0, 0),
	})
	if got.DataRate != 5 {
		t.Errorf("DataRate = %d, want the EU868 ceiling 5 (%s)", got.DataRate, got.Reason)
	}
	if got.TxPowerIndex != 2 {
		t.Errorf("TxPowerIndex = %d, want 2 (%s)", got.TxPowerIndex, got.Reason)
	}
	if !got.Changed {
		t.Error("Changed must be true")
	}
}

// TestADRDecideStaysAtBothCeilings: once data rate and power are both at their
// limits, more headroom must not produce a request. Without the break in
// idealSteps the algorithm would report a change that is not one, and the device
// would be asked to confirm its own settings forever.
func TestADRDecideStaysAtBothCeilings(t *testing.T) {
	eu868 := mustRegionTB(t, "EU868")
	maxTX := len(eu868.TXPowerOffsets()) - 1
	got := adrDecide(adrInput{
		Region: eu868, Enabled: true,
		DataRate: 5, TxPowerIndex: maxTX, NbTrans: 1,
		MaxTxPowerIndex: maxTX, MaxDataRate: 5,
		InstallationMargin: DefaultADRInstallationMargin,
		History:            historyAt(20, 15.0, 5, maxTX),
	})
	if got.Changed {
		t.Errorf("nothing is left to ask for: %+v", got)
	}
}

// TestADRDecideRaisesPowerOnlyOnAFullWindow is the anti-oscillation guard: a
// short window must not trigger a power increase, because the frames that follow
// would be measured at a power level the decision itself caused.
func TestADRDecideRaisesPowerOnlyOnAFullWindow(t *testing.T) {
	eu868 := mustRegionTB(t, "EU868")
	in := adrInput{
		Region: eu868, Enabled: true,
		DataRate: 5, TxPowerIndex: 2, NbTrans: 1, MaxTxPowerIndex: 5, MaxDataRate: 5,
		InstallationMargin: DefaultADRInstallationMargin,
		// SF7 needs -7.5 dB; -10 dB measured leaves a 12.5 dB deficit.
		History: historyAt(5, -10.0, 5, 2),
	}
	if got := adrDecide(in); got.Changed {
		t.Errorf("a 5-sample window must not change power: %+v", got)
	}

	in.History = historyAt(adrHistorySize, -10.0, 5, 2)
	got := adrDecide(in)
	if !got.Changed {
		t.Fatalf("a full window of weak signal must raise power: %+v", got)
	}
	// A lower index means more power, and the deficit is large enough to run the
	// index down to the top of the range.
	if got.TxPowerIndex != 0 {
		t.Errorf("TxPowerIndex = %d, want 0 (%s)", got.TxPowerIndex, got.Reason)
	}
	if got.DataRate != 5 {
		t.Errorf("DataRate = %d, want 5: lowering the data rate is the device's ADRACKRUN job, not the server's",
			got.DataRate)
	}
}

// TestADRDecideLowersADeviceAboveItsCeiling covers a band or profile change that
// leaves a session running a rate it is no longer allowed to use.
func TestADRDecideLowersADeviceAboveItsCeiling(t *testing.T) {
	eu868 := mustRegionTB(t, "EU868")
	got := adrDecide(adrInput{
		Region: eu868, Enabled: true,
		DataRate: 7, TxPowerIndex: 0, NbTrans: 1, MaxTxPowerIndex: 5, MaxDataRate: 7,
		InstallationMargin: DefaultADRInstallationMargin,
		History:            historyAt(adrHistorySize, 0.0, 7, 0),
	})
	if got.DataRate != 5 {
		t.Errorf("DataRate = %d, want the 125 kHz ceiling 5 (%s)", got.DataRate, got.Reason)
	}
}

func TestPacketLossPercentage(t *testing.T) {
	// A partial window has no meaningful denominator.
	if got := packetLossPercentage(historyAt(5, 5, 0, 0)); got != 0 {
		t.Errorf("partial window loss = %.1f%%, want 0", got)
	}

	// Every other counter missing: 19 gaps across 20 received frames.
	sparse := historyAt(adrHistorySize, 5, 0, 0)
	for i := range sparse {
		sparse[i].FCnt = uint32(2*i + 1)
	}
	if got := packetLossPercentage(sparse); got < 90 {
		t.Errorf("every-other-frame loss = %.1f%%, want above 90", got)
	}

	// One missing frame in twenty is 5 %.
	one := historyAt(adrHistorySize, 5, 0, 0)
	for i := 10; i < len(one); i++ {
		one[i].FCnt++
	}
	if got := packetLossPercentage(one); got < 4.9 || got > 5.1 {
		t.Errorf("single-gap loss = %.2f%%, want 5", got)
	}
}

// TestNBTransFor checks the retransmission table against the bands it defines.
func TestNBTransFor(t *testing.T) {
	for _, tc := range []struct {
		current int
		loss    float64
		want    int
	}{
		{1, 0, 1}, {1, 4.9, 1}, {2, 4.9, 1}, {3, 4.9, 2},
		{1, 9.9, 1}, {2, 9.9, 2}, {3, 9.9, 3},
		{1, 29.9, 2}, {2, 29.9, 3},
		{1, 30, 3}, {1, 95, 3}, {3, 95, 3},
		// Out-of-range values are clamped rather than trusted.
		{0, 0, 1}, {9, 0, 2},
	} {
		if got := nbTransFor(tc.current, tc.loss); got != tc.want {
			t.Errorf("nbTransFor(%d, %.1f%%) = %d, want %d", tc.current, tc.loss, got, tc.want)
		}
	}
}

// TestRecordUplinkHistoryBoundsTheWindow checks both halves of the ring: it must
// grow to the window size and then hold there, keeping the newest samples.
func TestRecordUplinkHistoryBoundsTheWindow(t *testing.T) {
	var ses Session
	ses.DataRate = 3
	for i := 0; i < 30; i++ {
		recordUplinkHistory(&ses, uint32(i+1), float64(i))
	}
	if len(ses.UplinkHistory) != adrHistorySize {
		t.Fatalf("history holds %d samples, want %d", len(ses.UplinkHistory), adrHistorySize)
	}
	if ses.UplinkHistory[0].FCnt != 11 || ses.UplinkHistory[adrHistorySize-1].FCnt != 30 {
		t.Errorf("window holds counters %d..%d, want 11..30",
			ses.UplinkHistory[0].FCnt, ses.UplinkHistory[adrHistorySize-1].FCnt)
	}
	if ses.UplinkHistory[0].DataRate != 3 {
		t.Error("a sample must record the data rate the uplink arrived on")
	}
}
