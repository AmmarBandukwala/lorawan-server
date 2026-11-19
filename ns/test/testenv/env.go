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

	// Where this environment places the frames its helpers build. A harness that
	// always transmitted on an EU868 channel could not test US915 at all: the
	// engine would reject the frequency as outside the band, and every scenario
	// would silently be an EU868 scenario wearing another name.
	//
	// The default stays EU868 because the existing scenario corpus asserts EU868
	// specifics (RX2 at 869.525 MHz, RX1 on the uplink channel). The product
	// default is region.DefaultRegion (US915); NewRegion is how a test asks for
	// another band, and TestUS915Session exercises that path end to end.
	regionName   string
	uplinkFreqHz uint32
	uplinkDR     int

	// sched is set by NewDeferred. When present, the helpers that a test expects
	// to have completed synchronously run the pending work themselves, so a test
	// only has to think about the scheduler when it is studying the scheduler.
	sched *DeferredScheduler
}

// Settle runs anything the deferred scheduler is holding, including work that
// those callbacks schedule in turn, until the queue is empty.
//
// Draining rather than running one round matters because a real uplink is a
// chain: closing the aggregation window processes the frame, and processing the
// frame schedules the downlink. One round would leave the answer unsent and the
// test would be asserting that the engine does nothing.
func (env *Env) Settle() {
	if env.sched == nil {
		return
	}
	// Bounded so a callback that schedules itself forever fails the test instead
	// of hanging it.
	for round := 0; round < 64; round++ {
		if env.sched.Run() == 0 {
			return
		}
	}
	env.t.Fatal("scheduled work never settled; a callback is scheduling itself forever")
}

// New builds an engine with the default configuration, EU868 and a fixed clock.
// mutate adjusts the config before the engine is built; pass nil for defaults.
func New(t *testing.T, mutate func(*engine.Config)) *Env {
	t.Helper()

	store := engine.NewMemStore()
	return newOnStore(t, store, store.Store(), mutate)
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
		env.frame(wire, gateway.RxWindow1)); err != nil {
		env.t.Fatalf("HandleUplink(join): %v", err)
	}
	env.Settle()

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
		env.frame(wire, window)); err != nil {
		env.t.Fatalf("HandleUplink: %v", err)
	}
	env.Settle()
}

// UplinkWire feeds raw bytes, for frames a well-behaved device would not build:
// replays, a foreign DevAddr, a corrupted MIC.
func (env *Env) UplinkWire(wire []byte) error {
	env.t.Helper()
	err := env.Engine.HandleUplink(context.Background(),
		env.frame(wire, gateway.RxWindow1))
	env.Settle()
	return err
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
	f := env.frame(wire, gateway.RxWindow1)
	f.SNR = snr
	if err := env.Engine.HandleUplink(context.Background(), f); err != nil {
		env.t.Fatalf("HandleUplink: %v", err)
	}
	env.Settle()
}

// UplinkFrame wraps radio bytes in the metadata a gateway would report, on the
// band the EU868 fixtures are written for. Tests that care about the band use
// UplinkFrameOn or an Env method, which follow the environment's band.
func UplinkFrame(wire []byte, window gateway.RxWindow, freqHz uint32, dr int) gateway.UplinkFrame {
	return UplinkFrameOn(wire, window, "EU868", freqHz, dr)
}

// UplinkFrameOn is UplinkFrame with the band named, because the frame's region
// and its frequency have to agree or the engine is being asked to apply one
// band's rules to another band's airtime.
func UplinkFrameOn(wire []byte, window gateway.RxWindow, regionName string,
	freqHz uint32, dr int) gateway.UplinkFrame {

	return gateway.UplinkFrame{
		GatewayID:     MustEUI64Static(GatewayEUI),
		RFRegion:      regionName,
		RxWindow:      window,
		FrequencyHz:   freqHz,
		DataRateIndex: dr,
		UplinkID:      [8]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08},
		PHYPayload:    wire,
		RSSI:          -72,
		SNR:           8.5,
	}
}

// CopyOf builds a second gateway's copy of the same frame: identical bytes,
// different receiver, different signal, different transport token.
//
// The token differs because that is what the wire does — each gateway derives its
// own from its own concentrator — and because the downlink has to be timed from
// the copy the engine chose, not from another gateway's. A test that reused one
// token could not tell those two things apart.
func CopyOf(wire []byte, gatewayEUI lorawan.EUI64, snr, rssi float64, token byte) gateway.UplinkFrame {
	f := UplinkFrame(wire, gateway.RxWindow1, EU868Ch0Hz, EU868DR5)
	f.GatewayID = gatewayEUI
	f.SNR = snr
	f.RSSI = rssi
	f.UplinkID = [8]byte{token, token, token, token, token, token, token, token}
	return f
}

