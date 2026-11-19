package engine_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/testenv"

	"github.com/fieldnode/lorawan-server/ns/internal/gateway"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
	"github.com/fieldnode/lorawan-server/ns/internal/sim"
)

func TestDuplicateFrameFromSecondGatewayIsDropped(t *testing.T) {
	env := testenv.New(t, nil)

	simDev, _ := env.Join()
	env.GW.Reset()

	wire, err := simDev.NextUplink(sim.UplinkOptions{FPort: 1, Payload: []byte("same frame")})
	if err != nil {
		t.Fatalf("NextUplink: %v", err)
	}

	first := testenv.UplinkFrame(wire, gateway.RxWindow1, testenv.EU868Ch0Hz, testenv.EU868DR5)
	if err := env.Engine.HandleUplink(context.Background(), first); err != nil {
		t.Fatalf("HandleUplink: %v", err)
	}

	// A second gateway hears the same transmission a moment later.
	second := first
	second.GatewayID = testenv.MustEUI64(t, "bb55555555555555")
	second.RSSI = -91
	if err := env.Engine.HandleUplink(context.Background(), second); err != nil {
		t.Fatalf("HandleUplink: %v", err)
	}

	if got := len(env.Store.Uplinks()); got != 1 {
		t.Errorf("recorded uplinks = %d, want 1 (the duplicate must be dropped)", got)
	}
	if len(env.Store.EventsWithReason(engine.ReasonDuplicateFrame)) != 1 {
		t.Errorf("want one %s event, got %+v", engine.ReasonDuplicateFrame, env.Store.Events())
	}
}

func TestDevNonceReplayAfterDedupWindow(t *testing.T) {
	env := testenv.New(t, nil)

	env.Provision(true)
	simDev := sim.New(testenv.MustEUI64(t, testenv.DevEUI), testenv.MustEUI64(t, testenv.JoinEUI),
		testenv.MustKey(t, testenv.AppKey), env.Engine.Config().NetID)
	wire, err := simDev.NextJoinRequest()
	if err != nil {
		t.Fatalf("NextJoinRequest: %v", err)
	}
	if err := env.UplinkWire(wire); err != nil {
		t.Fatalf("HandleUplink: %v", err)
	}
	if len(env.Store.EventsOfKind(engine.EventJoinAccept)) != 1 {
		t.Fatalf("the first join should succeed; events: %+v", env.Store.Events())
	}

	// An attacker captures this join-request and replays it much later, after
	// the de-duplication window has expired. The DevNonce rule must still stop
	// it.
	env.GW.Reset()
	env.Clock.Advance(2 * env.Engine.Config().DedupWindow)
	if err := env.UplinkWire(wire); err != nil {
		t.Fatalf("HandleUplink(replay): %v", err)
	}

	if frames := env.GW.Frames(); len(frames) != 0 {
		t.Errorf("a replayed join-request produced %d join-accept(s), want 0", len(frames))
	}
	if len(env.Store.EventsWithReason(engine.ReasonDevNonceReplay)) != 1 {
		t.Errorf("want one %s event, got %+v", engine.ReasonDevNonceReplay, env.Store.Events())
	}
}

func TestFrameCounterGapTooLarge(t *testing.T) {
	env := testenv.New(t, nil)

	simDev, _ := env.Join()
	env.GW.Reset()

	// A device whose counter jumped far ahead of the expected value.
	fcnt := uint32(500)
	wire, err := simDev.NextUplink(sim.UplinkOptions{FPort: 1, Payload: []byte("jump"), ForceFCnt: &fcnt})
	if err != nil {
		t.Fatalf("NextUplink: %v", err)
	}
	if err := env.UplinkWire(wire); err != nil {
		t.Fatalf("HandleUplink: %v", err)
	}

	if len(env.Store.Uplinks()) != 0 {
		t.Error("a frame outside the replay window must not be recorded")
	}
	if len(env.Store.EventsWithReason(engine.ReasonFCntGapTooLarge)) != 1 {
		t.Errorf("want one %s event, got %+v", engine.ReasonFCntGapTooLarge, env.Store.Events())
	}
	if ses := env.Session(testenv.MustEUI64(t, testenv.DevEUI)); ses.FCntUp != 0 {
		t.Errorf("a rejected frame advanced FCntUp to %d, want 0", ses.FCntUp)
	}
}

