// Package testenv drives the network server the way a device would.
//
// It exists so tests outside internal/engine can be written against the
// exported API without each one rebuilding the same scaffolding: an engine with
// an in-memory store, a gateway that records instead of transmitting, a clock
// that only moves when a test says so, and a simulated device that produces
// spec-correct frames.
//
// The harness has to live outside internal/engine for a structural reason, not
// a stylistic one: a package's own tests cannot import anything that imports
// that package, so a shared harness must be a separate package, and a separate
// package can only use exported API. That constraint is the point — if a
// scenario cannot be expressed through the exported surface, that is worth
// knowing.
//
// Tests that must reach inside the engine (the ADR arithmetic, the airtime
// budget) stay colocated in internal/engine and keep their own small helpers.
package testenv

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/gateway"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
	"github.com/fieldnode/lorawan-server/ns/internal/region"
	"github.com/fieldnode/lorawan-server/ns/internal/sim"
)

// Fixtures shared by every scenario, so a failure message names the same device
// in every test and events from one test cannot be mistaken for another's.
const (
	DevEUI     = "0011223344556677"
	JoinEUI    = "0102030405060708"
	AppKey     = "2b7e151628aed2a6abf7158809cf4f3c"
	GatewayEUI = "aa55555555555555"
	// EU868 uplink channel 0 at SF7BW125, the most common test setting.
	EU868Ch0Hz = uint32(868100000)
	EU868DR5   = 5
)

// Gateway records downlinks instead of transmitting them. Paired with
// engine.WithScheduler (which runs scheduled work inline), a test can assert on
// the exact frames the engine scheduled without sleeping.
type Gateway struct {
	mu   sync.Mutex
	sent []gateway.DownlinkFrame
	// err makes the transport fail, to test what the engine does when a
	// gateway refuses a frame.
	err error
}

// SendDownlink implements gateway.Gateway.
func (g *Gateway) SendDownlink(_ context.Context, f gateway.DownlinkFrame) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.err != nil {
		return g.err
	}
	g.sent = append(g.sent, f)
	return nil
}

// Frames returns everything transmitted so far, oldest first.
func (g *Gateway) Frames() []gateway.DownlinkFrame {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]gateway.DownlinkFrame(nil), g.sent...)
}

// LastFrame is the most recent transmission, or nil.
func (g *Gateway) LastFrame() *gateway.DownlinkFrame {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.sent) == 0 {
		return nil
	}
	f := g.sent[len(g.sent)-1]
	return &f
}

// Reset forgets the recorded frames, so a test can assert on what one uplink
// caused rather than on a growing history.
func (g *Gateway) Reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sent = nil
}

// FailWith makes every later transmission return err. Pass nil to recover.
func (g *Gateway) FailWith(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.err = err
}

// Clock is time a test controls. The engine only ever reads it through the
// injected function, so nothing drifts when a machine is slow.
type Clock struct {
	mu sync.Mutex
	t  time.Time
}

// Now implements the engine's clock injection.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Advance moves time forward, which is how a test expires the de-duplication
// window or reaches a scheduled RX window.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// Env is one engine instance with everything around it stubbed out.
type Env struct {
	t      *testing.T
	Engine *engine.Engine
	Store  *engine.MemStore
	GW     *Gateway
	Clock  *Clock
}

// New builds an engine with the default configuration, EU868 and a fixed clock.
// mutate adjusts the config before the engine is built; pass nil for defaults.
func New(t *testing.T, mutate func(*engine.Config)) *Env {
	t.Helper()

	cfg := engine.DefaultConfig()
	cfg.NetID = MustNetID(t, "010203")
	cfg.Region = "EU868"
	if mutate != nil {
		mutate(&cfg)
	}

	clk := &Clock{t: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)}
	store := engine.NewMemStore()
	gw := &Gateway{}

	eng, err := engine.New(cfg, store.Store(), gw,
		engine.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		engine.WithClock(clk.Now),
		// Run scheduled transmissions inline so assertions are deterministic.
		engine.WithScheduler(func(_ time.Duration, fn func()) { fn() }),
	)
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	return &Env{t: t, Engine: eng, Store: store, GW: gw, Clock: clk}
}

// Provision stores an OTAA (otaa=true) or ABP device and returns it.
func (env *Env) Provision(otaa bool) *engine.Device {
	env.t.Helper()

	devEUI := MustEUI64(env.t, DevEUI)
	joinEUI := MustEUI64(env.t, JoinEUI)

	var dev *engine.Device
	if otaa {
		dev = engine.NewOTAADevice(devEUI, joinEUI, MustKey(env.t, AppKey))
	} else {
		dev = engine.NewABPDevice(devEUI, joinEUI, MustDevAddr(env.t, "01020304"),
			MustKey(env.t, "00112233445566778899aabbccddeeff"),
			MustKey(env.t, "ffeeddccbbaa99887766554433221100"))
	}
	if err := env.Store.SaveDevice(context.Background(), dev); err != nil {
		env.t.Fatalf("SaveDevice: %v", err)
	}
	return dev
}

