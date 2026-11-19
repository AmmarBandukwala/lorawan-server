package scenarios_test

import (
	"context"
	"testing"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/test/testenv"

	"github.com/fieldnode/lorawan-server/ns/internal/gateway"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
	"github.com/fieldnode/lorawan-server/ns/internal/sim"
)

func TestOTAAJoinSchedulesBothWindows(t *testing.T) {
	env := testenv.New(t, nil)

	simDev, frames := env.Join()

	if len(frames) != 2 {
		t.Fatalf("want a join-accept in RX1 and RX2, got %d frames", len(frames))
	}
	rx1, rx2 := frames[0], frames[1]

	// EU868: DR5 uplink maps to DR0 downlink; join-accept RX1 at 5 s, RX2 at 6 s.
	// RP002 EU868: with RX1DROffset 0 the RX1 data-rate equals the uplink
	// data-rate, so a DR5 uplink is answered at DR5 (not DR0 — that would be
	// the RX2 data-rate).
	if rx1.Window != gateway.RxWindow1 || rx1.FrequencyHz != testenv.EU868Ch0Hz || rx1.DataRateIndex != testenv.EU868DR5 {
		t.Errorf("RX1 plan = %s @ %d Hz DR%d, want RX1 @ %d Hz DR%d",
			rx1.Window, rx1.FrequencyHz, rx1.DataRateIndex, testenv.EU868Ch0Hz, testenv.EU868DR5)
	}
	if rx1.Delay != 5*time.Second {
		t.Errorf("RX1 delay = %v, want 5s", rx1.Delay)
	}
	if rx2.Window != gateway.RxWindow2 || rx2.FrequencyHz != 869525000 || rx2.Delay != 6*time.Second {
		t.Errorf("RX2 plan = %s @ %d Hz after %v, want RX2 @ 869525000 Hz after 6s",
			rx2.Window, rx2.FrequencyHz, rx2.Delay)
	}
	if want := testenv.UplinkFrame(nil, 0, 0, 0).UplinkID; rx1.UplinkID != want {
		t.Errorf("RX1 uplink token = %x, want the uplink token %x echoed back", rx1.UplinkID, want)
	}

	// Both windows must carry the same join-accept.
	ja1, _, err := lorawan.ParseJoinAccept(rx1.PHYPayload, testenv.MustKey(t, testenv.AppKey))
	if err != nil {
		t.Fatalf("RX1 join-accept does not decode: %v", err)
	}
	ja2, _, err := lorawan.ParseJoinAccept(rx2.PHYPayload, testenv.MustKey(t, testenv.AppKey))
	if err != nil {
		t.Fatalf("RX2 join-accept does not decode: %v", err)
	}
	if ja1.DevAddr != ja2.DevAddr || ja1.JoinNonce != ja2.JoinNonce {
		t.Errorf("RX1 and RX2 join-accepts differ: %+v vs %+v", ja1, ja2)
	}
	if ja1.NetID != env.Engine.Config().NetID {
		t.Errorf("join-accept NetID %s, want %s", ja1.NetID, env.Engine.Config().NetID)
	}
	if ja1.RxDelay != 1 {
		t.Errorf("join-accept RxDelay = %d, want 1 (EU868 RX1 delay)", ja1.RxDelay)
	}

	ses := env.Session(testenv.MustEUI64(t, testenv.DevEUI))
	if ses.DevAddr != simDev.DevAddr {
		t.Errorf("server DevAddr %s != device DevAddr %s", ses.DevAddr, simDev.DevAddr)
	}
	if !ses.NwkSKey.Equal(simDev.NwkSKey) {
		t.Error("server and device disagree on NwkSKey")
	}
	if !ses.AppSKey.Equal(simDev.AppSKey) {
		t.Error("server and device disagree on AppSKey")
	}
	if ses.FCntUp != 0 {
		t.Errorf("fresh session FCntUp = %d, want 0 (next expected)", ses.FCntUp)
	}
	if ses.Region != "EU868" {
		t.Errorf("session region = %q, want EU868", ses.Region)
	}
	if got := len(env.Store.EventsOfKind(engine.EventJoinAccept)); got != 1 {
		t.Errorf("join_accept events = %d, want 1", got)
	}
}

func TestJoinUplinkInRX2PlansRX2Only(t *testing.T) {
	env := testenv.New(t, nil)
	env.Provision(true)

	simDev := sim.New(testenv.MustEUI64(t, testenv.DevEUI), testenv.MustEUI64(t, testenv.JoinEUI),
		testenv.MustKey(t, testenv.AppKey), env.Engine.Config().NetID)
	wire, err := simDev.NextJoinRequest()
	if err != nil {
		t.Fatalf("NextJoinRequest: %v", err)
	}
	if err := env.Engine.HandleUplink(context.Background(),
		testenv.UplinkFrame(wire, gateway.RxWindow2, 869525000, 0)); err != nil {
		t.Fatalf("HandleUplink: %v", err)
	}

	frames := env.GW.Frames()
	if len(frames) != 1 {
		t.Fatalf("a join-request received in RX2 must only be answered in RX2, got %d frames", len(frames))
	}
	if frames[0].Window != gateway.RxWindow2 {
		t.Errorf("window = %s, want RX2", frames[0].Window)
	}
	if err := simDev.HandleJoinAccept(frames[0].PHYPayload); err != nil {
		t.Fatalf("device rejected the join-accept: %v", err)
	}
}