// CopyOf is the environment's band version of the package CopyOf: a second
// gateway's reception of the same frame, placed on the channel this environment
// transmits on.
func (env *Env) CopyOf(wire []byte, gatewayEUI lorawan.EUI64, snr, rssi float64, token byte) gateway.UplinkFrame {
	f := env.frame(wire, gateway.RxWindow1)
	f.GatewayID = gatewayEUI
	f.SNR = snr
	f.RSSI = rssi
	f.UplinkID = [8]byte{token, token, token, token, token, token, token, token}
	return f
}

// Feed hands the engine a frame the test assembled itself, so a copy from a
// second gateway does not have to be smuggled through the single-gateway helper.
//
// It stamps the arrival time when the caller left it unset, because the engine
// measures the remaining receive-window budget from it: a frame with no arrival
// time looks like it arrived at an unknown moment, and the engine correctly
// declines to subtract anything.
func (env *Env) Feed(f gateway.UplinkFrame) error {
	env.t.Helper()
	if f.ReceivedAt.IsZero() {
		f.ReceivedAt = env.Clock.Now()
	}
	return env.Engine.HandleUplink(context.Background(), f)
}

// Enqueue queues an application downlink for the standard device.
//
// A class-A server answers only when it has something to say, so a test that
// wants to observe a downlink has to give the engine a reason to send one.
// Forgetting this is the most common way to write an aggregation or timing test
// that fails for a reason unrelated to what it is measuring.
func (env *Env) Enqueue(payload []byte, confirmed bool) {
	env.t.Helper()
	if err := env.Engine.EnqueueDownlink(context.Background(),
		MustEUI64(env.t, DevEUI), 10, payload, confirmed); err != nil {
		env.t.Fatalf("EnqueueDownlink: %v", err)
	}
}

// SharedState is what two engine instances must share to behave like one
// network rather than two that happen to hear the same radio.
type SharedState struct {
	// Seen is the frame de-duplication book. Two instances fed by one gateway
	// each receive the same frame; only one may answer it.
	Seen engine.SeenRepo
	// Airtime is the duty-cycle ledger. Sharing it makes the two instances'
	// combined transmissions add up, which is what compliance requires.
	Airtime engine.AirtimeRepo
}

// NewShared builds an engine with its own device and session store but a
// caller-supplied shared book.
//
// It exists so a test can stand up two engines against one gateway and assert
// they answer a frame once, which is the property a single-instance test cannot
// observe at all. Each engine keeps its own sessions: sharing those would be a
// different, larger change.
func NewShared(t *testing.T, shared SharedState, mutate func(*engine.Config)) *Env {
	t.Helper()

	store := engine.NewMemStore()
	st := store.Store()
	st.Seen = shared.Seen
	st.Airtime = shared.Airtime
	return newOnStore(t, store, st, mutate)
}

// newOnStore is the shared construction path behind New and NewShared.
func newOnStore(t *testing.T, store *engine.MemStore, st engine.Store,
	mutate func(*engine.Config)) *Env {

	return buildEnv(t, store, st, mutate, nil)
}

// DeferredScheduler holds scheduled work until a test runs it, which is how the
// aggregation window becomes observable.
//
// The default test scheduler runs callbacks inline, and that is right for
// everything except a feature whose whole behaviour is "wait, then act": inline
// execution closes the window before the second gateway's copy can arrive, so
// the test would be asserting that aggregation does nothing.
type DeferredScheduler struct {
	mu    sync.Mutex
	items []deferredItem
	// ran accumulates the delays of everything already executed, so a test can
	// assert on the timing of work it had to let run.
	ran []time.Duration
}

type deferredItem struct {
	delay time.Duration
	fn    func()
}

// Pending is how much work is waiting.
func (d *DeferredScheduler) Pending() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.items)
}

// Reset forgets both the queued work and the history of what already ran.
//
// A test that has just completed a join still has that join's RX1/RX2 delays in
// its execution history, and a timing assertion made afterwards would be reading
// the join's numbers. Clearing both halves at once keeps "measure from here"
// impossible to get half-done.
func (d *DeferredScheduler) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.items = nil
	d.ran = nil
}

