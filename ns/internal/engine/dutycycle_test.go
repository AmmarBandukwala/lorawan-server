package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/gateway"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
	"github.com/fieldnode/lorawan-server/ns/internal/region"
	"github.com/fieldnode/lorawan-server/ns/internal/sim"
)

// The numbers these tests pin are derived in region/toa_test.go and
// region/subband_test.go: a 12-byte frame at EU868 DR5 costs 41.216 ms, the 1%
// sub-band at 868.1 MHz buys 36 s per hour, and 36 s / 41.216 ms is 873 frames.

func newBudget(window time.Duration, clk *fakeClock) *airtimeBudget {
	return newAirtimeBudget(window, clk.now)
}

func mustEU868(t *testing.T) region.Region {
	t.Helper()
	r, err := region.New("EU868")
	if err != nil {
		t.Fatalf("region.New(EU868): %v", err)
	}
	return r
}

// exhaust books transmissions until the ledger refuses, and returns how many it
// accepted.
func exhaust(t *testing.T, b *airtimeBudget, gw lorawan.EUI64, reg region.Region,
	freqHz uint32, dr, frameLen int, at time.Time) int {
	t.Helper()
	accepted := 0
	for i := 0; i < 100000; i++ {
		perm := b.reserve(gw, reg, freqHz, dr, frameLen, at)
		if !perm.Allowed {
			return accepted
		}
		accepted++
	}
	t.Fatalf("the budget never filled up")
	return 0
}

func TestAirtimeBudgetBooksUntilTheWindowIsFull(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)}
	b := newBudget(DefaultDutyCycleWindow, clk)
	gw := mustEUI64(t, testGwEUI)

	// 36 s of hourly budget at 41.216 ms per frame is 873 frames; the 874th
	// would take the total past 36 s.
	got := exhaust(t, b, gw, mustEU868(t), eu868Ch0Hz, eu868DR5, 12, clk.now())
	if got != 873 {
		t.Errorf("the 1%% hourly budget allowed %d frames, want 873", got)
	}

	perm := b.reserve(gw, mustEU868(t), eu868Ch0Hz, eu868DR5, 12, clk.now())
	if perm.Allowed {
		t.Fatal("the budget must refuse once it is full")
	}
	if perm.RetryAfter <= 0 {
		t.Errorf("RetryAfter = %v, want a positive wait", perm.RetryAfter)
	}
	// Two, not one: filling the budget ends with a probe that is refused, and
	// this check is the second.
	if counts := b.denialCounts(); counts["duty_cycle"] != 2 {
		t.Errorf("denial counts = %v, want duty_cycle=2", counts)
	}
}

// TestAirtimeBudgetKeepsRX2OpenWhenRX1RunsOut is the behaviour that matters most
// in the field: the 869.4-869.65 MHz range carries ten times the duty cycle of
// the 868 MHz channels, so a busy deployment loses RX1 first and stays reachable
// through RX2 rather than going dark entirely.
func TestAirtimeBudgetKeepsRX2OpenWhenRX1RunsOut(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)}
	b := newBudget(DefaultDutyCycleWindow, clk)
	gw := mustEUI64(t, testGwEUI)
	reg := mustEU868(t)

	exhaust(t, b, gw, reg, eu868Ch0Hz, eu868DR5, 12, clk.now())

	if perm := b.reserve(gw, reg, eu868Ch0Hz, eu868DR5, 12, clk.now()); perm.Allowed {
		t.Error("RX1 should be out of budget")
	}
	perm := b.reserve(gw, reg, 869_525_000, 0, 12, clk.now())
	if !perm.Allowed {
		t.Errorf("RX2 must stay available: %s", perm.Reason)
	}
}

