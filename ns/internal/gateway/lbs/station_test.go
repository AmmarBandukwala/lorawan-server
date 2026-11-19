package lbs

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/gateway"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
)

// fakeStation is a LoRa Basics Station in test form: a WebSocket client that
// speaks the protocol in the order a real station does.
type fakeStation struct {
	t    *testing.T
	conn *websocket.Conn
	eui  eui
}

func dialStation(t *testing.T, addr string, id eui, header http.Header) *fakeStation {
	t.Helper()
	url := "ws://" + addr + "/connectedtcp/" + id.String()
	conn, resp, err := websocket.DefaultDialer.Dial(url, header)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial %s: %v (status %s)", url, err, resp.Status)
		}
		t.Fatalf("dial %s: %v", url, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	st := &fakeStation{t: t, conn: conn, eui: id}
	conn.SetPongHandler(func(string) error { return nil })
	return st
}

func (st *fakeStation) send(v any) {
	st.t.Helper()
	if err := st.conn.WriteJSON(v); err != nil {
		st.t.Fatalf("write to the server: %v", err)
	}
}

// read returns the next message's msgtype and its raw body.
func (st *fakeStation) read() (string, json.RawMessage) {
	st.t.Helper()
	if err := st.conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		st.t.Fatalf("set a read deadline: %v", err)
	}
	_, payload, err := st.conn.ReadMessage()
	if err != nil {
		st.t.Fatalf("read from the server: %v", err)
	}
	var envelope struct {
		Type string `json:"msgtype"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		st.t.Fatalf("cannot parse a server message %s: %v", payload, err)
	}
	return envelope.Type, payload
}

// readDownlink waits for the next dnmsg, skipping anything else the server
// sends in between — a timesync answer, for instance.
func (st *fakeStation) readDownlink() downlink {
	st.t.Helper()
	for {
		typ, payload := st.read()
		if typ != msgDownlink {
			continue
		}
		var dn downlink
		if err := json.Unmarshal(payload, &dn); err != nil {
			st.t.Fatalf("cannot parse a dnmsg %s: %v", payload, err)
		}
		return dn
	}
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func mustParse[T any](t *testing.T, s string, parse func(string) (T, error)) T {
	t.Helper()
	v, err := parse(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return v
}

const (
	testGwEUI   = "AA55555555555555"
	testDevEUI  = "0011223344556677"
	testJoinEUI = "0102030405060708"
	testAppKey  = "2b7e151628aed2a6abf7158809cf4f3c"
	testNetID   = "010203"
)

// stationTest wires a real engine to a real LNS server on a real socket. Nothing
// between the device frame and the dnmsg is faked, which is the point.
func stationTest(t *testing.T, mutate func(*Config)) (*Server, *engine.Engine, *engine.MemStore) {
	t.Helper()

	store := engine.NewMemStore()
	devEUI := mustParse(t, testDevEUI, lorawan.ParseEUI64)
	joinEUI := mustParse(t, testJoinEUI, lorawan.ParseEUI64)
	appKey := mustParse(t, testAppKey, lorawan.ParseAES128Key)
	netID := mustParse(t, testNetID, lorawan.ParseNetID)
	if err := store.SaveDevice(context.Background(), engine.NewOTAADevice(devEUI, joinEUI, appKey)); err != nil {
		t.Fatalf("SaveDevice: %v", err)
	}

	engCfg := engine.DefaultConfig()
	engCfg.NetID = netID
	engCfg.Region = "EU868"

	lazy := &gateway.Lazy{}
	eng, err := engine.New(engCfg, store.Store(), lazy,
		engine.WithLogger(quietLogger()),
		// Transmit inline so the dnmsg is on the wire once HandleUplink returns,
		// which is what makes the assertions below deterministic.
		engine.WithScheduler(func(_ time.Duration, fn func()) { fn() }),
	)
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}

	lbsCfg := Config{
		Addr:    "127.0.0.1:0",
		Region:  "EU868",
		NetID:   [3]byte(netID),
		Handler: eng,
		Logger:  quietLogger(),
	}
	if mutate != nil {
		mutate(&lbsCfg)
	}
	srv, err := New(lbsCfg)
	if err != nil {
		t.Fatalf("lbs.New: %v", err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	lazy.Set(srv)
	return srv, eng, store
}

// TestStationProtocolLabel pins how a station's reported revision is presented.
// A station that never sent a protocol number must not read as revision 0: some
// firmwares omit the field, and "v0" would send an operator looking for a
// protocol that does not exist.
func TestStationProtocolLabel(t *testing.T) {
	for _, tc := range []struct {
		protocol int
		want     string
	}{
		{2, "basic-station v2"},
		{1, "basic-station v1"},
		{0, "basic-station"},
		{-1, "basic-station"},
	} {
		if got := stationProtocol(stationInfo{Protocol: tc.protocol}); got != tc.want {
			t.Errorf("protocol %d = %q, want %q", tc.protocol, got, tc.want)
		}
	}
}

// TestStationHandshakeReceivesRouterConfig covers the gate every station must
// pass before it will carry traffic: it sends version and expects a
// router_config describing this network.
func TestStationHandshakeReceivesRouterConfig(t *testing.T) {
	srv, _, _ := stationTest(t, nil)
	st := dialStation(t, srv.ListenAddr(), mustEUI(t, testGwEUI), nil)

	st.send(map[string]any{
		"msgtype":  "version",
		"station":  "2.0.5",
		"firmware": "1.0.0",
		"model":    "MTAC-036-01",
		"protocol": 2,
	})
	typ, payload := st.read()
	if typ != msgRouterConfig {
		t.Fatalf("first reply = %q, want router_config", typ)
	}

	var cfg routerConfig
	if err := json.Unmarshal(payload, &cfg); err != nil {
		t.Fatalf("cannot parse the router config: %v", err)
	}
	if cfg.Region != "EU868" {
		t.Errorf("region = %q, want EU868", cfg.Region)
	}
	if len(cfg.NetID) != 3 || cfg.NetID[0] != 0x01 || cfg.NetID[1] != 0x02 || cfg.NetID[2] != 0x03 {
		t.Errorf("NetID = %v, want [1 2 3]", cfg.NetID)
	}
	// The station must be told the RX2 parameters this server answers on.
	// Silence here is the "uplinks fine, RX2 never works" fault.
	if cfg.RX2.Frequency != 869525000 {
		t.Errorf("rx2 freq = %d, want 869525000", cfg.RX2.Frequency)
	}
	// rx1dl is the band's default RX1 delay, which for EU868 is 1 s (RP002
	// PDN_RX1_DELAY). It is not the 5 s a join-accept uses: that delay belongs
	// to the individual downlink and travels in each dnmsg's RxDelay field, which
	// the join test asserts. Sending 5 here would push every ordinary downlink a
	// second late and the device would have closed RX1 by then.
	if cfg.RX1Delay != 1 {
		t.Errorf("rx1dl = %d, want the EU868 default of 1", cfg.RX1Delay)
	}
	if len(cfg.DataRates) == 0 {
		t.Error("the DR table must be sent; a station without one cannot decode")
	}
	// No radio plan unless the operator asked for one.
	if cfg.RadioPlan != nil {
		t.Error("sx1301_conf must be omitted unless a radio plan is configured")
	}

	// The handshake must also register the gateway for the status endpoint.
	presence := srv.Presence()
	if len(presence) != 1 {
		t.Fatalf("Presence() = %d gateways, want 1", len(presence))
	}
	if presence[0].Transport != transportName {
		t.Errorf("transport = %q, want %q", presence[0].Transport, transportName)
	}
	if presence[0].Model != "MTAC-036-01" || presence[0].Firmware != "1.0.0" {
		t.Errorf("station info = %+v, want the reported model and firmware", presence[0])
	}
	// The protocol revision the station reported has to survive into presence,
	// shaped like the UDP transport's "semtech-udp v2". Reporting the transport
	// name twice instead tells an operator nothing about what the station speaks.
	if presence[0].Protocol != "basic-station v2" {
		t.Errorf("protocol = %q, want %q", presence[0].Protocol, "basic-station v2")
	}
	if !srv.CanReach(lorawanEUI(mustEUI(t, testGwEUI))) {
		t.Error("a connected station must report itself reachable")
	}
}
