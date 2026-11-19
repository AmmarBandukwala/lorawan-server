package semtech

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/gateway"
	"github.com/fieldnode/lorawan-server/ns/internal/sim"
)

// liveStack is the whole network server with nothing stubbed out: an engine on
// an in-memory store, a real UDP transport, a fake forwarder socket and a
// simulated device that has already joined.
type liveStack struct {
	eng   *engine.Engine
	srv   *Server
	gw    *fakeGateway
	dev   *sim.Device
	store *engine.MemStore
}

func newLiveStack(t *testing.T) *liveStack {
	t.Helper()
	ctx := context.Background()
	store := engine.NewMemStore()

	devEUI := mustEUI(t, "0011223344556677")
	joinEUI := mustEUI(t, "0102030405060708")
	appKey := mustKey(t, "2b7e151628aed2a6abf7158809cf4f3c")
	netID := mustNetID(t, "010203")

	if err := store.SaveDevice(ctx, engine.NewOTAADevice(devEUI, joinEUI, appKey)); err != nil {
		t.Fatalf("SaveDevice: %v", err)
	}
	cfg := engine.DefaultConfig()
	cfg.NetID = netID
	cfg.Region = "EU868"

	lazy := &gateway.Lazy{}
	eng, err := engine.New(cfg, store.Store(), lazy,
		engine.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		// Transmit inline so a PULL_RESP is on the wire once HandleUplink
		// returns, which is what makes the reads below deterministic.
		engine.WithScheduler(func(_ time.Duration, fn func()) { fn() }))
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	srv, err := New(Config{
		Addr:    "127.0.0.1:0",
		Region:  "EU868",
		Handler: eng,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	lazy.Set(srv)

	gw := newFakeGateway(t, srv.ListenAddr())
	gw.send(PullData, "")
	gw.read() // PULL_ACK, and it teaches the server where the gateway lives

	// Join, so the rest of the suite runs against a real session rather than a
	// hand-built one.
	dev := sim.New(devEUI, joinEUI, appKey, netID)
	join, err := dev.NextJoinRequest()
	if err != nil {
		t.Fatalf("NextJoinRequest: %v", err)
	}
	gw.send(PushData, pushData(join, 868.1, "SF12BW125", 1_000_000))
	gw.read() // PUSH_ACK
	_, body := gw.read()
	tx := decodeTXPK(t, body)
	gw.read() // the same join-accept, scheduled in the second window

	raw, err := hex.DecodeString(tx.Data)
	if err != nil {
		t.Fatalf("the join-accept is not hex: %v", err)
	}
	if err := dev.HandleJoinAccept(raw); err != nil {
		t.Fatalf("HandleJoinAccept: %v", err)
	}
	return &liveStack{eng: eng, srv: srv, gw: gw, dev: dev, store: store}
}

// pushData renders a one-entry rxpk body.
func pushData(frame []byte, freqMHz float64, datr string, tmst uint32) string {
	return fmt.Sprintf(
		`{"rxpk":[{"tmst":%d,"freq":%g,"rssi":-70,"lsnr":9,"size":%d,`+
			`"modu":"LORA","datr":"%s","codr":"4/5","data":"%s"}]}`,
		tmst, freqMHz, len(frame), datr, hex.EncodeToString(frame))
}

func decodeTXPK(t *testing.T, body []byte) DownlinkCommand {
	t.Helper()
	var wrapper struct {
		Tx DownlinkCommand `json:"txpk"`
	}
	if err := json.Unmarshal(body, &wrapper); err != nil {
		t.Fatalf("decode txpk %s: %v", body, err)
	}
	return wrapper.Tx
}

// readDownlink decodes the next PULL_RESP the server pushes.
func readDownlink(t *testing.T, gw *fakeGateway) DownlinkCommand {
	t.Helper()
	h, body := gw.read()
	if h.Type != PullResp {
		t.Fatalf("reply = %s, want PULL_RESP", h.Type)
	}
	return decodeTXPK(t, body)
}

// TestApplicationDownlinkLeavesInTheNextWindow is the M5 path end to end: an
// application queues a frame, the device speaks, and the frame goes out in the
// window that uplink opened, encrypted so only that device can read it.
//
// This is the behaviour a class-A network is judged on. A downlink accepted by
// an API and then lost inside the server is worse than one refused up front,
// because the application believes it was sent.
func TestApplicationDownlinkLeavesInTheNextWindow(t *testing.T) {
	ctx := context.Background()
	st := newLiveStack(t)

	payload := []byte{0x01, 0x02, 0x03, 0x04, 0x05}
	if err := st.eng.EnqueueDownlink(ctx, st.dev.DevEUI, 10, payload, false); err != nil {
		t.Fatalf("EnqueueDownlink: %v", err)
	}

	// The device has no idea a downlink is waiting; it just reports.
	up, err := st.dev.NextUplink(sim.UplinkOptions{FPort: 1, Payload: []byte{0xaa}})
	if err != nil {
		t.Fatalf("NextUplink: %v", err)
	}
	st.gw.send(PushData, pushData(up, 868.1, "SF7BW125", 5_000_000))
	st.gw.read() // PUSH_ACK

	tx := readDownlink(t, st.gw)
	raw, err := hex.DecodeString(tx.Data)
	if err != nil {
		t.Fatalf("txpk data is not hex: %v", err)
	}
	if tx.Size != len(raw) {
		t.Errorf("txpk size = %d but data holds %d bytes", tx.Size, len(raw))
	}
	// RX1 follows the uplink channel, and EU868 uses a 1:1 data-rate offset at
	// RX1DROffset 0, so an SF7 uplink is answered on the same channel at SF7.
	if tx.FrequencyMHZ != 868.1 {
		t.Errorf("RX1 freq = %v MHz, want the uplink channel", tx.FrequencyMHZ)
	}
	if tx.DataRate != "SF7BW125" {
		t.Errorf("RX1 datr = %q, want SF7BW125", tx.DataRate)
	}
	if tx.TimestampUS == nil || *tx.TimestampUS != 5_000_000+1_000_000 {
		t.Errorf("RX1 tmst = %v, want the uplink stamp plus the 1 s RX1 delay", tx.TimestampUS)
	}

	dl, err := st.dev.HandleDownlink(raw)
	if err != nil {
		t.Fatalf("the device could not read the queued downlink: %v", err)
	}
	if dl.FPort != 10 {
		t.Errorf("FPort = %d, want 10", dl.FPort)
	}
	if string(dl.FRMPayload) != string(payload) {
		t.Errorf("payload = % x, want % x", dl.FRMPayload, payload)
	}

	// What the network stored for that uplink must be the decrypted application
	// payload. An integration that had to decrypt frames itself would need the
	// session keys, which would make every application a party to the network's
	// key material.
	stored := st.store.Uplinks()
	if len(stored) != 1 {
		t.Fatalf("the store holds %d uplinks, want 1", len(stored))
	}
	if string(stored[0].FRMPayload) != "\xaa" {
		t.Errorf("stored payload = % x, want the plaintext aa", stored[0].FRMPayload)
	}
	if stored[0].FPort != 1 {
		t.Errorf("stored FPort = %d, want 1", stored[0].FPort)
	}
	if stored[0].FrequencyHz != 868100000 || stored[0].DataRateIndex != 5 {
		t.Errorf("stored radio metadata = %d Hz DR%d", stored[0].FrequencyHz, stored[0].DataRateIndex)
	}
}

// TestQueuedDownlinkIsDeliveredOnce: the frame waits for a window, goes out in
// it, and a later uplink must not replay an already-accepted application frame.
func TestQueuedDownlinkIsDeliveredOnce(t *testing.T) {
	ctx := context.Background()
	st := newLiveStack(t)

	if err := st.eng.EnqueueDownlink(ctx, st.dev.DevEUI, 10, []byte{0x42}, false); err != nil {
		t.Fatalf("EnqueueDownlink: %v", err)
	}
	up, err := st.dev.NextUplink(sim.UplinkOptions{FPort: 1, Payload: []byte{0x01}})
	if err != nil {
		t.Fatalf("NextUplink: %v", err)
	}
	st.gw.send(PushData, pushData(up, 868.1, "SF7BW125", 5_000_000))
	st.gw.read() // PUSH_ACK
	readDownlink(t, st.gw)
	// The network answers in both windows, so the same frame is also scheduled in
	// RX2. That is not a duplicate delivery: both copies carry the same FCntDown,
	// and a LoRaWAN device discards a frame whose downlink counter it has already
	// seen, so the application payload is handed up once.
	readDownlink(t, st.gw)

	// A second uplink with nothing queued must produce no downlink at all.
	up2, err := st.dev.NextUplink(sim.UplinkOptions{FPort: 1, Payload: []byte{0x02}})
	if err != nil {
		t.Fatalf("NextUplink: %v", err)
	}
	st.gw.send(PushData, pushData(up2, 868.3, "SF7BW125", 20_000_000))
	st.gw.read() // PUSH_ACK

	// The read deadline makes "nothing arrives" observable rather than a hang.
	if err := st.gw.conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond)); err != nil {
		t.Fatalf("set a read deadline: %v", err)
	}
	buf := make([]byte, 4096)
	n, _, err := st.gw.conn.ReadFrom(buf)
	if err == nil {
		h, perr := ParseHeader(buf[:n])
		t.Errorf("an unexpected frame arrived after the downlink was delivered: %d bytes (%v, %v)",
			n, h.Type, perr)
	}
}