// TestAirtimeBudgetDwellTimeCapsOneFrame: AS923 limits a single transmission to
// 400 ms regardless of how quiet the channel has been, so an SF12 frame — which
// costs well over a second — can never go out there.
func TestAirtimeBudgetDwellTimeCapsOneFrame(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)}
	b := newBudget(DefaultDutyCycleWindow, clk)
	gw := mustEUI64(t, testGwEUI)
	as923, err := region.New("AS923")
	if err != nil {
		t.Fatalf("region.New(AS923): %v", err)
	}

	// SF12/125 kHz with 12 bytes costs 1155 ms, nearly three times the dwell cap.
	perm := b.reserve(gw, as923, 923_200_000, 0, 12, clk.now())
	if perm.Allowed {
		t.Error("a frame longer than the dwell time must be refused")
	}
	if counts := b.denialCounts(); counts["dwell_time"] != 1 {
		t.Errorf("denial counts = %v, want dwell_time=1", counts)
	}

	// SF7/125 kHz with 12 bytes costs 41 ms, comfortably inside it.
	if perm := b.reserve(gw, as923, 923_200_000, 5, 12, clk.now()); !perm.Allowed {
		t.Errorf("a short frame must be allowed on AS923: %s", perm.Reason)
	}
}

// TestAirtimeBudgetRefusesAFrameThatCannotEverFit distinguishes "wait" from
// "never": a frame whose airtime exceeds the whole window budget will not be
// helped by waiting, and the operator needs to be told that.
func TestAirtimeBudgetRefusesAFrameThatCannotEverFit(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)}
	// A one-minute window makes the 0.1% sub-band at 433 MHz worth 60 ms, less
	// than a single SF12 frame.
	b := newBudget(time.Minute, clk)
	gw := mustEUI64(t, testGwEUI)

	perm := b.reserve(gw, mustEU868(t), 433_500_000, 0, 12, clk.now())
	if perm.Allowed {
		t.Fatal("a frame longer than the entire budget must be refused")
	}
	if perm.RetryAfter != 0 {
		t.Errorf("RetryAfter = %v, want 0: waiting cannot help", perm.RetryAfter)
	}
	if counts := b.denialCounts(); counts["frame_exceeds_budget"] != 1 {
		t.Errorf("denial counts = %v, want frame_exceeds_budget=1", counts)
	}
}

// TestAirtimeBudgetRefusesAnUndefinedFrequency: an unknown range means the
// channel plan is wrong, and the safe answer is no.
func TestAirtimeBudgetRefusesAnUndefinedFrequency(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)}
	b := newBudget(DefaultDutyCycleWindow, clk)

	perm := b.reserve(mustEUI64(t, testGwEUI), mustEU868(t), 868_650_000, eu868DR5, 12, clk.now())
	if perm.Allowed {
		t.Error("a frequency in a gap between sub-bands must be refused")
	}
	if counts := b.denialCounts(); counts["outside_sub_band"] != 1 {
		t.Errorf("denial counts = %v, want outside_sub_band=1", counts)
	}
}

// TestAirtimeBudgetFreesAfterTheWindow: the ledger is a rolling window, so
// occupancy expires and the gateway recovers without intervention.
func TestAirtimeBudgetFreesAfterTheWindow(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)}
	b := newBudget(time.Minute, clk) // 1% of a minute is 600 ms
	gw := mustEUI64(t, testGwEUI)
	reg := mustEU868(t)

	if filled := exhaust(t, b, gw, reg, eu868Ch0Hz, eu868DR5, 12, clk.now()); filled == 0 {
		t.Fatal("a one-minute 1% window should hold some frames")
	}
	if perm := b.reserve(gw, reg, eu868Ch0Hz, eu868DR5, 12, clk.now()); perm.Allowed {
		t.Fatal("the window should be full")
	}

	clk.advance(time.Minute + time.Millisecond)
	if perm := b.reserve(gw, reg, eu868Ch0Hz, eu868DR5, 12, clk.now()); !perm.Allowed {
		t.Errorf("the budget must free up once the window passes: %s", perm.Reason)
	}
}

