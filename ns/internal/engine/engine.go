package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/gateway"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
	"github.com/fieldnode/lorawan-server/ns/internal/region"
)

// Config configures an Engine.
type Config struct {
	// NetID identifies this network. Its low 7 bits become the DevAddr
	// prefix of every device that joins, and it is carried in join-accept.
	NetID lorawan.NetID
	// Region is the default band name (see region.New). A frame whose
	// gateway reports another supported band is processed in that band.
	Region string
	// MaxFCntDelta bounds how far ahead of the expected uplink counter an
	// accepted frame may be. It limits the damage of a forged frame with a
	// valid MIC (practically impossible) or a device with a broken counter.
	// Zero means DefaultMaxFCntDelta.
	MaxFCntDelta uint32
	// DedupWindow bounds the multi-gateway de-duplication memory. Zero means
	// DefaultDedupWindow.
	DedupWindow time.Duration
	// DownlinkPowerdBm is the TX power handed to the gateway; the transport
	// clamps it to its own and the band's limits.
	DownlinkPowerdBm int8
	// DeviceStatusEvery asks every Nth accepted uplink for a battery/margin
	// report (0 disables the periodic poll).
	DeviceStatusEvery int

	// ADRInstallationMargin is the fade margin ADR keeps between the measured
	// link and the data rate it chooses. Higher is more reliable and slower;
	// 10 dB is the value the LoRaWAN application note recommends and the one
	// most deployed servers default to.
	ADRInstallationMargin float64

	// ADRACKLimit and ADRACKDelay are the device-side counters that make a
	// device fall back to a lower data rate when the network stops answering.
	// The server cannot see that counter, so it sends a LinkADRReq — even one
	// that changes nothing — before the counter can expire, which resets it.
	// The defaults are RP002's (2048 + 32 uplinks).
	ADRACKLimit int
	ADRACKDelay int

	// LinkADRMaxRetries is how often an unanswered LinkADRReq is re-sent before
	// the server gives up. RP002 specifies 32.
	LinkADRMaxRetries int

	// DutyCycleWindow is the period duty cycle is measured over. Zero selects
	// DefaultDutyCycleWindow (one hour), which is what the regulation uses.
	// Shortening it lets bursts through and then blocks; lengthening it makes
	// the server refuse transmissions long after the traffic that caused it.
	DutyCycleWindow time.Duration

	// DisableDutyCycleEnforcement turns off the checks on our own transmissions.
	// It is phrased negatively so the zero value enforces, and it exists for
	// bands and test rigs where the concentrator is known to enforce on its
	// own — not as a production setting.
	DisableDutyCycleEnforcement bool
}

// Engine defaults.
const (
	DefaultMaxFCntDelta = uint32(100)
	DefaultDedupWindow  = 30 * time.Second
)

// DefaultConfig returns the configuration used when the caller supplies none.
func DefaultConfig() Config {
	return Config{
		Region:                "EU868",
		MaxFCntDelta:          DefaultMaxFCntDelta,
		DedupWindow:           DefaultDedupWindow,
		DownlinkPowerdBm:      14,
		ADRInstallationMargin: DefaultADRInstallationMargin,
		ADRACKLimit:           DefaultADRACKLimit,
		ADRACKDelay:           DefaultADRACKDelay,
		LinkADRMaxRetries:     DefaultLinkADRMaxRetries,
	}
}

// Engine is the network-server core. A single Engine is safe for concurrent
// use; it holds no per-connection state.
type Engine struct {
	cfg           Config
	store         Store
	gw            gateway.Gateway
	log           *slog.Logger
	defaultRegion region.Region

	mu      sync.Mutex
	regions map[string]region.Region
	seen    map[string]time.Time
	// budget tracks channel occupancy per gateway and sub-band so our own
	// transmissions stay inside the band's duty cycle and dwell time.
	budget *airtimeBudget

	scheduleFn func(delay time.Duration, fn func())
	nowFn      func() time.Time
}

// Option customises an Engine at construction time.
type Option func(*Engine)

// WithLogger sets the structured logger. Defaults to slog.Default().
func WithLogger(l *slog.Logger) Option { return func(e *Engine) { e.log = l } }

// WithScheduler replaces the delayed-execution mechanism. Tests pass a
// synchronous implementation to observe downlinks without sleeping.
func WithScheduler(fn func(delay time.Duration, fn func())) Option {
	return func(e *Engine) { e.scheduleFn = fn }
}

// WithClock replaces the time source (tests).
func WithClock(fn func() time.Time) Option { return func(e *Engine) { e.nowFn = fn } }

