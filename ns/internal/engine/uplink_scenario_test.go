package engine_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/testenv"

	"github.com/fieldnode/lorawan-server/ns/internal/gateway"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
	"github.com/fieldnode/lorawan-server/ns/internal/sim"
)

func TestUplinkDecryptsAndDownlinkIsAcknowledged(t *testing.T) {
	env := testenv.New(t, nil)
	ctx := context.Background()
	devEUI := testenv.MustEUI64(t, testenv.DevEUI)

	simDev, _ := env.Join()
	env.GW.Reset()

	// A plain uplink with nothing queued must not produce a downlink: class-A
	// devices get no gratuitous frames.
	env.Uplink(simDev, sim.UplinkOptions{FPort: 10, Payload: []byte("hello fieldnode")}, gateway.RxWindow1)

	uplinks := env.Store.Uplinks()
	if len(uplinks) != 1 {
		t.Fatalf("recorded uplinks = %d, want 1", len(uplinks))
	}
	if got := string(uplinks[0].FRMPayload); got != "hello fieldnode" {
		t.Errorf("decrypted payload = %q, want %q", got, "hello fieldnode")
	}
	if uplinks[0].FPort != 10 {
		t.Errorf("FPort = %d, want 10", uplinks[0].FPort)
	}
	if uplinks[0].FCntUp != 0 {
		t.Errorf("FCntUp = %d, want 0", uplinks[0].FCntUp)
	}
	if uplinks[0].RSSI != -72 || uplinks[0].SNR != 8.5 {
		t.Errorf("radio metadata = %f/%f, want -72/8.5", uplinks[0].RSSI, uplinks[0].SNR)
	}
	if frames := env.GW.Frames(); len(frames) != 0 {
		t.Errorf("uplink with nothing queued produced %d downlink(s), want 0", len(frames))
	}
	if ses := env.Session(devEUI); ses.FCntUp != 1 {
		t.Errorf("session FCntUp = %d, want 1 (next expected)", ses.FCntUp)
	}

	// Queue an application downlink; it must ride the next uplink's RX window.
	if err := env.Engine.EnqueueDownlink(ctx, devEUI, 10, []byte("pump to 60%"), false); err != nil {
		t.Fatalf("EnqueueDownlink: %v", err)
	}
	env.GW.Reset()
	env.Uplink(simDev, sim.UplinkOptions{FPort: 10, Payload: []byte("2")}, gateway.RxWindow1)

	frames := env.GW.Frames()
	if len(frames) != 2 {
		t.Fatalf("the downlink should go to RX1 and RX2, got %d frames", len(frames))
	}
	dl, err := simDev.HandleDownlink(frames[0].PHYPayload)
	if err != nil {
		t.Fatalf("device rejected the downlink: %v", err)
	}
	if got := string(dl.FRMPayload); got != "pump to 60%" {
		t.Errorf("downlink payload = %q, want %q", got, "pump to 60%")
	}
	if dl.FPort != 10 {
		t.Errorf("downlink FPort = %d, want 10", dl.FPort)
	}
	if dl.FCntDown != 1 {
		t.Errorf("downlink FCntDown = %d, want 1", dl.FCntDown)
	}

	// The device acknowledges by setting FCtrl.ACK on its next uplink.
	env.GW.Reset()
	env.Uplink(simDev, sim.UplinkOptions{FPort: 10, Payload: []byte("3"), ACK: true}, gateway.RxWindow1)

	ses := env.Session(devEUI)
	if ses.Pending != nil {
		t.Errorf("pending downlink survived the acknowledgement: %+v", ses.Pending)
	}
	if ses.AFCntDown == 0 {
		t.Error("AFCntDown was not recorded from the ACK bit")
	}
	if frames := env.GW.Frames(); len(frames) != 0 {
		t.Errorf("after the ack the engine sent %d frame(s), want 0", len(frames))
	}
}

func TestConfirmedUplinkIsAcknowledged(t *testing.T) {
	env := testenv.New(t, nil)

	simDev, _ := env.Join()
	env.GW.Reset()

	env.Uplink(simDev, sim.UplinkOptions{FPort: 3, Payload: []byte("alarm"), Confirmed: true}, gateway.RxWindow1)

	frames := env.GW.Frames()
	if len(frames) == 0 {
		t.Fatalf("a confirmed uplink must be acknowledged; events: %+v", env.Store.Events())
	}
	dl, err := simDev.HandleDownlink(frames[0].PHYPayload)
	if err != nil {
		t.Fatalf("device rejected the ack frame: %v", err)
	}
	if !dl.ACK {
		t.Error("FCtrl.ACK not set on the response to a confirmed uplink")
	}
	if dl.FPort != -1 {
		t.Errorf("a pure ACK frame should carry no FPort, got %d", dl.FPort)
	}
	if up := env.Store.Uplinks(); len(up) == 0 || !up[len(up)-1].Confirmed {
		t.Error("the uplink was not recorded as confirmed")
	}
}