// TestAirtimeBudgetIsPerGateway: two gateways on the same channel have separate
// radios and separate legal budgets. Sharing one ledger would halve the capacity
// of a multi-gateway deployment for no regulatory reason.
func TestAirtimeBudgetIsPerGateway(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)}
	b := newBudget(time.Minute, clk)
	reg := mustEU868(t)
	gwA := mustEUI64(t, "aaaaaaaaaaaaaaaa")
	gwB := mustEUI64(t, "bbbbbbbbbbbbbbbb")

	exhaust(t, b, gwA, reg, eu868Ch0Hz, eu868DR5, 12, clk.now())

	if perm := b.reserve(gwA, reg, eu868Ch0Hz, eu868DR5, 12, clk.now()); perm.Allowed {
		t.Error("gateway A should be out of budget")
	}
	if perm := b.reserve(gwB, reg, eu868Ch0Hz, eu868DR5, 12, clk.now()); !perm.Allowed {
		t.Errorf("gateway B has its own budget: %s", perm.Reason)
	}
}

// TestAirtimeBudgetNoLimitNeedsNoAccounting: US915 has no duty cycle, and the
// ledger must stay empty rather than filling with entries that enforce nothing.
func TestAirtimeBudgetNoLimitNeedsNoAccounting(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)}
	b := newBudget(DefaultDutyCycleWindow, clk)
	us915, err := region.New("US915")
	if err != nil {
		t.Fatalf("region.New(US915): %v", err)
	}
	gw := mustEUI64(t, testGwEUI)

	for i := 0; i < 500; i++ {
		perm := b.reserve(gw, us915, 923_300_000, 8, 51, clk.now())
		if !perm.Allowed {
			t.Fatalf("transmission %d refused in a band with no duty cycle: %s", i, perm.Reason)
		}
	}
	if usage := b.usage(); len(usage) != 0 {
		t.Errorf("an unlimited band should book nothing, got %d rows", len(usage))
	}
}

func TestAirtimeBudgetUsageReport(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)}
	b := newBudget(DefaultDutyCycleWindow, clk)
	gw := mustEUI64(t, testGwEUI)
	reg := mustEU868(t)

	for i := 0; i < 10; i++ {
		if perm := b.reserve(gw, reg, eu868Ch0Hz, eu868DR5, 12, clk.now()); !perm.Allowed {
			t.Fatalf("reserve %d: %s", i, perm.Reason)
		}
	}
	usage := b.usage()
	if len(usage) != 1 {
		t.Fatalf("usage report = %+v, want one row", usage)
	}
	row := usage[0]
	// EUI64.String renders uppercase while the fixture constant is lowercase;
	// comparing case-insensitively keeps both readable.
	if !strings.EqualFold(row.Gateway, testGwEUI) || row.Region != "EU868" {
		t.Errorf("row = %+v", row)
	}
	// 10 frames at 41.216 ms is 412.16 ms of the 36 s hourly budget.
	if row.Used < 412*time.Millisecond || row.Used > 413*time.Millisecond {
		t.Errorf("used = %v, want about 412ms", row.Used)
	}
	if row.Budget != 36*time.Second {
		t.Errorf("budget = %v, want 36s", row.Budget)
	}
	if row.Transmits != 10 {
		t.Errorf("transmits = %d, want 10", row.Transmits)
	}
}

// --- through the engine ------------------------------------------------------

// exhaustSubBand fills one sub-band's ledger so a following uplink meets a
// closed budget.
func (env *testEnv) exhaustSubBand(freqHz uint32, dr int) {
	env.t.Helper()
	gw := mustEUI64(env.t, testGwEUI)
	exhaust(env.t, env.eng.budget, gw, mustEU868(env.t), freqHz, dr, 12, env.clk.now())
}

