package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/gateway"
	"github.com/fieldnode/lorawan-server/ns/internal/integration"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
	"github.com/fieldnode/lorawan-server/ns/internal/sim"
)

// captureGW records the downlinks the engine scheduled, so a test can hand a
// join-accept to the simulated device without binding a radio port.
type captureGW struct {
	mu     sync.Mutex
	frames []gateway.DownlinkFrame
}

func (c *captureGW) SendDownlink(_ context.Context, f gateway.DownlinkFrame) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.frames = append(c.frames, f)
	return nil
}

func (c *captureGW) next() (gateway.DownlinkFrame, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.frames) == 0 {
		return gateway.DownlinkFrame{}, false
	}
	f := c.frames[0]
	c.frames = c.frames[1:]
	return f, true
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// received is one delivery the webhook endpoint accepted.
type received struct {
	body map[string]any
	auth string
}

func mustAppKey(t *testing.T, s string) lorawan.AES128Key {
	t.Helper()
	k, err := lorawan.ParseAES128Key(s)
	if err != nil {
		t.Fatalf("ParseAES128Key(%q): %v", s, err)
	}
	return k
}

func mustNetworkID(t *testing.T, s string) lorawan.NetID {
	t.Helper()
	n, err := lorawan.ParseNetID(s)
	if err != nil {
		t.Fatalf("ParseNetID(%q): %v", s, err)
	}
	return n
}

