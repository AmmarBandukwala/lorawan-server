// Package testenv drives the network server the way a device would.
//
// It exists so tests can be written against the exported API without each one
// rebuilding the same scaffolding: an engine with an in-memory store, a gateway
// that records instead of transmitting, a clock that only moves when a test says
// so, and a simulated device that produces spec-correct frames.
//
// It has to be a package rather than a shared test file for a structural
// reason: a harness cannot live inside the package it drives, because that
// package's own tests could not import it. A separate package can only use
// exported API, and that constraint is the point — if a
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

	session, err := env.Store.SessionByDevEUI(context.Background(), devEUI)
	if err != nil {
		env.t.Fatalf("SessionByDevEUI: %v", err)
	}
	return session
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