func TestDeviceStatusRoundTrip(t *testing.T) {
	env := testenv.New(t, nil)
	ctx := context.Background()
	devEUI := testenv.MustEUI64(t, testenv.DevEUI)

	simDev, _ := env.Join()
	env.GW.Reset()

	if err := env.Engine.RequestDeviceStatus(ctx, devEUI); err != nil {
		t.Fatalf("RequestDeviceStatus: %v", err)
	}
	env.Uplink(simDev, sim.UplinkOptions{NoFPort: true}, gateway.RxWindow1)

	frames := env.GW.Frames()
	if len(frames) == 0 {
		t.Fatalf("no DevStatusReq scheduled; events: %+v", env.Store.Events())
	}
	dl, err := simDev.HandleDownlink(frames[0].PHYPayload)
	if err != nil {
		t.Fatalf("device rejected the downlink: %v", err)
	}
	if len(dl.MACCommands) != 1 || dl.MACCommands[0].CID != lorawan.CIDDevStatus {
		t.Fatalf("downlink MAC commands = %+v, want a single DevStatusReq", dl.MACCommands)
	}

	env.GW.Reset()
	// The answer rides in FOpts (NoFPort), the common case for a MAC-only
	// uplink.
	env.Uplink(simDev, sim.UplinkOptions{
		NoFPort: true,
		MACCommands: lorawan.MACCommandSet{{
			CID:     lorawan.CIDDevStatus,
			Payload: lorawan.DevStatusAnsPayload{Battery: 187, SNR: -4}.Marshal(),
		}},
	}, gateway.RxWindow1)

	ses := env.Session(devEUI)
	if !ses.HasDeviceStatus {
		t.Fatal("device status was not recorded on the session")
	}
	if ses.Battery != 187 || ses.Margin != -4 {
		t.Errorf("device status = battery %d margin %d, want 187/-4", ses.Battery, ses.Margin)
	}
}

func TestLinkADRApplyOnlyAfterAnswer(t *testing.T) {
	env := testenv.New(t, nil)
	ctx := context.Background()
	devEUI := testenv.MustEUI64(t, testenv.DevEUI)

	simDev, _ := env.Join()
	env.GW.Reset()

	// EU868: DR3 is an uplink data-rate and TX index 2 exists.
	if err := env.Engine.RequestLinkADR(ctx, devEUI, 3, 2, 1, 0x00FF, 0); err != nil {
		t.Fatalf("RequestLinkADR: %v", err)
	}
	env.Uplink(simDev, sim.UplinkOptions{NoFPort: true}, gateway.RxWindow1)

	frames := env.GW.Frames()
	if len(frames) == 0 {
		t.Fatalf("no LinkADRReq scheduled; events: %+v", env.Store.Events())
	}
	dl, err := simDev.HandleDownlink(frames[0].PHYPayload)
	if err != nil {
		t.Fatalf("device rejected the downlink: %v", err)
	}
	if len(dl.MACCommands) != 1 || dl.MACCommands[0].CID != lorawan.CIDLinkADR {
		t.Fatalf("MAC commands = %+v, want a single LinkADRReq", dl.MACCommands)
	}
	req, err := lorawan.ParseLinkADRReq(dl.MACCommands[0].Payload)
	if err != nil {
		t.Fatalf("ParseLinkADRReq: %v", err)
	}
	if req.DataRate != 3 || req.TxPower != 2 || req.NbRep != 1 || req.ChMask != 0x00FF {
		t.Errorf("LinkADRReq = %+v, want DR3 TX2 NbRep1 ChMask 0x00FF", req)
	}

	// Until the device answers, the server must not assume the new parameters.
	if ses := env.Session(devEUI); ses.DataRate == 3 {
		t.Error("the data-rate changed before the device acknowledged it")
	}

	env.GW.Reset()
	// This answer deliberately uses the other transport: FPort 0 with the
	// commands encrypted inside the FRMPayload instead of FOpts.
	env.Uplink(simDev, sim.UplinkOptions{
		MACCommands: lorawan.MACCommandSet{{
			CID: lorawan.CIDLinkADR,
			Payload: lorawan.LinkADRAnsPayload{
				PowerACK: true, DataRateACK: true, ChannelACK: true,
			}.Marshal(),
		}},
	}, gateway.RxWindow1)

	ses := env.Session(devEUI)
	if ses.DataRate != 3 || ses.TxPower != 2 || ses.NbTrans != 1 {
		t.Errorf("after LinkADRAns session = DR%d TX%d NbTrans%d, want DR3 TX2 NbTrans1",
			ses.DataRate, ses.TxPower, ses.NbTrans)
	}
	if ses.PendingADR != nil {
		t.Error("the ADR request was not cleared after the answer")
	}
}