// New builds an Engine. store must have all three ports set and gw must not be
// nil; the region in cfg.Region must be supported.
func New(cfg Config, store Store, gw gateway.Gateway, opts ...Option) (*Engine, error) {
	if err := store.Validate(); err != nil {
		return nil, err
	}
	if gw == nil {
		return nil, errors.New("engine: gateway is nil")
	}
	if cfg.Region == "" {
		cfg.Region = DefaultConfig().Region
	}
	if cfg.MaxFCntDelta == 0 {
		cfg.MaxFCntDelta = DefaultMaxFCntDelta
	}
	if cfg.DedupWindow <= 0 {
		cfg.DedupWindow = DefaultDedupWindow
	}
	// Zero means "unset" for the ADR knobs, so a Config literal built field by
	// field still gets sane behaviour. A deliberately zero installation margin
	// is expressed with a small positive number; 0 dB of fade margin is not a
	// configuration anyone should want.
	if cfg.ADRInstallationMargin == 0 {
		cfg.ADRInstallationMargin = DefaultADRInstallationMargin
	}
	if cfg.ADRACKLimit <= 0 {
		cfg.ADRACKLimit = DefaultADRACKLimit
	}
	if cfg.ADRACKDelay <= 0 {
		cfg.ADRACKDelay = DefaultADRACKDelay
	}
	if cfg.LinkADRMaxRetries <= 0 {
		cfg.LinkADRMaxRetries = DefaultLinkADRMaxRetries
	}
	if cfg.DutyCycleWindow <= 0 {
		cfg.DutyCycleWindow = DefaultDutyCycleWindow
	}

	e := &Engine{
		cfg:     cfg,
		store:   store,
		gw:      gw,
		log:     slog.Default(),
		regions: map[string]region.Region{},
		seen:    map[string]time.Time{},
		nowFn:   time.Now,
	}
	for _, opt := range opts {
		opt(e)
	}
	// Built after the options so it shares the clock they may have replaced;
	// a test that drives time by hand must drive the duty-cycle window with the
	// same clock or the ledger and the engine disagree about when it is.
	e.budget = newAirtimeBudget(cfg.DutyCycleWindow, e.nowFn)
	if e.scheduleFn == nil {
		e.scheduleFn = func(delay time.Duration, fn func()) {
			time.AfterFunc(delay, fn)
		}
	}

	r, err := region.New(cfg.Region)
	if err != nil {
		return nil, fmt.Errorf("engine: default region: %w", err)
	}
	e.defaultRegion = r
	e.regions[r.CommonName()] = r
	return e, nil
}

// Config returns the effective configuration.
func (e *Engine) Config() Config { return e.cfg }

func (e *Engine) now() time.Time { return e.nowFn() }

// regionFor resolves a band name, empty meaning the engine default. Regions
// are constructed once and cached; they are read-only.
func (e *Engine) regionFor(name string) (region.Region, error) {
	if name == "" {
		return e.defaultRegion, nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if r, ok := e.regions[name]; ok {
		return r, nil
	}
	r, err := region.New(name)
	if err != nil {
		return nil, err
	}
	e.regions[r.CommonName()] = r
	return r, nil
}

// recordEvent persists an event; a recorder failure is logged, never fatal to
// the radio flow.
func (e *Engine) recordEvent(ctx context.Context, ev Event) {
	if ev.At.IsZero() {
		ev.At = e.now()
	}
	if err := e.store.Recorder.RecordEvent(ctx, ev); err != nil {
		e.log.Error("record event failed", "kind", ev.Kind, "reason", ev.Reason, "err", err)
	}
}

// dropEvent records a rejected frame and returns nil: a rejected frame is a
// normal outcome on live radio and must not abort the caller's read loop.
func (e *Engine) dropEvent(ctx context.Context, frame gateway.UplinkFrame, phy *lorawan.PHYPayload, reason, detail string) error {
	ev := Event{
		Kind:      EventUplinkDrop,
		Reason:    reason,
		Detail:    detail,
		GatewayID: frame.GatewayID,
	}
	if phy != nil {
		if phy.MAC != nil {
			ev.DevAddr = phy.MAC.FHDR.DevAddr
			ev.FCntUp = uint32(phy.MAC.FHDR.FCnt)
			if phy.MAC.FPort != nil {
				ev.FPort = int(*phy.MAC.FPort)
			} else {
				ev.FPort = -1
			}
		}
		if phy.JoinRequest != nil {
			ev.DevEUI = phy.JoinRequest.DevEUI
		}
	}
	e.recordEvent(ctx, ev)
	return nil
}