// Join runs a complete OTAA activation and returns the simulated device plus
// the frames the engine scheduled for the join-accept.
func (env *Env) Join() (*sim.Device, []gateway.DownlinkFrame) {
	env.t.Helper()

	env.Provision(true)
	simDev := sim.New(MustEUI64(env.t, DevEUI), MustEUI64(env.t, JoinEUI),
		MustKey(env.t, AppKey), env.Engine.Config().NetID)

	wire, err := simDev.NextJoinRequest()
	if err != nil {
		env.t.Fatalf("NextJoinRequest: %v", err)
	}
	if err := env.Engine.HandleUplink(context.Background(),
		UplinkFrame(wire, gateway.RxWindow1, EU868Ch0Hz, EU868DR5)); err != nil {
		env.t.Fatalf("HandleUplink(join): %v", err)
	}

	frames := env.GW.Frames()
	if len(frames) == 0 {
		env.t.Fatalf("no join-accept scheduled; events: %+v", env.Store.Events())
	}
	if err := simDev.HandleJoinAccept(frames[0].PHYPayload); err != nil {
		env.t.Fatalf("HandleJoinAccept: %v", err)
	}
	return simDev, frames
}

// Uplink sends a frame built by the simulated device through the engine.
func (env *Env) Uplink(simDev *sim.Device, o sim.UplinkOptions, window gateway.RxWindow) {
	env.t.Helper()

	wire, err := simDev.NextUplink(o)
	if err != nil {
		env.t.Fatalf("NextUplink: %v", err)
	}
	if err := env.Engine.HandleUplink(context.Background(),
		UplinkFrame(wire, window, EU868Ch0Hz, EU868DR5)); err != nil {
		env.t.Fatalf("HandleUplink: %v", err)
	}
}

// UplinkWire feeds raw bytes, for frames a well-behaved device would not build:
// replays, a foreign DevAddr, a corrupted MIC.
func (env *Env) UplinkWire(wire []byte) error {
	env.t.Helper()
	return env.Engine.HandleUplink(context.Background(),
		UplinkFrame(wire, gateway.RxWindow1, EU868Ch0Hz, EU868DR5))
}

// Session reads back a device's session.
func (env *Env) Session(devEUI lorawan.EUI64) *engine.Session {
	env.t.Helper()

	ses, err := env.Store.SessionByDevEUI(context.Background(), devEUI)
	if err != nil {
		env.t.Fatalf("SessionByDevEUI: %v", err)
	}
	return ses
}

// UplinkSNR feeds a device frame with a chosen signal-to-noise ratio. SNR is the
// only measurement adaptive data rate responds to, so a test that wants to move
// a device's data rate has to be able to set it.
func (env *Env) UplinkSNR(simDev *sim.Device, o sim.UplinkOptions, snr float64) {
	env.t.Helper()

	wire, err := simDev.NextUplink(o)
	if err != nil {
		env.t.Fatalf("NextUplink: %v", err)
	}
	f := UplinkFrame(wire, gateway.RxWindow1, EU868Ch0Hz, EU868DR5)
	f.SNR = snr
	if err := env.Engine.HandleUplink(context.Background(), f); err != nil {
		env.t.Fatalf("HandleUplink: %v", err)
	}
}

// UplinkFrame wraps radio bytes in the metadata a gateway would report.
func UplinkFrame(wire []byte, window gateway.RxWindow, freqHz uint32, dr int) gateway.UplinkFrame {
	return gateway.UplinkFrame{
		GatewayID:     MustEUI64Static(GatewayEUI),
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

// --- parsers that fail the test instead of returning an error -----------------
//
// Every test needs these. A test that ignores a parse error is a test that has
// quietly stopped testing anything, so these stop the run.

func MustNetID(t *testing.T, s string) lorawan.NetID {
	t.Helper()
	n, err := lorawan.ParseNetID(s)
	if err != nil {
		t.Fatalf("ParseNetID(%q): %v", s, err)
	}
	return n
}

func MustEUI64(t *testing.T, s string) lorawan.EUI64 {
	t.Helper()
	e, err := lorawan.ParseEUI64(s)
	if err != nil {
		t.Fatalf("ParseEUI64(%q): %v", s, err)
	}
	return e
}

func MustKey(t *testing.T, s string) lorawan.AES128Key {
	t.Helper()
	k, err := lorawan.ParseAES128Key(s)
	if err != nil {
		t.Fatalf("ParseAES128Key(%q): %v", s, err)
	}
	return k
}

func MustDevAddr(t *testing.T, s string) lorawan.DevAddr {
	t.Helper()
	a, err := lorawan.ParseDevAddr(s)
	if err != nil {
		t.Fatalf("ParseDevAddr(%q): %v", s, err)
	}
	return a
}

// MustRegion loads a band plan, failing the test on an unknown name.
func MustRegion(t *testing.T, name string) region.Region {
	t.Helper()
	r, err := region.New(name)
	if err != nil {
		t.Fatalf("region.New(%q): %v", name, err)
	}
	return r
}

// MustEUI64Static is the form without a *testing.T, for package-level fixtures.
// It panics, which is right: a malformed constant is a bug in the test file, and
// it should surface before any assertion runs.
func MustEUI64Static(s string) lorawan.EUI64 {
	e, err := lorawan.ParseEUI64(s)
	if err != nil {
		panic("testenv: bad EUI64 constant " + s + ": " + err.Error())
	}
	return e
}
