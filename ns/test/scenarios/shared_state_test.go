package scenarios_test

import (
	"context"
	"testing"

	"github.com/fieldnode/lorawan-server/ns/internal/gateway"
	"github.com/fieldnode/lorawan-server/ns/internal/sim"
	"github.com/fieldnode/lorawan-server/ns/test/testenv"
)

// joinRequest builds the bytes of a join-request without sending them anywhere,
// so the same frame can be offered to more than one engine.
func joinRequest(t *testing.T) []byte {
	t.Helper()

	dev := sim.New(testenv.MustEUI64(t, testenv.DevEUI),
		testenv.MustEUI64(t, testenv.JoinEUI),
		testenv.MustKey(t, testenv.AppKey),
		testenv.MustNetID(t, "010203"))

	wire, err := dev.NextJoinRequest()
	if err != nil {
		t.Fatalf("NextJoinRequest: %v", err)
	}
	return wire
}

// TestSharedBookStopsTwoInstancesAnsweringTwice is the point of the persisted
// de-duplication book.
//
// One forwarder delivers the same radio frame to every network-server instance it
// is configured to send to. Without a shared book each instance answers on its
// own, and the device receives two join-accepts — which a real device resolves by
// taking whichever it decodes first and, on some firmware, by failing to join at
// all. No single-instance test can observe this, which is why it needs two
// engines and one book.
func TestSharedBookStopsTwoInstancesAnsweringTwice(t *testing.T) {
	shared := testenv.NewSharedSeen()

	// Two instances, each knowing the device, sharing only the frame book.
	a := testenv.NewShared(t, testenv.SharedState{Seen: shared}, nil)
	b := testenv.NewShared(t, testenv.SharedState{Seen: shared}, nil)
	a.Provision(true)
	b.Provision(true)

	wire := joinRequest(t)
	frame := testenv.UplinkFrame(wire, gateway.RxWindow1, testenv.EU868Ch0Hz, testenv.EU868DR5)
	ctx := context.Background()

	if err := a.Engine.HandleUplink(ctx, frame); err != nil {
		t.Fatalf("instance A HandleUplink: %v", err)
	}
	if len(a.GW.Frames()) == 0 {
		t.Fatal("instance A did not answer the join-request at all")
	}

	if err := b.Engine.HandleUplink(ctx, frame); err != nil {
		t.Fatalf("instance B HandleUplink: %v", err)
	}
	if frames := b.GW.Frames(); len(frames) != 0 {
		t.Errorf("instance B answered a frame instance A already answered: %d downlinks", len(frames))
	}

	// B must say why, in terms an operator can act on, rather than stay silent.
	duplicates := b.Store.EventsOfKind("uplink_drop")
	if len(duplicates) == 0 {
		t.Fatal("instance B dropped the frame without recording an event")
	}
	if got := duplicates[0].Reason; got != "duplicate_frame" {
		t.Errorf("drop reason = %q, want duplicate_frame", got)
	}

	if got := shared.Claims(); got != 1 {
		t.Errorf("the shared book holds %d claims, want 1: the second instance should not re-claim", got)
	}
}

// TestUnsharedBooksLetBothInstancesAnswer documents the failure the shared book
// exists to prevent. It is a regression test in the uncomfortable direction: if
// this ever stops failing, the de-duplication book has leaked between engines
// that were meant to be independent.
func TestUnsharedBooksLetBothInstancesAnswer(t *testing.T) {
	a := testenv.New(t, nil)
	b := testenv.New(t, nil)
	a.Provision(true)
	b.Provision(true)

	wire := joinRequest(t)
	frame := testenv.UplinkFrame(wire, gateway.RxWindow1, testenv.EU868Ch0Hz, testenv.EU868DR5)
	ctx := context.Background()

	for _, env := range []*testenv.Env{a, b} {
		if err := env.Engine.HandleUplink(ctx, frame); err != nil {
			t.Fatalf("HandleUplink: %v", err)
		}
	}
	if len(a.GW.Frames()) == 0 || len(b.GW.Frames()) == 0 {
		t.Fatal("independent instances should each answer, so this test's premise broke")
	}
	t.Log("two unshared instances both answered the same frame: this is why the book is persisted")
}
