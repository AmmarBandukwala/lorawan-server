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
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
	"github.com/fieldnode/lorawan-server/ns/internal/sim"
)

func mustKey(t *testing.T, s string) lorawan.AES128Key {
	t.Helper()
	v, err := lorawan.ParseAES128Key(s)
	if err != nil {
		t.Fatalf("ParseAES128Key(%q): %v", s, err)
	}
	return v
}

func mustNetID(t *testing.T, s string) lorawan.NetID {
	t.Helper()
	v, err := lorawan.ParseNetID(s)
	if err != nil {
		t.Fatalf("ParseNetID(%q): %v", s, err)
	}
	return v
}

// TestJoinAcceptOverRealUDP drives the whole path with no fakes in the middle:
// a simulated device's join-request leaves as a Semtech PUSH_DATA over a real
// socket, the engine authenticates it, and the join-accept comes back as a
// PULL_RESP txpk that the device can decrypt.
//
// It also pins the downlink timing. tmst is the only way a concentrator can
// place a transmission inside the device's RX window, so the uplink's
// concentrator timestamp plus the join-accept delay has to show up there.
func TestJoinAcceptOverRealUDP(t *testing.T) {
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

	// The engine needs a transport at construction and the transport needs the
	// engine as its handler, so the server is installed afterwards.
	lazy := &gateway.Lazy{}
	eng, err := engine.New(cfg, store.Store(), lazy,
		engine.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		// Transmit inline so the PULL_RESP is on the wire once HandleUplink
		// returns, which keeps the test deterministic.
		engine.WithScheduler(func(_ time.Duration, fn func()) { fn() }),
	)
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
		t.Fatalf("semtech.New: %v", err)
	}
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Close()
	lazy.Set(srv)

	gw := newFakeGateway(t, srv.ListenAddr())
	gw.send(PullData, "")
	gw.read() // PULL_ACK, and it teaches the server where the gateway lives

	dev := sim.New(devEUI, joinEUI, appKey, netID)
	wire, err := dev.NextJoinRequest()
	if err != nil {
		t.Fatalf("NextJoinRequest: %v", err)
	}

	const uplinkTMST = uint32(1000000) // 1 s after the concentrator started
	body := fmt.Sprintf(`{"rxpk":[{"tmst":%d,"freq":868.1,"rssi":-70,"lsnr":9,`+
		`"size":%d,"modu":"LORA","datr":"SF12BW125","codr":"4/5","data":"%s"}]}`,
		uplinkTMST, len(wire), hex.EncodeToString(wire))

	gw.send(PushData, body)
	if h, _ := gw.read(); h.Type != PushAck {
		t.Fatalf("first reply = %s, want PUSH_ACK", h.Type)
	}

	type txpk struct {
		Imme bool    `json:"imme"`
		TMST *uint32 `json:"tmst"`
		Freq float64 `json:"freq"`
		Datr string  `json:"datr"`
		Powe int8    `json:"powe"`
		IPol bool    `json:"ipol"`
		Size int     `json:"size"`
		Data string  `json:"data"`
	}
	readTXPK := func() txpk {
		t.Helper()
		h, b := gw.read()
		if h.Type != PullResp {
			t.Fatalf("reply = %s, want PULL_RESP", h.Type)
		}
		var wrapper struct {
			Tx txpk `json:"txpk"`
		}
		if err := json.Unmarshal(b, &wrapper); err != nil {
			t.Fatalf("decode txpk %s: %v", b, err)
		}
		return wrapper.Tx
	}

	// A join-request is answered in both windows.
	rx1 := readTXPK()
	rx2 := readTXPK()

	// EU868 join-accept timing: RX1 at 5 s, RX2 at 6 s, from the uplink stamp.
	if rx1.TMST == nil || *rx1.TMST != uplinkTMST+5000000 {
		t.Errorf("RX1 tmst = %v, want %d", rx1.TMST, uplinkTMST+5000000)
	}
	if rx2.TMST == nil || *rx2.TMST != uplinkTMST+6000000 {
		t.Errorf("RX2 tmst = %v, want %d", rx2.TMST, uplinkTMST+6000000)
	}
	if rx1.Freq != 868.1 {
		t.Errorf("RX1 freq = %v MHz, want the uplink channel 868.1", rx1.Freq)
	}
	if rx2.Freq != 869.525 {
		t.Errorf("RX2 freq = %v MHz, want the EU868 RX2 channel 869.525", rx2.Freq)
	}
	for name, tx := range map[string]txpk{"RX1": rx1, "RX2": rx2} {
		if tx.Datr != "SF12BW125" {
			t.Errorf("%s datr = %q, want SF12BW125", name, tx.Datr)
		}
		if !tx.IPol {
			t.Errorf("%s must invert the downlink polarity", name)
		}
		if tx.Powe > 16 {
			t.Errorf("%s power = %d dBm, above the EU868 gateway limit", name, tx.Powe)
		}
		if tx.Imme {
			t.Errorf("%s must be timestamped, not immediate", name)
		}
	}
	if rx1.Data != rx2.Data {
		t.Error("both windows must carry the same join-accept")
	}

	raw, err := hex.DecodeString(rx1.Data)
	if err != nil {
		t.Fatalf("txpk data is not hex: %v", err)
	}
	if len(raw) != rx1.Size {
		t.Errorf("txpk size = %d but data holds %d bytes", rx1.Size, len(raw))
	}
	if err := dev.HandleJoinAccept(raw); err != nil {
		t.Fatalf("the device rejected the join-accept that came back over UDP: %v", err)
	}

	if rec, ok := srv.reg.lookup(mustEUI(t, testGwEUI)); !ok || rec.DownlinksSent != 2 {
		t.Errorf("registry = %+v (found=%v), want two downlinks recorded", rec, ok)
	}
	if len(store.EventsOfKind(engine.EventJoinAccept)) != 1 {
		t.Errorf("the join was not recorded: %+v", store.Events())
	}
}