func TestLinkADRRefusalKeepsParameters(t *testing.T) {
	env := testenv.New(t, nil)
	ctx := context.Background()
	devEUI := testenv.MustEUI64(t, testenv.DevEUI)

	simDev, _ := env.Join()
	before := env.Session(devEUI)

	if err := env.Engine.RequestLinkADR(ctx, devEUI, 3, 2, 1, 0x00FF, 0); err != nil {
		t.Fatalf("RequestLinkADR: %v", err)
	}
	env.Uplink(simDev, sim.UplinkOptions{NoFPort: true}, gateway.RxWindow1)

	// The device refuses: every status bit cleared.
	env.Uplink(simDev, sim.UplinkOptions{
		NoFPort:     true,
		MACCommands: lorawan.MACCommandSet{{CID: lorawan.CIDLinkADR, Payload: []byte{0x00}}},
	}, gateway.RxWindow1)

	ses := env.Session(devEUI)
	if ses.DataRate != before.DataRate || ses.TxPower != before.TxPower {
		t.Errorf("a refused ADR changed the session: DR%d TX%d, want DR%d TX%d",
			ses.DataRate, ses.TxPower, before.DataRate, before.TxPower)
	}
	if len(env.Store.EventsWithReason("link_adr_rejected")) != 1 {
		t.Errorf("want one link_adr_rejected event, got %+v", env.Store.Events())
	}
}

func TestABPUplink(t *testing.T) {
	env := testenv.New(t, nil)
	ctx := context.Background()

	dev := env.Provision(false)
	ses := &engine.Session{
		DevEUI: dev.DevEUI, DevAddr: dev.DevAddr,
		NwkSKey: dev.NwkSKey, AppSKey: dev.AppSKey,
		KeyEpoch: 1, Region: "EU868",
		DataRate: testenv.EU868DR5, TxPower: 0, NbTrans: 1,
		Rx2DataRate: engine.UseRegionDefault,
	}
	if err := env.Store.SaveSession(ctx, ses); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	simDev := &sim.Device{
		DevEUI: dev.DevEUI, JoinEUI: dev.JoinEUI, NetID: env.Engine.Config().NetID,
		Joined: true, DevAddr: dev.DevAddr, NwkSKey: dev.NwkSKey, AppSKey: dev.AppSKey,
	}

	env.Uplink(simDev, sim.UplinkOptions{FPort: 2, Payload: []byte("abp boot")}, gateway.RxWindow1)

	uplinks := env.Store.Uplinks()
	if len(uplinks) != 1 {
		t.Fatalf("recorded uplinks = %d, want 1; events: %+v", len(uplinks), env.Store.Events())
	}
	if got := string(uplinks[0].FRMPayload); got != "abp boot" {
		t.Errorf("ABP payload = %q, want %q", got, "abp boot")
	}
	if uplinks[0].DevAddr != dev.DevAddr {
		t.Errorf("recorded DevAddr = %s, want %s", uplinks[0].DevAddr, dev.DevAddr)
	}
}

func TestUplinkUnknownDevAddr(t *testing.T) {
	env := testenv.New(t, nil)

	simDev, _ := env.Join()
	env.GW.Reset()

	// A frame addressed to an address no session owns.
	simDev.DevAddr = testenv.MustDevAddr(t, "deadbeef")
	wire, err := simDev.NextUplink(sim.UplinkOptions{FPort: 1, Payload: []byte("x")})
	if err != nil {
		t.Fatalf("NextUplink: %v", err)
	}
	if err := env.UplinkWire(wire); err != nil {
		t.Fatalf("HandleUplink: %v", err)
	}

	if frames := env.GW.Frames(); len(frames) != 0 {
		t.Errorf("an unknown DevAddr produced %d downlink(s), want 0", len(frames))
	}
	if len(env.Store.EventsWithReason(engine.ReasonUnknownDevAddr)) != 1 {
		t.Errorf("want one %s event, got %+v", engine.ReasonUnknownDevAddr, env.Store.Events())
	}
	if len(env.Store.Uplinks()) != 0 {
		t.Error("an unknown DevAddr must not be recorded as traffic")
	}
}