func TestRetransmissionIsAcceptedWithoutAdvancingCounter(t *testing.T) {
	env := testenv.New(t, nil)
	devEUI := testenv.MustEUI64(t, testenv.DevEUI)

	simDev, _ := env.Join()

	wire, err := simDev.NextUplink(sim.UplinkOptions{FPort: 1, Payload: []byte("first")})
	if err != nil {
		t.Fatalf("NextUplink: %v", err)
	}
	if err := env.UplinkWire(wire); err != nil {
		t.Fatalf("HandleUplink: %v", err)
	}
	if ses := env.Session(devEUI); ses.FCntUp != 1 {
		t.Fatalf("FCntUp = %d after the first uplink, want 1", ses.FCntUp)
	}

	// The device never heard our answer and repeats the same counter. Outside
	// the dedup window this reaches the counter logic as a retransmission.
	env.Clock.Advance(2 * env.Engine.Config().DedupWindow)
	if err := env.UplinkWire(wire); err != nil {
		t.Fatalf("HandleUplink(retransmission): %v", err)
	}

	uplinks := env.Store.Uplinks()
	if len(uplinks) != 2 {
		t.Fatalf("recorded uplinks = %d, want 2", len(uplinks))
	}
	if !uplinks[1].Retransmission {
		t.Error("the repeat was not flagged as a retransmission")
	}
	if uplinks[1].FCntUp != uplinks[0].FCntUp {
		t.Errorf("retransmission counter %d != original %d", uplinks[1].FCntUp, uplinks[0].FCntUp)
	}
	if ses := env.Session(devEUI); ses.FCntUp != 1 {
		t.Errorf("a retransmission advanced FCntUp to %d, want it to stay 1", ses.FCntUp)
	}
}

// TestUnacknowledgedDownlinkIsRetransmittedWithSameCounter pins the counter rule
// for a confirmed frame that has to be sent again: the retransmission must reuse
// FCntDown, because a new counter would make the first transmission unrecoverable
// for the device (it would discard the retry as a replay and accept the original
// if it ever arrived).
//
// This used to run with an unconfirmed frame and assert that it was retried. That
// was wrong: nothing acknowledges an unconfirmed downlink, so "unacknowledged"
// was permanently true and the frame would have been re-sent on every uplink for
// the life of the session. The interoperable behaviour is to mark a transmitted
// queue item pending and delete it once its timeout passes rather than to
// retransmit it: nothing acknowledges an unconfirmed downlink, so "unacknowledged"
// would stay permanently true. The unconfirmed case is covered by
// TestUnconfirmedDownlinkIsTransmittedOnce.
func TestUnacknowledgedDownlinkIsRetransmittedWithSameCounter(t *testing.T) {
	env := testenv.New(t, nil)
	ctx := context.Background()
	devEUI := testenv.MustEUI64(t, testenv.DevEUI)

	simDev, _ := env.Join()
	if err := env.Engine.EnqueueDownlink(ctx, devEUI, 10, []byte("hold this"), true); err != nil {
		t.Fatalf("EnqueueDownlink: %v", err)
	}

	// The join-accept is recorded as a downlink too, so measure from here.
	sent := len(env.Store.Downlinks())

	env.Uplink(simDev, sim.UplinkOptions{FPort: 10, Payload: []byte("1")}, gateway.RxWindow1)
	all := env.Store.Downlinks()
	if len(all) <= sent {
		t.Fatalf("no downlink scheduled; records: %+v", all)
	}
	if first := all[sent]; first.FCntDown != 1 {
		t.Fatalf("first data downlink FCntDown = %d, want 1", first.FCntDown)
	}

	// The device did not acknowledge, so the same frame goes out again with the
	// same counter.
	env.Uplink(simDev, sim.UplinkOptions{FPort: 10, Payload: []byte("2")}, gateway.RxWindow1)

	after := env.Store.Downlinks()
	if len(after) <= len(all) {
		t.Fatalf("the unacknowledged downlink was not retried; records: %+v", after)
	}
	if last := after[len(after)-1]; last.FCntDown != 1 {
		t.Errorf("retransmission FCntDown = %d, want 1 (unchanged)", last.FCntDown)
	}
	if ses := env.Session(devEUI); ses.Pending == nil {
		t.Error("the pending downlink disappeared before it was acknowledged")
	}
}

