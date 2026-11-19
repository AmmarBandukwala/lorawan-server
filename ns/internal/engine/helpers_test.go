// In-package test helpers for the engine's white-box tests.
//
// Why this exists next to ns/test/testenv, which does the same job: a Go
// package's own tests cannot import anything that imports that package, so a
// shared out-of-package harness is unreachable from here. The tests that remain
// in this package need it — the duty-cycle tests reach engine.budget to
// pre-fill a sub-band's airtime ledger, which there is deliberately no exported
// way to do because fabricating radio history in a running server would be a way
// to take a gateway down.
//
// Everything else moved to ns/test/scenarios and drives the exported API. When
// adding a test here, ask first whether it can be written from outside; only
// reach for this file when the behaviour is genuinely internal.
package engine

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/gateway"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
	"github.com/fieldnode/lorawan-server/ns/internal/sim"
)

// --- test doubles -----------------------------------------------------------

// fakeGateway records downlinks instead of transmitting them. Combined with
// WithScheduler (immediate execution) a test can assert on the exact frames the
// engine scheduled without sleeping.
type fakeGateway struct {
	mu   sync.Mutex
	sent []gateway.DownlinkFrame
	err  error
}

func (g *fakeGateway) SendDownlink(_ context.Context, f gateway.DownlinkFrame) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.err != nil {
		return g.err
	}
	g.sent = append(g.sent, f)
	return nil
}

func (g *fakeGateway) frames() []gateway.DownlinkFrame {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]gateway.DownlinkFrame(nil), g.sent...)
}

func (g *fakeGateway) reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sent = nil
}

// fakeClock lets tests move time forward to expire the de-duplication window.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// --- fixtures ---------------------------------------------------------------

const (
	testDevEUI  = "0011223344556677"
	testJoinEUI = "0102030405060708"
	testAppKey  = "2b7e151628aed2a6abf7158809cf4f3c"
	testGwEUI   = "aa55555555555555"
	// EU868 uplink channel 0, SF7BW125 (DR5).
	eu868Ch0Hz = uint32(868100000)
	eu868DR5   = 5
)

type testEnv struct {
	t     *testing.T
	eng   *Engine
	store *MemStore
	gw    *fakeGateway
	clk   *fakeClock
}

func newTestEnv(t *testing.T, mutate func(*Config)) *testEnv {
	t.Helper()

	cfg := DefaultConfig()
	cfg.NetID = mustNetID(t, "010203")
	cfg.Region = "EU868"
	if mutate != nil {
		mutate(&cfg)
	}

	clk := &fakeClock{t: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)}
	store := NewMemStore()
	gw := &fakeGateway{}

	eng, err := New(cfg, store.Store(), gw,
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		WithClock(clk.now),
		// Run scheduled transmissions inline so assertions are deterministic.
		WithScheduler(func(_ time.Duration, fn func()) { fn() }),
	)
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	return &testEnv{t: t, eng: eng, store: store, gw: gw, clk: clk}
}

// newTestEnvWithBooks builds an engine wired to shared, durable books. It is how
// a restart is simulated: build one engine, flush its books, then build a second
// engine against the same objects and restore.
//
// The clock is passed through rather than created fresh so both engines agree on
// what "now" is; a restart test that let time move would be measuring the window
// rather than the persistence.
func newTestEnvWithBooks(t *testing.T, books *fakeBooks, clk *fakeClock,
	mutate func(*Config)) *testEnv {

	t.Helper()

	cfg := DefaultConfig()
	cfg.NetID = mustNetID(t, "010203")
	cfg.Region = "EU868"
	if mutate != nil {
		mutate(&cfg)
	}
	if clk == nil {
		clk = &fakeClock{t: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)}
	}

	store := NewMemStore()
	st := store.Store()
	st.Airtime = books
	st.Seen = books

	eng, err := New(cfg, st, &fakeGateway{},
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		WithClock(clk.now),
		WithScheduler(func(_ time.Duration, fn func()) { fn() }),
	)
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	return &testEnv{t: t, eng: eng, store: store, gw: eng.gw.(*fakeGateway), clk: clk}
}

// fakeBooks is the durable layer in test form: a de-duplication book and an
// airtime ledger that outlive any one engine. Handing the same value to two
// engines is what makes "the process restarted" testable, as opposed to
// "the in-memory map was still there", which is the thing every single-instance
// test quietly assumes.
//
// It decides what has aged out using the engine's clock, not the wall clock. A
// ledger that reads time.Now() while the engine runs on a fake clock disagrees
// with it about what "recently" means, and the failure is the worst kind: the
// test passes in the hour it was written and starts failing once the fake
// timestamp falls behind real time by more than the window.
type fakeBooks struct {
	mu      sync.Mutex
	now     func() time.Time
	seen    map[string]time.Time
	records []AirtimeRecord
}

func newFakeBooks() *fakeBooks {
	return &fakeBooks{seen: map[string]time.Time{}, now: time.Now}
}

// withClock points the books at an engine's clock. It returns the receiver so a
// test can build and wire it in one expression.
func (b *fakeBooks) withClock(now func() time.Time) *fakeBooks {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.now = now
	return b
}

func (b *fakeBooks) ClaimFrame(_ context.Context, key string, window time.Duration) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	if at, ok := b.seen[key]; ok && now.Sub(at) < window {
		return false, nil
	}
	b.seen[key] = now
	return true, nil
}

func (b *fakeBooks) SaveAirtime(_ context.Context, records []AirtimeRecord,
	window time.Duration) error {

	b.mu.Lock()
	defer b.mu.Unlock()

	cutoff := b.now().Add(-window)
	b.records = b.records[:0]
	for _, r := range records {
		if !r.Start.Before(cutoff) {
			b.records = append(b.records, r)
		}
	}
	return nil
}

