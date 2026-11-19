package engine_test

import (
	"context"
	"testing"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/testenv"

	"github.com/fieldnode/lorawan-server/ns/internal/gateway"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
	"github.com/fieldnode/lorawan-server/ns/internal/sim"
)

// linkADRReqFrom lets the simulated device decode the downlink the engine
// scheduled and reports the LinkADRReq inside it, if any.
//
// Decoding through the device rather than reaching into the frame is deliberate:
// FOpts are encrypted with the session key, and the device is the party that has
// to be able to read them. Only the first frame is decoded — both RX windows
// carry the same frame with the same downlink counter, and decoding twice would
// advance the device's counter.
func linkADRReqFrom(t *testing.T, simDev *sim.Device, frames []gateway.DownlinkFrame) (lorawan.LinkADRReqPayload, bool) {
	t.Helper()
	if len(frames) == 0 {
		return lorawan.LinkADRReqPayload{}, false
	}
	dl, err := simDev.HandleDownlink(frames[0].PHYPayload)
	if err != nil {
		t.Fatalf("the device rejected the downlink: %v", err)
	}
	for _, c := range dl.MACCommands {
		if c.CID != lorawan.CIDLinkADR {
			continue
		}
		p, err := lorawan.ParseLinkADRReq(c.Payload)
		if err != nil {
			t.Fatalf("ParseLinkADRReq: %v", err)
		}
		return p, true
	}
	return lorawan.LinkADRReqPayload{}, false
}

// TestADRConvergesAndThenStops drives the whole loop with a simulated device:
// weak settings plus a strong signal produce a request, a positive answer moves
// the session, and once the band's ceilings are reached the server goes quiet.
//
// The "goes quiet" half is the important one. A server that keeps re-sending a
// LinkADRReq the device has already satisfied burns a downlink slot on every
// uplink forever, and on a busy gateway that is a measurable loss of capacity.
func TestADRConvergesAndThenStops(t *testing.T) {
	env := testenv.New(t, nil)
	ctx := context.Background()
	devEUI := testenv.MustEUI64(t, testenv.DevEUI)
	eu868 := testenv.MustRegion(t, "EU868")
	maxTX := len(eu868.TXPowerOffsets()) - 1

	simDev, _ := env.Join()

	// Park the device at the bottom of the band so there is somewhere to climb.
	ses := env.Session(devEUI)
	ses.DataRate, ses.TxPower, ses.NbTrans = 0, 0, 1
	if err := env.Store.SaveSession(ctx, ses); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	asked := 0
	for round := 0; round < 12; round++ {
		env.GW.Reset()
		env.UplinkSNR(simDev, sim.UplinkOptions{NoFPort: true, ADR: true}, 12.0)

		req, ok := linkADRReqFrom(t, simDev, env.GW.Frames())
		if !ok {
			break
		}
		asked++

		wire, err := simDev.LinkADRAns(true)
		if err != nil {
			t.Fatalf("LinkADRAns: %v", err)
		}
		env.UplinkWire(wire)

		after := env.Session(devEUI)
		if int(req.DataRate) != after.DataRate || int(req.TxPower) != after.TxPower {
			t.Fatalf("round %d: the device accepted DR%d TX%d but the session holds DR%d TX%d",
				round, req.DataRate, req.TxPower, after.DataRate, after.TxPower)
		}
	}

	final := env.Session(devEUI)
	if final.DataRate != 5 {
		t.Errorf("DataRate = %d, want the EU868 ceiling 5", final.DataRate)
	}
	if final.TxPower != maxTX {
		t.Errorf("TxPower = %d, want the top of the EU868 range %d", final.TxPower, maxTX)
	}
	if final.PendingADR != nil {
		t.Errorf("a request is still outstanding after converging: %+v", final.PendingADR)
	}
	if asked == 0 {
		t.Error("ADR never asked for anything")
	}
	if asked > 6 {
		t.Errorf("ADR asked %d times, want a handful of converging steps", asked)
	}

	// Steady state: more uplinks at the same signal must not produce a request.
	// Each is decoded as it arrives, so the device's downlink counter stays in
	// step with the engine's and a later frame can still be checked.
	for i := 0; i < 5; i++ {
		env.GW.Reset()
		env.UplinkSNR(simDev, sim.UplinkOptions{NoFPort: true, ADR: true}, 12.0)
		if _, ok := linkADRReqFrom(t, simDev, env.GW.Frames()); ok {
			t.Fatalf("uplink %d: a device already at both ceilings was asked again", i)
		}
	}
}

// TestADRRespectsTheDeviceADRBit covers the device's side of the bargain: a
// device that clears FCtrl.ADR has decided to keep its own data rate.
func TestADRRespectsTheDeviceADRBit(t *testing.T) {
	env := testenv.New(t, nil)
	devEUI := testenv.MustEUI64(t, testenv.DevEUI)

	simDev, _ := env.Join()
	// The session comes out of the join carrying the data rate the join-request
	// arrived on; what matters here is that ADR leaves it where it is.
	before := env.Session(devEUI).DataRate

	for i := 0; i < engine.ADRHistorySize; i++ {
		env.UplinkSNR(simDev, sim.UplinkOptions{NoFPort: true /* ADR clear */}, 15.0)
	}

	ses := env.Session(devEUI)
	if ses.DataRate != before {
		t.Errorf("DataRate = %d, want it left at %d", ses.DataRate, before)
	}
	if ses.PendingADR != nil {
		t.Errorf("an ADR-off device was asked to change: %+v", ses.PendingADR)
	}
	// The measurements are still recorded: a device can turn ADR back on at any
	// time, and the window should not start empty.
	if len(ses.UplinkHistory) == 0 {
		t.Error("uplink history must be recorded even while ADR is off")
	}
}

// TestADRDisabledByDeviceProfile is the operator's override.
func TestADRDisabledByDeviceProfile(t *testing.T) {
	env := testenv.New(t, nil)
	ctx := context.Background()
	devEUI := testenv.MustEUI64(t, testenv.DevEUI)

	simDev, _ := env.Join()

	dev, err := env.Store.DeviceByDevEUI(ctx, devEUI)
	if err != nil {
		t.Fatalf("DeviceByDevEUI: %v", err)
	}
	dev.DisableADR = true
	if err := env.Store.SaveDevice(ctx, dev); err != nil {
		t.Fatalf("SaveDevice: %v", err)
	}

	before := env.Session(devEUI).DataRate
	for i := 0; i < engine.ADRHistorySize; i++ {
		env.UplinkSNR(simDev, sim.UplinkOptions{NoFPort: true, ADR: true}, 15.0)
	}
	if ses := env.Session(devEUI); ses.PendingADR != nil || ses.DataRate != before {
		t.Errorf("DisableADR was ignored: DR%d (was %d) pending %+v",
			ses.DataRate, before, ses.PendingADR)
	}
}