// TestUplinkReachesAConfiguredWebhook is the feature's actual promise, end to end:
// a device joins, reports, and a configured HTTP destination receives the reading
// as JSON.
//
// Every layer is the real one — the engine, the dispatcher, the JavaScript
// formatter, the webhook sink, an actual HTTP POST to an actual listener. Only the
// radio is simulated. This is the seam a package test cannot cover: the recorder is
// assembled here in main.go rather than inside any of the packages, so a wiring
// mistake leaves every component green while nothing is ever delivered.
func TestUplinkReachesAConfiguredWebhook(t *testing.T) {
	in := make(chan received, 8)

	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("the webhook received a body that is not JSON: %v", err)
		}
		in <- received{body: body, auth: r.Header.Get("Authorization")}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer hook.Close()

	ctx := context.Background()
	mem := engine.NewMemStore()
	store := mem.Store()

	devEUI := mustEUI(t, "0011223344556677")
	joinEUI := mustEUI(t, "0102030405060708")
	gwEUI := mustEUI(t, "aa55555555555555")
	appKey := mustAppKey(t, "2b7e151628aed2a6abf7158809cf4f3c")
	netID := mustNetworkID(t, "010203")

	// A model with a formatter, because the promise is a decoded reading rather
	// than base64 bytes.
	if err := store.Models.SaveModel(ctx, &engine.DeviceModel{
		ModelID:   "probe",
		Name:      "Probe",
		CodecKind: "JS",
		CodecScript: "function decodeUplink(input) {" +
			"return { data: { temperature: input.bytes[0] } }; }",
	}); err != nil {
		t.Fatalf("SaveModel: %v", err)
	}

	dev := engine.NewOTAADevice(devEUI, joinEUI, appKey)
	dev.ModelID = "probe"
	if err := store.Devices.SaveDevice(ctx, dev); err != nil {
		t.Fatalf("SaveDevice: %v", err)
	}

	// The real assembly path from main.go, with the test server as the endpoint.
	cfg := config{
		region:         "EU868",
		webhookURLs:    []string{hook.URL},
		webhookToken:   "hook-token",
		codecTimeout:   2 * time.Second,
		codecExecution: time.Second,
	}
	recorder, dispatcher, _, closeDispatcher, err := newRecorder(store, nil, cfg, quietLog())
	if err != nil {
		t.Fatalf("newRecorder: %v", err)
	}
	defer closeDispatcher()
	if dispatcher == nil {
		t.Fatal("no dispatcher was built, so nothing could be delivered")
	}

	engCfg := engine.DefaultConfig()
	engCfg.NetID = netID
	engCfg.Region = "EU868"
	transmitter := &captureGW{}
	eng, err := engine.New(engCfg, recorder, transmitter,
		engine.WithLogger(quietLog()),
		// Run the RX-window callbacks inline. EU868 answers a join in 5 and 6
		// seconds, so with real timers the join-accept would land just as the
		// test's own deadline expires.
		engine.WithScheduler(func(_ time.Duration, fn func()) { fn() }))
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}

	rx := func(payload []byte, freq uint32, dr int) error {
		return eng.HandleUplink(ctx, gateway.UplinkFrame{
			GatewayID: gwEUI, RFRegion: "EU868", FrequencyHz: freq,
			DataRateIndex: dr, RxWindow: gateway.RxWindow1,
			UplinkID:   [8]byte{0, 1, 0, 0, 0x12, 0x34, 0x56, 0x78},
			PHYPayload: payload, RSSI: -70, SNR: 9,
			ReceivedAt: time.Now().UTC(),
		})
	}

	simDev := sim.New(devEUI, joinEUI, appKey, netID)
	join, err := simDev.NextJoinRequest()
	if err != nil {
		t.Fatalf("NextJoinRequest: %v", err)
	}
	if err := rx(join, 868100000, 0); err != nil {
		t.Fatalf("join-request: %v", err)
	}

	// The join-accept is scheduled on the RX timers, so wait for one to appear and
	// hand it to the device. Without this the device has no keys and cannot build
	// a frame the server would accept.
	deadline := time.Now().Add(5 * time.Second)
	var joined bool
	for time.Now().Before(deadline) && !joined {
		if frame, ok := transmitter.next(); ok {
			if err := simDev.HandleJoinAccept(frame.PHYPayload); err == nil {
				joined = true
			}
		} else {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if !joined {
		t.Fatal("the device never received a join-accept it could decrypt")
	}

	frame, err := simDev.NextUplink(sim.UplinkOptions{FPort: 10, Payload: []byte{21, 44}, ADR: true})
	if err != nil {
		t.Fatalf("NextUplink: %v", err)
	}
	if err := rx(frame, 868100000, 5); err != nil {
		t.Fatalf("data uplink: %v", err)
	}

	// The join itself is an event too, and a subscriber receives it, so wait for
	// the data uplink rather than assuming the first delivery is the one under test.
	got, ok := awaitUplink(in, 5*time.Second)
	if !ok {
		t.Fatalf("no uplink event reached the webhook; dispatcher stats = %+v", dispatcher.Stats())
	}

	if got.auth != "Bearer hook-token" {
		t.Errorf("Authorization = %q, want the configured bearer token", got.auth)
	}
	if got.body["dev_eui"] != "0011223344556677" {
		t.Errorf("dev_eui = %v", got.body["dev_eui"])
	}
	// The reading, not merely the bytes: this only appears if the formatter ran and
	// its result rode through the dispatcher to the sink.
	decoded, isMap := got.body["decoded"].(map[string]any)
	if !isMap {
		t.Fatalf("the event carries no decoded reading: %v", got.body)
	}
	if temp, _ := decoded["temperature"].(float64); temp != 21 {
		t.Errorf("decoded temperature = %v, want 21", decoded["temperature"])
	}
	// The raw payload ships too, so a wrong formatter is debuggable from the
	// receiving side without coming back to the console.
	if data, _ := got.body["data"].(string); data == "" {
		t.Error("the raw payload must ship alongside the reading")
	}
	// Radio metadata, so a receiver can chart link quality without asking.
	if got.body["rssi"] == nil || got.body["snr"] == nil {
		t.Errorf("the event lost its radio metadata: %v", got.body)
	}

	// Delivery counters are bumped after Publish returns, so reading them the
	// moment an event is observed at the endpoint races with the increment.
	// Poll instead of sleeping a guessed amount.
	waitForStats(t, dispatcher, func(s integration.Stats) bool { return s.Published >= 2 },
		5*time.Second)
	if st := dispatcher.Stats(); st.Failed != 0 || st.Dropped != 0 {
		t.Errorf("dispatcher stats = %+v, want the join and the uplink published with no failures", st)
	}
}

// waitForStats polls the dispatcher's counters until cond holds.
func waitForStats(t *testing.T, d *integration.Dispatcher, cond func(integration.Stats) bool, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond(d.Stats()) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("dispatcher counters never reached the expected state; last = %+v", d.Stats())
}

// awaitUplink returns the first uplink-kind event, skipping the others.
func awaitUplink(in chan received, within time.Duration) (received, bool) {
	deadline := time.After(within)
	for {
		select {
		case ev := <-in:
			if ev.body["kind"] == "uplink" {
				return ev, true
			}
		case <-deadline:
			return received{}, false
		}
	}
}

// TestNothingLeavesTheProcessWithoutAnEndpoint: configuring no destination must
// mean no traffic leaves, whatever else the dispatcher is doing.
//
// A dispatcher is still built here, and that is correct rather than wasteful: the
// in-memory store can hold device models, and decoding happens for the console's
// own readings view even when nobody is subscribed. What matters is that it has no
// sinks to fan out to, and that no supervisor exists to dial anything.
func TestNothingLeavesTheProcessWithoutAnEndpoint(t *testing.T) {
	store := engine.NewMemStore().Store()
	recorder, dispatcher, supervisor, closeFn, err := newRecorder(store, nil, config{region: "EU868"}, quietLog())
	if err != nil {
		t.Fatalf("newRecorder: %v", err)
	}
	defer closeFn()

	if supervisor != nil {
		t.Error("a supervisor was built with nowhere to store a destination")
	}
	if dispatcher != nil && len(dispatcher.Sinks()) != 0 {
		t.Errorf("sinks = %v, want none: with no endpoint configured nothing may leave",
			dispatcher.Sinks())
	}
	if recorder.Recorder == nil {
		t.Error("the recorder was dropped, so nothing would be recorded at all")
	}
}

// TestNoDispatcherAtAllWhenNothingCouldUseOne: a store that can neither decode nor
// store a destination, with no endpoint, gets no goroutine, no channel and no
// indirection on the radio path.
func TestNoDispatcherAtAllWhenNothingCouldUseOne(t *testing.T) {
	// A Store value with only a recorder: no model repository to decode against.
	full := engine.NewMemStore().Store()
	store := engine.Store{Recorder: full.Recorder}
	_, dispatcher, supervisor, closeFn, err := newRecorder(store, nil, config{region: "EU868"}, quietLog())
	if err != nil {
		t.Fatalf("newRecorder: %v", err)
	}
	defer closeFn()

	if dispatcher != nil {
		t.Error("a dispatcher was built with no endpoint, no models and nowhere to store one")
	}
	if supervisor != nil {
		t.Error("a supervisor was built with nowhere to store a destination")
	}
}