func TestCommandAPIErrors(t *testing.T) {
	env := testenv.New(t, nil)
	ctx := context.Background()
	devEUI := testenv.MustEUI64(t, testenv.DevEUI)

	// No session yet.
	if err := env.Engine.EnqueueDownlink(ctx, devEUI, 10, []byte("x"), false); err == nil ||
		!strings.Contains(err.Error(), "must join") {
		t.Errorf("EnqueueDownlink before join = %v, want a 'must join' error", err)
	}

	simDev, _ := env.Join()

	if err := env.Engine.EnqueueDownlink(ctx, devEUI, 10, []byte("first"), false); err != nil {
		t.Fatalf("EnqueueDownlink: %v", err)
	}
	// A second request must not silently replace the first.
	if err := env.Engine.EnqueueDownlink(ctx, devEUI, 10, []byte("second"), false); !errors.Is(err, engine.ErrPendingDownlinkExists) {
		t.Errorf("second EnqueueDownlink = %v, want engine.ErrPendingDownlinkExists", err)
	}

	// FPort 0 is reserved for MAC commands.
	if err := env.Engine.CancelPendingDownlink(ctx, devEUI); err != nil {
		t.Fatalf("CancelPendingDownlink: %v", err)
	}
	if err := env.Engine.EnqueueDownlink(ctx, devEUI, 0, []byte("x"), false); err == nil {
		t.Error("FPort 0 must be rejected by EnqueueDownlink")
	}

	// Data-rate 20 does not exist in EU868.
	if err := env.Engine.RequestLinkADR(ctx, devEUI, 20, 0, 1, 0x00FF, 0); err == nil {
		t.Error("RequestLinkADR accepted a non-existent data-rate")
	}
	// TX power index 20 is outside the EU868 table (8 entries).
	if err := env.Engine.RequestLinkADR(ctx, devEUI, 3, 20, 1, 0x00FF, 0); err == nil {
		t.Error("RequestLinkADR accepted an out-of-range TX power index")
	}
	// NbTrans must be 1..15.
	if err := env.Engine.RequestLinkADR(ctx, devEUI, 3, 0, 0, 0x00FF, 0); err == nil {
		t.Error("RequestLinkADR accepted NbTrans 0")
	}

	// A valid request still works after the failed ones.
	if err := env.Engine.RequestLinkADR(ctx, devEUI, 5, 2, 1, 0x00FF, 0); err != nil {
		t.Errorf("RequestLinkADR with valid parameters: %v", err)
	}
	env.GW.Reset()
	env.Uplink(simDev, sim.UplinkOptions{NoFPort: true}, gateway.RxWindow1)
	if len(env.GW.Frames()) == 0 {
		t.Error("the valid LinkADRReq was not scheduled")
	}
}

func TestPeriodicDeviceStatusPoll(t *testing.T) {
	env := testenv.New(t, func(c *engine.Config) { c.DeviceStatusEvery = 2 })

	simDev, _ := env.Join()
	env.GW.Reset()

	// Counters 0 and 1 must not trigger a poll: 0 is skipped so a device does
	// not get polled the instant it comes online, and 1 is not a multiple of 2.
	for _, payload := range []string{"1", "2"} {
		env.Uplink(simDev, sim.UplinkOptions{FPort: 1, Payload: []byte(payload)}, gateway.RxWindow1)
		if frames := env.GW.Frames(); len(frames) != 0 {
			t.Fatalf("uplink %s produced %d downlink(s), want 0", payload, len(frames))
		}
	}

	// Counter 2 is a multiple of the poll interval.
	env.Uplink(simDev, sim.UplinkOptions{FPort: 1, Payload: []byte("3")}, gateway.RxWindow1)
	frames := env.GW.Frames()
	if len(frames) == 0 {
		t.Fatalf("counter 2 should trigger a DevStatusReq; events: %+v", env.Store.Events())
	}
	dl, err := simDev.HandleDownlink(frames[0].PHYPayload)
	if err != nil {
		t.Fatalf("device rejected the poll: %v", err)
	}
	if len(dl.MACCommands) == 0 || dl.MACCommands[0].CID != lorawan.CIDDevStatus {
		t.Errorf("expected a DevStatusReq, got %+v", dl.MACCommands)
	}
}
