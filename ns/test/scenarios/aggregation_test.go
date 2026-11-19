package scenarios_test

import (
	"testing"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/sim"
	"github.com/fieldnode/lorawan-server/ns/test/testenv"
)

// Second gateway, so a test can say "heard by two" without inventing an EUI
// inline in the middle of an assertion.
const secondGatewayEUI = "BB55555555555555"

// TestTwoGatewaysHearOneFrameGetOneAnswerThroughTheBetterOne is the point of
// aggregation.
//
// Before it, the second copy was discarded and the downlink left through
// whichever gateway's packet reached the server first — a routing accident, since
// backhaul latency says nothing about radio quality. Here the weaker gateway
// arrives first on purpose, so the assertion cannot pass by accident.
func TestTwoGatewaysHearOneFrameGetOneAnswerThroughTheBetterOne(t *testing.T) {
	env, sched := testenv.NewDeferred(t, nil)
	simDev, _ := env.Join()
	env.GW.Reset()
	// The engine answers an uplink only when it has something to send, so the
	// frame under test has to be worth answering.
	env.Enqueue([]byte("ack-with-data"), false)

	wire, err := simDev.NextUplink(sim.UplinkOptions{FPort: 10, Payload: []byte("temp=21")})
	if err != nil {
		t.Fatalf("NextUplink: %v", err)
	}

	near := testenv.MustEUI64(t, testenv.GatewayEUI)
	far := testenv.MustEUI64(t, secondGatewayEUI)

	// The weak copy arrives first; the strong copy lands inside the window.
	if err := env.Feed(testenv.CopyOf(wire, near, 3.0, -95, 0x11)); err != nil {
		t.Fatalf("Feed(near): %v", err)
	}
	if err := env.Feed(testenv.CopyOf(wire, far, 12.0, -60, 0x22)); err != nil {
		t.Fatalf("Feed(far): %v", err)
	}

	// Nothing has been answered yet: the window is still open, which is the whole
	// reason the second copy had anywhere to join.
	if got := sched.Pending(); got != 1 {
		t.Fatalf("pending work = %d, want 1 (the open window owns the answer)", got)
	}
	if frames := env.GW.Frames(); len(frames) != 0 {
		t.Fatalf("answered %d frames before the window closed", len(frames))
	}

	env.Settle()

	frames := env.GW.Frames()
	if len(frames) == 0 {
		t.Fatalf("the frame was never answered; events: %+v", env.Store.Events())
	}
	for _, f := range frames {
		if f.GatewayID != far {
			t.Errorf("downlink left through %s, want the stronger gateway %s",
				f.GatewayID, far)
		}
	}
}

// TestAggregationRecordsEveryGatewayThatHeardIt: coverage is the other half of
// the value. A deployment cannot plan a network from a table that keeps only the
// winner.
func TestAggregationRecordsEveryGatewayThatHeardIt(t *testing.T) {
	// No scheduler handle needed: Settle drains the aggregation window and the
	// downlinks it queues, and this test only inspects what was stored.
	env, _ := testenv.NewDeferred(t, nil)
	simDev, _ := env.Join()

	wire, err := simDev.NextUplink(sim.UplinkOptions{FPort: 10, Payload: []byte("hello")})
	if err != nil {
		t.Fatalf("NextUplink: %v", err)
	}

	near := testenv.MustEUI64(t, testenv.GatewayEUI)
	far := testenv.MustEUI64(t, secondGatewayEUI)
	if err := env.Feed(testenv.CopyOf(wire, near, 3.0, -95, 0x11)); err != nil {
		t.Fatalf("Feed(near): %v", err)
	}
	if err := env.Feed(testenv.CopyOf(wire, far, 12.0, -60, 0x22)); err != nil {
		t.Fatalf("Feed(far): %v", err)
	}
	env.Settle()

	uplinks := env.Store.Uplinks()
	if len(uplinks) != 1 {
		t.Fatalf("stored %d uplinks, want 1: two copies are one frame", len(uplinks))
	}
	rec := uplinks[0]
	if len(rec.Receptions) != 2 {
		t.Fatalf("recorded %d receptions, want 2: %+v", len(rec.Receptions), rec.Receptions)
	}
	// The chosen reception leads, so a reader can take index zero and get the
	// gateway the answer went back through.
	if rec.Receptions[0].GatewayID != far {
		t.Errorf("first reception is %s, want the chosen gateway %s",
			rec.Receptions[0].GatewayID, far)
	}
	if rec.Receptions[0].SNR != 12.0 || rec.Receptions[1].SNR != 3.0 {
		t.Errorf("reception signal = %v and %v, want 12 and 3",
			rec.Receptions[0].SNR, rec.Receptions[1].SNR)
	}
	if rec.SNR != 12.0 {
		t.Errorf("record SNR = %v, want the best of the copies (12)", rec.SNR)
	}
}