// Delays reports the delay each waiting item was scheduled with, in the order it
// was scheduled. Assertions about *when* a downlink should go out — the remaining
// slack after aggregation — are made against this.
func (d *DeferredScheduler) Delays() []time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()

	out := make([]time.Duration, 0, len(d.items))
	for _, it := range d.items {
		out = append(out, it.delay)
	}
	return out
}

// Run executes everything waiting and reports how much there was. Callbacks are
// taken out of the queue before running, so one that schedules more work leaves
// it for the next Run rather than recursing.
func (d *DeferredScheduler) Run() int {
	d.mu.Lock()
	items := d.items
	d.items = nil
	for _, it := range items {
		d.ran = append(d.ran, it.delay)
	}
	d.mu.Unlock()

	for _, it := range items {
		it.fn()
	}
	return len(items)
}

// ExecutedDelays reports the delay each item was scheduled with, across every Run
// so far. Asserting on the *dispatch* delay of a downlink means reading this
// after the window has run, at which point Pending and Delays are empty.
func (d *DeferredScheduler) ExecutedDelays() []time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]time.Duration(nil), d.ran...)
}

// NewDeferred builds an environment whose scheduled work waits for the test.
func NewDeferred(t *testing.T, mutate func(*engine.Config)) (*Env, *DeferredScheduler) {
	t.Helper()

	store := engine.NewMemStore()
	sched := &DeferredScheduler{}
	env := buildEnv(t, store, store.Store(), mutate, func(delay time.Duration, fn func()) {
		sched.mu.Lock()
		defer sched.mu.Unlock()
		sched.items = append(sched.items, deferredItem{delay: delay, fn: fn})
	})
	env.sched = sched
	return env, sched
}

// buildEnv constructs the engine. scheduler nil means the inline one, which is
// what nearly every test wants.
func buildEnv(t *testing.T, store *engine.MemStore, st engine.Store,
	mutate func(*engine.Config), scheduler func(time.Duration, func())) *Env {

	cfg := engine.DefaultConfig()
	cfg.NetID = MustNetID(t, "010203")
	cfg.Region = "EU868"
	if mutate != nil {
		mutate(&cfg)
	}

	clk := &Clock{t: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)}
	gw := &Gateway{}
	if scheduler == nil {
		// Run scheduled transmissions inline so assertions are deterministic.
		scheduler = func(_ time.Duration, fn func()) { fn() }
	}

	eng, err := engine.New(cfg, st, gw,
		engine.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		engine.WithClock(clk.Now),
		engine.WithScheduler(scheduler),
	)
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}

	// Place the harness's frames on the band the engine is configured for, so a
	// test that changes the band does not also have to know that band's channels.
	freq, dr := defaultUplinkPlacement(t, cfg.Region)
	return &Env{
		t: t, Engine: eng, Store: store, GW: gw, Clock: clk,
		regionName: cfg.Region, uplinkFreqHz: freq, uplinkDR: dr,
	}
}

// defaultUplinkPlacement returns the band's first enabled uplink channel and the
// fastest data rate that channel may use within the band's uplink range.
//
// The fastest, not the slowest: at the slowest rate every payload runs into the
// band's minimum maximum-payload size (59 bytes on EU868 DR0), so ordinary test
// frames would be refused for capacity rather than for whatever the test is
// actually about. On EU868 and AS923 this resolves to DR5 (SF7BW125), the
// fixture the scenarios were written against; on US915 channel 0 it resolves to
// DR3, the fastest rate a 125 kHz channel there allows.
func defaultUplinkPlacement(t *testing.T, regionName string) (freqHz uint32, dataRate int) {
	t.Helper()

	reg, err := region.New(regionName)
	if err != nil {
		t.Fatalf("testenv: %v", err)
	}
	maxUp := reg.Defaults().MaxUplinkDataRate
	for _, ch := range reg.UplinkChannels() {
		if !ch.Enabled || len(ch.DataRates) == 0 {
			continue
		}
		best := -1
		for _, dr := range ch.DataRates {
			if dr <= maxUp && dr > best {
				best = dr
			}
		}
		if best >= 0 {
			return ch.FrequencyHz, best
		}
	}
	// A band with no usable default channel would make every scenario
	// meaningless rather than merely awkward, so say so loudly.
	t.Fatalf("testenv: region %s has no enabled uplink channel carrying a data rate "+
		"at or below its maximum of DR%d", regionName, maxUp)
	return 0, 0
}

