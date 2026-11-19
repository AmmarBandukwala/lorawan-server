package engine_test

import (
	"context"
	"testing"

	"github.com/fieldnode/lorawan-server/ns/internal/testenv"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/gateway"
	"github.com/fieldnode/lorawan-server/ns/internal/sim"
)

// TestUnconfirmedDownlinkIsTransmittedOnce is the regression this file exists
// for. An unconfirmed application frame is never acknowledged by the device —
// FCtrl.ACK only appears in reply to a confirmed downlink — so a queued frame
// that survived its own transmission would be re-sent on every uplink the device
// ever sends, each time with a fresh FCntDown, so the device would hand the same
// payload to its application over and over.
func TestUnconfirmedDownlinkIsTransmittedOnce(t *testing.T) {
	env := testenv.New(t, nil)
	simDev, _ := env.Join()
	ctx := context.Background()
	devEUI := testenv.MustEUI64(t, testenv.DevEUI)

	if err := env.Engine.EnqueueDownlink(ctx, devEUI, 10, []byte{0x01}, false); err != nil {
		t.Fatalf("EnqueueDownlink: %v", err)
	}
	env.Uplink(simDev, sim.UplinkOptions{FPort: 1, Payload: []byte{0xaa}}, gateway.RxWindow1)
	sent := env.GW.Frames()
	if len(sent) == 0 {
		t.Fatal("the queued downlink was not transmitted")
	}
	if env.Session(devEUI).Pending != nil {
		t.Fatal("an unconfirmed frame must not stay queued after transmission")
	}

	// A second uplink must produce no downlink at all.
	env.GW.Reset()
	env.Uplink(simDev, sim.UplinkOptions{FPort: 1, Payload: []byte{0xbb}}, gateway.RxWindow1)
	if again := env.GW.Frames(); len(again) != 0 {
		t.Errorf("the downlink was re-sent on the next uplink: %d frame(s)", len(again))
	}
}

// TestConfirmedDownlinkSurvivesUntilAcknowledged is the other half: a confirmed
// frame must be re-sent while the device has not answered, and dropped once it
// has.
func TestConfirmedDownlinkSurvivesUntilAcknowledged(t *testing.T) {
	env := testenv.New(t, nil)
	simDev, _ := env.Join()
	ctx := context.Background()
	devEUI := testenv.MustEUI64(t, testenv.DevEUI)

	if err := env.Engine.EnqueueDownlink(ctx, devEUI, 10, []byte{0x01}, true); err != nil {
		t.Fatalf("EnqueueDownlink: %v", err)
	}
	env.Uplink(simDev, sim.UplinkOptions{FPort: 1, Payload: []byte{0xaa}}, gateway.RxWindow1)
	first := env.Session(devEUI).Pending
	if first == nil {
		t.Fatal("a confirmed frame must stay queued until the device acknowledges it")
	}
	if first.FCntDown == 0 {
		t.Error("the transmitted frame must record the FCntDown it went out with")
	}

	// The device answers with a confirmed uplink carrying FCtrl.ACK.
	env.GW.Reset()
	env.Uplink(simDev, sim.UplinkOptions{FPort: 1, Payload: []byte{0xbb}, Confirmed: true, ACK: true},
		gateway.RxWindow1)
	if ses := env.Session(devEUI); ses.Pending != nil {
		t.Error("the acknowledged frame must be dropped from the queue")
	}
}

// TestDownlinkRetriesAreCapped: a device that has left coverage must not spend
// every window it ever opens re-transmitting a frame nobody will hear.
func TestDownlinkRetriesAreCapped(t *testing.T) {
	env := testenv.New(t, nil)
	simDev, _ := env.Join()
	ctx := context.Background()
	devEUI := testenv.MustEUI64(t, testenv.DevEUI)

	if err := env.Engine.EnqueueDownlink(ctx, devEUI, 10, []byte{0x01}, true); err != nil {
		t.Fatalf("EnqueueDownlink: %v", err)
	}
	for i := 0; i < engine.MaxDownlinkAttempts; i++ {
		env.GW.Reset()
		env.Uplink(simDev, sim.UplinkOptions{FPort: 1, Payload: []byte{byte(i)}}, gateway.RxWindow1)
	}
	if ses := env.Session(devEUI); ses.Pending != nil {
		t.Errorf("the frame is still queued after %d attempts", engine.MaxDownlinkAttempts)
	}
	if len(env.Store.EventsWithReason("downlink_abandoned")) != 1 {
		t.Errorf("giving up must be recorded; events: %+v", env.Store.Events())
	}

	// And after giving up, nothing more is transmitted for it.
	env.GW.Reset()
	env.Uplink(simDev, sim.UplinkOptions{FPort: 1, Payload: []byte{0xff}}, gateway.RxWindow1)
	if frames := env.GW.Frames(); len(frames) != 0 {
		t.Errorf("%d frame(s) sent after the network gave up", len(frames))
	}
}