// TestCopyArrivingAfterTheWindowIsNotAnsweredTwice guards the other direction:
// aggregation must not turn a duplicate into a second answer.
func TestCopyArrivingAfterTheWindowIsNotAnsweredTwice(t *testing.T) {
	// Settle closes the aggregation window, so the second copy genuinely arrives
	// after it. The scheduler handle is not needed to make that happen.
	env, _ := testenv.NewDeferred(t, nil)
	simDev, _ := env.Join()
	env.GW.Reset()
	env.Enqueue([]byte("answer once"), false)

	wire, err := simDev.NextUplink(sim.UplinkOptions{FPort: 10, Payload: []byte("late")})
	if err != nil {
		t.Fatalf("NextUplink: %v", err)
	}

	near := testenv.MustEUI64(t, testenv.GatewayEUI)
	far := testenv.MustEUI64(t, secondGatewayEUI)
	if err := env.Feed(testenv.CopyOf(wire, near, 3.0, -95, 0x11)); err != nil {
		t.Fatalf("Feed(near): %v", err)
	}
	env.Settle()

	answered := len(env.GW.Frames())
	if answered == 0 {
		t.Fatal("the first copy was never answered")
	}

	// The window has closed by the time this copy turns up: a gateway behind a
	// slow backhaul, which is ordinary on cellular.
	if err := env.Feed(testenv.CopyOf(wire, far, 12.0, -60, 0x22)); err != nil {
		t.Fatalf("Feed(late copy): %v", err)
	}
	env.Settle()

	if frames := env.GW.Frames(); len(frames) != answered {
		t.Errorf("a late copy produced %d downlinks where %d had already gone out",
			len(frames), answered)
	}
	drops := env.Store.EventsOfKind("uplink_drop")
	if len(drops) == 0 {
		t.Error("the late copy was discarded without saying so")
	} else if drops[0].Reason != "duplicate_frame" {
		t.Errorf("drop reason = %q, want duplicate_frame", drops[0].Reason)
	}
}