func TestJoinUnknownDevice(t *testing.T) {
	env := testenv.New(t, nil)

	simDev := sim.New(testenv.MustEUI64(t, "ffffffffffffffff"), testenv.MustEUI64(t, testenv.JoinEUI),
		testenv.MustKey(t, testenv.AppKey), env.Engine.Config().NetID)
	wire, err := simDev.NextJoinRequest()
	if err != nil {
		t.Fatalf("NextJoinRequest: %v", err)
	}
	if err := env.UplinkWire(wire); err != nil {
		t.Fatalf("HandleUplink: %v", err)
	}

	if frames := env.GW.Frames(); len(frames) != 0 {
		t.Errorf("an unknown device must not get a join-accept, got %d", len(frames))
	}
	if len(env.Store.EventsWithReason(engine.ReasonUnknownDevice)) != 1 {
		t.Errorf("want one %s event, got %+v", engine.ReasonUnknownDevice, env.Store.Events())
	}
}

func TestJoinMICFailure(t *testing.T) {
	env := testenv.New(t, nil)

	// The server stores a different AppKey than the device uses.
	dev := engine.NewOTAADevice(testenv.MustEUI64(t, testenv.DevEUI), testenv.MustEUI64(t, testenv.JoinEUI),
		testenv.MustKey(t, "00000000000000000000000000000001"))
	if err := env.Store.SaveDevice(context.Background(), dev); err != nil {
		t.Fatalf("SaveDevice: %v", err)
	}

	simDev := sim.New(testenv.MustEUI64(t, testenv.DevEUI), testenv.MustEUI64(t, testenv.JoinEUI),
		testenv.MustKey(t, testenv.AppKey), env.Engine.Config().NetID)
	wire, err := simDev.NextJoinRequest()
	if err != nil {
		t.Fatalf("NextJoinRequest: %v", err)
	}
	if err := env.UplinkWire(wire); err != nil {
		t.Fatalf("HandleUplink: %v", err)
	}

	if frames := env.GW.Frames(); len(frames) != 0 {
		t.Errorf("a bad MIC must not get a join-accept, got %d frames", len(frames))
	}
	if len(env.Store.EventsWithReason(engine.ReasonJoinMICFailed)) != 1 {
		t.Errorf("want one %s event, got %+v", engine.ReasonJoinMICFailed, env.Store.Events())
	}
	if _, err := env.Store.SessionByDevEUI(context.Background(), testenv.MustEUI64(t, testenv.DevEUI)); err == nil {
		t.Error("a failed join must not create a session")
	}
}

func TestJoinFromABPDeviceRejected(t *testing.T) {
	env := testenv.New(t, nil)
	env.Provision(false)

	simDev := sim.New(testenv.MustEUI64(t, testenv.DevEUI), testenv.MustEUI64(t, testenv.JoinEUI),
		testenv.MustKey(t, testenv.AppKey), env.Engine.Config().NetID)
	wire, err := simDev.NextJoinRequest()
	if err != nil {
		t.Fatalf("NextJoinRequest: %v", err)
	}
	if err := env.UplinkWire(wire); err != nil {
		t.Fatalf("HandleUplink: %v", err)
	}

	if len(env.Store.EventsWithReason(engine.ReasonNotOTAA)) != 1 {
		t.Errorf("want one %s event, got %+v", engine.ReasonNotOTAA, env.Store.Events())
	}
}

func TestRejoinBumpsKeyEpochAndJoinNonce(t *testing.T) {
	env := testenv.New(t, nil)
	ctx := context.Background()

	simDev, _ := env.Join()
	first := env.Session(testenv.MustEUI64(t, testenv.DevEUI))

	env.GW.Reset()
	wire, err := simDev.NextJoinRequest()
	if err != nil {
		t.Fatalf("NextJoinRequest: %v", err)
	}
	if err := env.UplinkWire(wire); err != nil {
		t.Fatalf("HandleUplink(rejoin): %v", err)
	}
	frames := env.GW.Frames()
	if len(frames) == 0 {
		t.Fatalf("re-join produced no join-accept; events: %+v", env.Store.Events())
	}
	// The device must accept the new join-accept: its JoinNonce has to move
	// forward, otherwise a captured join-accept could be replayed.
	if err := simDev.HandleJoinAccept(frames[0].PHYPayload); err != nil {
		t.Fatalf("device rejected the re-join accept: %v", err)
	}

	second := env.Session(testenv.MustEUI64(t, testenv.DevEUI))
	if second.KeyEpoch != first.KeyEpoch+1 {
		t.Errorf("key epoch = %d, want %d", second.KeyEpoch, first.KeyEpoch+1)
	}
	if second.DevAddr == first.DevAddr {
		t.Error("a re-join must allocate a fresh DevAddr")
	}
	if second.NwkSKey.Equal(first.NwkSKey) {
		t.Error("a re-join must derive fresh session keys")
	}
	if second.FCntUp != 0 {
		t.Errorf("re-joined session FCntUp = %d, want 0", second.FCntUp)
	}

	dev, err := env.Store.DeviceByDevEUI(ctx, testenv.MustEUI64(t, testenv.DevEUI))
	if err != nil {
		t.Fatalf("DeviceByDevEUI: %v", err)
	}
	if dev.NextJoinNonce != 2 {
		t.Errorf("device NextJoinNonce = %d, want 2", dev.NextJoinNonce)
	}
}