func (b *fakeBooks) LoadAirtime(_ context.Context, window time.Duration) ([]AirtimeRecord, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	cutoff := b.now().Add(-window)
	var out []AirtimeRecord
	for _, r := range b.records {
		if !r.Start.Before(cutoff) {
			out = append(out, r)
		}
	}
	return out, nil
}

// flush writes an engine's current books, standing in for the periodic persister.
func (b *fakeBooks) flush(t *testing.T, eng *Engine) {
	t.Helper()
	eng.PruneAirtime()
	if err := b.SaveAirtime(context.Background(), eng.AirtimeSnapshot(), eng.AirtimeWindow()); err != nil {
		t.Fatalf("flush: %v", err)
	}
}

// booked is the airtime the book holds for one gateway and sub-band.
func (b *fakeBooks) booked(gateway lorawan.EUI64, minHz uint32) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()

	var total time.Duration
	for _, r := range b.records {
		if r.Gateway == gateway && r.MinHz == minHz {
			total += r.Airtime
		}
	}
	return total
}

func mustNetID(t *testing.T, s string) lorawan.NetID {
	t.Helper()
	n, err := lorawan.ParseNetID(s)
	if err != nil {
		t.Fatalf("ParseNetID(%q): %v", s, err)
	}
	return n
}

func mustEUI64(t *testing.T, s string) lorawan.EUI64 {
	t.Helper()
	e, err := lorawan.ParseEUI64(s)
	if err != nil {
		t.Fatalf("ParseEUI64(%q): %v", s, err)
	}
	return e
}

func mustKey(t *testing.T, s string) lorawan.AES128Key {
	t.Helper()
	k, err := lorawan.ParseAES128Key(s)
	if err != nil {
		t.Fatalf("ParseAES128Key(%q): %v", s, err)
	}
	return k
}

func mustDevAddr(t *testing.T, s string) lorawan.DevAddr {
	t.Helper()
	a, err := lorawan.ParseDevAddr(s)
	if err != nil {
		t.Fatalf("ParseDevAddr(%q): %v", s, err)
	}
	return a
}

func mustEUI64Static(s string) lorawan.EUI64 {
	e, _ := lorawan.ParseEUI64(s)
	return e
}

func uplinkFrame(wire []byte, window gateway.RxWindow, freqHz uint32, dr int) gateway.UplinkFrame {
	return gateway.UplinkFrame{
		GatewayID:     mustEUI64Static(testGwEUI),
		RFRegion:      "EU868",
		RxWindow:      window,
		FrequencyHz:   freqHz,
		DataRateIndex: dr,
		UplinkID:      [8]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08},
		PHYPayload:    wire,
		RSSI:          -72,
		SNR:           8.5,
	}
}

// provision stores an OTAA (otaa=true) or ABP device.
func (env *testEnv) provision(otaa bool) *Device {
	env.t.Helper()

	devEUI := mustEUI64(env.t, testDevEUI)
	joinEUI := mustEUI64(env.t, testJoinEUI)

	var dev *Device
	if otaa {
		dev = NewOTAADevice(devEUI, joinEUI, mustKey(env.t, testAppKey))
	} else {
		dev = NewABPDevice(devEUI, joinEUI, mustDevAddr(env.t, "01020304"),
			mustKey(env.t, "00112233445566778899aabbccddeeff"),
			mustKey(env.t, "ffeeddccbbaa99887766554433221100"))
	}
	if err := env.store.SaveDevice(context.Background(), dev); err != nil {
		env.t.Fatalf("SaveDevice: %v", err)
	}
	return dev
}

// join runs a complete OTAA activation and returns the simulated device plus
// the frames the engine scheduled for the join-accept.
func (env *testEnv) join() (*sim.Device, []gateway.DownlinkFrame) {
	env.t.Helper()

	env.provision(true)
	simDev := sim.New(mustEUI64(env.t, testDevEUI), mustEUI64(env.t, testJoinEUI),
		mustKey(env.t, testAppKey), env.eng.Config().NetID)

	wire, err := simDev.NextJoinRequest()
	if err != nil {
		env.t.Fatalf("NextJoinRequest: %v", err)
	}
	if err := env.eng.HandleUplink(context.Background(),
		uplinkFrame(wire, gateway.RxWindow1, eu868Ch0Hz, eu868DR5)); err != nil {
		env.t.Fatalf("HandleUplink(join): %v", err)
	}

	frames := env.gw.frames()
	if len(frames) == 0 {
		env.t.Fatalf("no join-accept scheduled; events: %+v", env.store.Events())
	}
	if err := simDev.HandleJoinAccept(frames[0].PHYPayload); err != nil {
		env.t.Fatalf("HandleJoinAccept: %v", err)
	}
	return simDev, frames
}

// uplink sends a frame built by the simulated device through the engine.
func (env *testEnv) uplink(simDev *sim.Device, o sim.UplinkOptions, window gateway.RxWindow) {
	env.t.Helper()

	wire, err := simDev.NextUplink(o)
	if err != nil {
		env.t.Fatalf("NextUplink: %v", err)
	}
	if err := env.eng.HandleUplink(context.Background(),
		uplinkFrame(wire, window, eu868Ch0Hz, eu868DR5)); err != nil {
		env.t.Fatalf("HandleUplink: %v", err)
	}
}

// uplinkWire feeds raw bytes, for frames a well-behaved device would not build
// (replays, foreign DevAddr).
func (env *testEnv) uplinkWire(wire []byte) error {
	env.t.Helper()
	return env.eng.HandleUplink(context.Background(),
		uplinkFrame(wire, gateway.RxWindow1, eu868Ch0Hz, eu868DR5))
}