// TestAggregationDisabledRestoresFirstCopyWins pins the escape hatch, so turning
// the feature off is a documented behaviour rather than an accident of ordering.
func TestAggregationDisabledRestoresFirstCopyWins(t *testing.T) {
	env, sched := testenv.NewDeferred(t, func(cfg *engine.Config) { cfg.AggregationDelay = 0 })
	simDev, _ := env.Join()
	env.GW.Reset()
	env.Enqueue([]byte("no window"), false)

	wire, err := simDev.NextUplink(sim.UplinkOptions{FPort: 10, Payload: []byte("off")})
	if err != nil {
		t.Fatalf("NextUplink: %v", err)
	}

	near := testenv.MustEUI64(t, testenv.GatewayEUI)
	far := testenv.MustEUI64(t, secondGatewayEUI)

	// With no window there is nothing to wait for, so the frame is processed
	// during Feed rather than being held. The proof is in what is queued: the two
	// pending items are the RX1 and RX2 transmissions at their full delays, not a
	// collection window. With aggregation on, the same feed would leave a single
	// 200 ms item pending instead.
	if err := env.Feed(testenv.CopyOf(wire, near, 3.0, -95, 0x11)); err != nil {
		t.Fatalf("Feed(near): %v", err)
	}
	delays := sched.Delays()
	if len(delays) != 2 {
		t.Fatalf("pending work = %v, want the RX1 and RX2 transmissions scheduled on arrival", delays)
	}
	for _, d := range delays {
		if d == 0 || d == engine.DefaultAggregationDelay {
			t.Errorf("a %v item is still pending; the frame should have been processed on arrival, not held", d)
		}
	}
	env.Settle()

	frames := env.GW.Frames()
	if len(frames) == 0 {
		t.Fatal("the first copy was not answered immediately")
	}
	for _, f := range frames {
		if f.GatewayID != near {
			t.Errorf("downlink left through %s, want the first gateway %s", f.GatewayID, near)
		}
	}

	// The stronger copy still must not produce a second answer.
	if err := env.Feed(testenv.CopyOf(wire, far, 12.0, -60, 0x22)); err != nil {
		t.Fatalf("Feed(far): %v", err)
	}
	env.Settle()
	if got := len(env.GW.Frames()); got != len(frames) {
		t.Errorf("the second copy produced %d downlinks, want the original %d", got, len(frames))
	}
}

// TestDownlinkIsDispatchedWithTheTimeLeftInTheWindow checks the cost of the
// window rather than its benefit.
//
// The RX1 delay is measured from the uplink, so every millisecond spent waiting
// for copies is a millisecond less available to dispatch in. Scheduling the full
// delay from the moment of dispatch would push every downlink later by the window
// and, once the window is a large fraction of RX1, past the device's listening
// period altogether.
func TestDownlinkIsDispatchedWithTheTimeLeftInTheWindow(t *testing.T) {
	const window = 200 * time.Millisecond

	env, sched := testenv.NewDeferred(t, func(cfg *engine.Config) { cfg.AggregationDelay = window })
	simDev, _ := env.Join()
	env.GW.Reset()
	// The join scheduled its own RX1/RX2 work; forgetting it is what makes the
	// delays below belong to this uplink and nothing else.
	sched.Reset()
	env.Enqueue([]byte("timed"), false)

	wire, err := simDev.NextUplink(sim.UplinkOptions{FPort: 10, Payload: []byte("timing")})
	if err != nil {
		t.Fatalf("NextUplink: %v", err)
	}
	if err := env.Feed(testenv.CopyOf(wire,
		testenv.MustEUI64(t, testenv.GatewayEUI), 8.0, -70, 0x11)); err != nil {
		t.Fatalf("Feed: %v", err)
	}

	// The window elapses. The fake clock only moves when a test says so, which is
	// what makes this assertion about elapsed time rather than about ordering.
	env.Clock.Advance(window)
	env.Settle() // closes the window, processes the frame, schedules the downlinks

	delays := sched.ExecutedDelays()
	// The first entry is the window itself; the ones after it are the downlinks.
	if len(delays) < 2 {
		t.Fatalf("scheduled %d items, want the window plus at least one downlink: %v",
			len(delays), delays)
	}
	if delays[0] != window {
		t.Errorf("the window was scheduled with %v, want %v", delays[0], window)
	}

	rx1, rx2 := time.Second-window, 2*time.Second-window
	foundRX1, foundRX2 := false, false
	for _, d := range delays[1:] {
		switch d {
		case rx1:
			foundRX1 = true
		case rx2:
			foundRX2 = true
		default:
			t.Errorf("downlink scheduled %v after dispatch; want %v (RX1) or %v (RX2)",
				d, rx1, rx2)
		}
	}
	if !foundRX1 {
		t.Errorf("no RX1 downlink at %v; delays were %v", rx1, delays[1:])
	}
	if !foundRX2 {
		t.Errorf("no RX2 downlink at %v; delays were %v", rx2, delays[1:])
	}
}