// TestEngineUsesRX2WhenRX1HasNoBudget is the end-to-end version of the sub-band
// behaviour: the device stays reachable, on the frequency that has room.
func TestEngineUsesRX2WhenRX1HasNoBudget(t *testing.T) {
	env := newTestEnv(t, nil)
	ctx := context.Background()

	simDev, _ := env.join()
	env.gw.reset()

	env.exhaustSubBand(eu868Ch0Hz, eu868DR5)
	if err := env.eng.EnqueueDownlink(ctx, simDev.DevEUI, 10, []byte("still reachable"), false); err != nil {
		t.Fatalf("EnqueueDownlink: %v", err)
	}
	env.uplink(simDev, sim.UplinkOptions{FPort: 10, Payload: []byte("3")}, gateway.RxWindow1)

	frames := env.gw.frames()
	if len(frames) != 1 {
		t.Fatalf("scheduled %d downlinks, want 1 (RX2 only)", len(frames))
	}
	if frames[0].Window != gateway.RxWindow2 {
		t.Errorf("window = %s, want RX2", frames[0].Window)
	}
	if frames[0].FrequencyHz != 869_525_000 {
		t.Errorf("frequency = %d, want the RX2 channel", frames[0].FrequencyHz)
	}

	// The event has to say which window and which limit, or an operator cannot
	// tell a regulatory refusal from a radio fault.
	denied := env.store.EventsWithReason(ReasonDutyCycleDenied)
	if len(denied) != 1 {
		t.Fatalf("duty-cycle events = %d, want 1 recording the refused RX1", len(denied))
	}
	if denied[0].Detail == "" {
		t.Error("the denial event carries no detail")
	}
}

// TestEngineStaysSilentWhenBothWindowsAreFull: nothing may be transmitted, and
// the reason must be recorded rather than left as a missing downlink.
func TestEngineStaysSilentWhenBothWindowsAreFull(t *testing.T) {
	env := newTestEnv(t, nil)
	ctx := context.Background()

	simDev, _ := env.join()
	env.gw.reset()

	env.exhaustSubBand(eu868Ch0Hz, eu868DR5)
	env.exhaustSubBand(869_525_000, 0)

	if err := env.eng.EnqueueDownlink(ctx, simDev.DevEUI, 10, []byte("no room"), false); err != nil {
		t.Fatalf("EnqueueDownlink: %v", err)
	}
	env.uplink(simDev, sim.UplinkOptions{FPort: 10, Payload: []byte("4")}, gateway.RxWindow1)

	if frames := env.gw.frames(); len(frames) != 0 {
		t.Errorf("scheduled %d downlinks, want none", len(frames))
	}
	if denied := env.store.EventsWithReason(ReasonDutyCycleDenied); len(denied) != 2 {
		t.Errorf("duty-cycle events = %d, want one per window", len(denied))
	}
	if usage := env.eng.DutyCycleUsage(); len(usage) != 2 {
		t.Errorf("usage rows = %d, want both sub-bands reported", len(usage))
	}
}

// TestEngineDutyCycleEnforcementCanBeDisabled covers the escape hatch, and that
// turning it off leaves everything else alone.
func TestEngineDutyCycleEnforcementCanBeDisabled(t *testing.T) {
	env := newTestEnv(t, func(cfg *Config) { cfg.DisableDutyCycleEnforcement = true })
	ctx := context.Background()

	simDev, _ := env.join()
	env.gw.reset()

	env.exhaustSubBand(eu868Ch0Hz, eu868DR5)
	env.exhaustSubBand(869_525_000, 0)

	if err := env.eng.EnqueueDownlink(ctx, simDev.DevEUI, 10, []byte("enforcement off"), false); err != nil {
		t.Fatalf("EnqueueDownlink: %v", err)
	}
	env.uplink(simDev, sim.UplinkOptions{FPort: 10, Payload: []byte("5")}, gateway.RxWindow1)

	if frames := env.gw.frames(); len(frames) != 2 {
		t.Errorf("scheduled %d downlinks, want both windows", len(frames))
	}
	if denied := env.store.EventsWithReason(ReasonDutyCycleDenied); len(denied) != 0 {
		t.Errorf("denial events = %d, want none when enforcement is off", len(denied))
	}
}