// TestUplinkFOptsAreEncryptedOnTheWire pins LoRaWAN 1.0.x §4.3.3: a device
// encrypts uplink FOpts with NwkSKey and the network server must decrypt them
// before parsing. A server that reads FOpts as cleartext passes tests written
// against a lenient simulator, but drops every real device's MAC commands — so
// this asserts both the ciphertext on the wire and the decoded result.
func TestUplinkFOptsAreEncryptedOnTheWire(t *testing.T) {
	env := testenv.New(t, nil)
	devEUI := testenv.MustEUI64(t, testenv.DevEUI)

	simDev, _ := env.Join()
	env.GW.Reset()

	ans := lorawan.MACCommandSet{{
		CID:     lorawan.CIDDevStatus,
		Payload: lorawan.DevStatusAnsPayload{Battery: 210, SNR: 7}.Marshal(),
	}}
	plain, err := ans.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}

	wire, err := simDev.NextUplink(sim.UplinkOptions{NoFPort: true, MACCommands: ans})
	if err != nil {
		t.Fatalf("NextUplink: %v", err)
	}

	onAir, err := lorawan.ParsePHYPayload(wire)
	if err != nil {
		t.Fatalf("ParsePHYPayload: %v", err)
	}
	if int(onAir.MAC.FHDR.FCtrl.FOptsLen) != len(plain) {
		t.Fatalf("FOptsLen = %d, want %d", onAir.MAC.FHDR.FCtrl.FOptsLen, len(plain))
	}
	if bytes.Equal(onAir.MAC.FHDR.FOpts, plain) {
		t.Fatal("FOpts went out in cleartext; 1.0.x §4.3.3 requires NwkSKey encryption")
	}

	if err := env.UplinkWire(wire); err != nil {
		t.Fatalf("HandleUplink: %v", err)
	}
	ses := env.Session(devEUI)
	if !ses.HasDeviceStatus {
		t.Fatalf("encrypted FOpts were not decoded; events: %+v", env.Store.Events())
	}
	if ses.Battery != 210 || ses.Margin != 7 {
		t.Errorf("device status = battery %d margin %d, want 210/7", ses.Battery, ses.Margin)
	}
}

// TestDownlinkSkipsWindowThatCannotCarryIt checks the RP002 payload limit. The
// uplink arrives at DR5, so RX1 is DR5 (255-byte limit) while RX2 is the EU868
// default DR0 (59 bytes). A 200-byte payload fits RX1 only, so exactly one
// transmission may be scheduled and the reason must be recorded.
func TestDownlinkSkipsWindowThatCannotCarryIt(t *testing.T) {
	env := testenv.New(t, nil)
	ctx := context.Background()
	devEUI := testenv.MustEUI64(t, testenv.DevEUI)

	simDev, _ := env.Join()
	env.GW.Reset()

	if err := env.Engine.EnqueueDownlink(ctx, devEUI, 10, bytes.Repeat([]byte("f"), 200), false); err != nil {
		t.Fatalf("EnqueueDownlink: %v", err)
	}
	env.Uplink(simDev, sim.UplinkOptions{NoFPort: true}, gateway.RxWindow1)

	frames := env.GW.Frames()
	if len(frames) != 1 {
		t.Fatalf("scheduled %d frame(s), want 1 (RX1 only); events: %+v", len(frames), env.Store.Events())
	}
	if frames[0].Window != gateway.RxWindow1 {
		t.Errorf("window = %s, want RX1", frames[0].Window)
	}
	if n := len(frames[0].PHYPayload); n <= 59 {
		t.Errorf("frame is %d bytes; it no longer exceeds the RX2 limit, so this test proves nothing", n)
	}
	if len(env.Store.EventsWithReason(engine.ReasonDownlinkTooLarge)) != 1 {
		t.Errorf("want one %s event, got %+v", engine.ReasonDownlinkTooLarge, env.Store.Events())
	}
}

// TestLinkADRRejectsDataRateOutsideBandRange checks that the server never asks a
// device for a rate the band does not certify. EU868 does define DR12/DR13
// (SF6/SF5) in its data-rate table, but its regional parameters cap ordinary
// uplink ADR at DR5, so a real device would only answer in the negative.
func TestLinkADRRejectsDataRateOutsideBandRange(t *testing.T) {
	env := testenv.New(t, nil)
	ctx := context.Background()
	devEUI := testenv.MustEUI64(t, testenv.DevEUI)

	env.Join()

	if err := env.Engine.RequestLinkADR(ctx, devEUI, 13, 2, 1, 0x00FF, 0); err == nil {
		t.Error("RequestLinkADR accepted DR13 on EU868, above the band ceiling of DR5")
	}
	if err := env.Engine.RequestLinkADR(ctx, devEUI, 5, 2, 1, 0x00FF, 0); err != nil {
		t.Errorf("RequestLinkADR(DR5), the band ceiling, failed: %v", err)
	}
}