// NewRegion builds an environment on a named band. The scenarios that matter on
// every band — join, uplink, both receive windows, ADR — run through this so that
// "supports multiple regions" is tested rather than asserted.
func NewRegion(t *testing.T, regionName string, mutate func(*engine.Config)) *Env {
	t.Helper()

	return New(t, func(cfg *engine.Config) {
		cfg.Region = regionName
		if mutate != nil {
			mutate(cfg)
		}
	})
}

// Region is the band this environment transmits on.
func (env *Env) Region() string { return env.regionName }

// UplinkFrequency and UplinkDataRate report where this environment places
// frames, for assertions about the RX1 the engine should derive from them.
func (env *Env) UplinkFrequency() uint32 { return env.uplinkFreqHz }
func (env *Env) UplinkDataRate() int     { return env.uplinkDR }

// frame builds a gateway uplink on this environment's band.
func (env *Env) frame(wire []byte, window gateway.RxWindow) gateway.UplinkFrame {
	return UplinkFrameOn(wire, window, env.regionName, env.uplinkFreqHz, env.uplinkDR)
}

// SharedSeen is an in-memory de-duplication book that several engines can point
// at, standing in for the Postgres table in a test of multi-instance behaviour.
//
// It implements the same window semantics as the real one: a claim succeeds when
// the key is new or has aged out.
type SharedSeen struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

// NewSharedSeen returns an empty book.
func NewSharedSeen() *SharedSeen {
	return &SharedSeen{seen: map[string]time.Time{}}
}

// ClaimFrame implements engine.SeenRepo.
func (s *SharedSeen) ClaimFrame(_ context.Context, key string, window time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	if at, ok := s.seen[key]; ok && now.Sub(at) < window {
		return false, nil
	}
	s.seen[key] = now
	return true, nil
}

// Claims is how many distinct frames the book holds, for a test that wants to
// assert the book was actually consulted rather than bypassed.
// Claims reports how many frames are held, for assertions about the book itself.
func (s *SharedSeen) Claims() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.seen)
}

// SharedLedger is an in-memory stand-in for ns_airtime, so the durability of the
// duty-cycle books can be tested without a database.
//
// It stores what it was given rather than mirroring the engine's own structure:
// SaveAirtime replaces the whole window, exactly as the Postgres flush does, so a
// test that writes the ledger and hands the same object to a second engine
// reproduces a restart rather than approximating one.
//
// Ageing is judged by the engine's clock, which WithClock wires up. Judging it by
// the wall clock instead compares a fake-clock booking against real time, and the
// test passes only while the fake timestamp happens to sit within a window of
// now — deterministic-looking, and wrong on a Monday.
type SharedLedger struct {
	mu      sync.Mutex
	now     func() time.Time
	records []engine.AirtimeRecord
	flushes int
}

// NewSharedLedger returns an empty ledger.
func NewSharedLedger() *SharedLedger { return &SharedLedger{now: time.Now} }

// WithClock points the ledger at an environment's clock so its idea of "inside
// the window" matches the engine's.
func (l *SharedLedger) WithClock(c *Clock) *SharedLedger {
	l.mu.Lock()
	defer l.mu.Unlock()
	if c != nil {
		l.now = c.Now
	}
	return l
}

// SaveAirtime replaces the window with what was handed over.
func (l *SharedLedger) SaveAirtime(_ context.Context, records []engine.AirtimeRecord,
	window time.Duration) error {

	l.mu.Lock()
	defer l.mu.Unlock()
	l.flushes++

	cutoff := l.now().Add(-window)
	l.records = l.records[:0]
	for _, r := range records {
		if r.Start.Before(cutoff) {
			continue
		}
		l.records = append(l.records, r)
	}
	return nil
}

// LoadAirtime returns the bookings still inside the window.
func (l *SharedLedger) LoadAirtime(_ context.Context, window time.Duration) ([]engine.AirtimeRecord, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	cutoff := l.now().Add(-window)
	var out []engine.AirtimeRecord
	for _, r := range l.records {
		if r.Start.Before(cutoff) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// Flushes counts writes, so a test can assert the engine actually persisted
// rather than merely being able to.
func (l *SharedLedger) Flushes() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.flushes
}

// Booked is the airtime recorded for one gateway and sub-band.
func (l *SharedLedger) Booked(gateway lorawan.EUI64, minHz uint32) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()

	var total time.Duration
	for _, r := range l.records {
		if r.Gateway == gateway && r.MinHz == minHz {
			total += r.Airtime
		}
	}
	return total
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
