// Package gateway defines the transport boundary between the Fieldnode
// network-server core and the LoRaWAN gateways in the field.
//
// The engine never speaks a gateway protocol directly. M3 adds a native
// Semtech UDP (and later Basic Station / MultiTech WS) server that converts
// radio packets into gateway.UplinkFrame values and executes the
// gateway.DownlinkFrame values the engine emits. Keeping this boundary tiny
// is what lets the whole core be tested with a fake gateway and no radio.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
)

// RxWindow identifies which receive window a downlink belongs to.
type RxWindow int

const (
	// RxWindow1 opens RX1Delay seconds after the uplink, on the RX1
	// frequency/data-rate derived from the uplink.
	RxWindow1 RxWindow = iota + 1
	// RxWindow2 opens RX2Delay seconds after the uplink on the band's
	// fixed RX2 frequency and data rate.
	RxWindow2
	// RxWindowManual is used by class-C transmissions and late
	// re-transmissions, where the engine supplies an absolute concentrator
	// timestamp instead of an uplink-relative delay.
	RxWindowManual
)

func (w RxWindow) String() string {
	switch w {
	case RxWindow1:
		return "RX1"
	case RxWindow2:
		return "RX2"
	case RxWindowManual:
		return "manual"
	default:
		return "unknown"
	}
}

// UplinkFrame is a single radio reception of a LoRaWAN frame. The same frame
// received by several gateways arrives as several UplinkFrames; the engine
// de-duplicates them (see engine.Config.DedupWindow).
type UplinkFrame struct {
	// GatewayID is the gateway that received the frame (its EUI).
	GatewayID lorawan.EUI64
	// RFRegion is the band the gateway operates in. Empty means "use the
	// engine's configured default region".
	RFRegion string
	// RxWindow is the window the uplink was received in. Class-A devices
	// only listen back in the window they transmitted in, so this decides
	// which downlink plans are usable.
	RxWindow RxWindow
	// FrequencyHz and DataRateIndex describe the uplink physical channel.
	FrequencyHz   uint32
	DataRateIndex int
	// UplinkID is the transport token that must be echoed back so the
	// gateway can derive the exact downlink timestamp from its own uplink
	// reception time (Semtech protocol "uplink token"). Zero means the
	// transport does not support token-based timing.
	UplinkID [8]byte
	// PHYPayload is the raw over-the-air frame, MIC included.
	PHYPayload []byte
	RSSI       float64
	SNR        float64
	// ReceivedAt is when the gateway received the frame (wall clock).
	ReceivedAt time.Time

	// Receptions holds every gateway copy of this frame, the chosen one first.
	// A frame that reaches two gateways arrives twice at the server, and the
	// second copy is information rather than noise: it says the device is
	// reachable from two directions, and its SNR may be the better one.
	//
	// The top-level fields above always describe the *chosen* reception — the
	// copy the downlink will go back through — so code that ignores Receptions
	// keeps working and simply does not benefit from the merge.
	Receptions []Reception
}

// Reception is one gateway's copy of the same radio frame.
//
// Frequency, data rate and window are repeated here rather than trusted from
// the frame because a forwarder can report a different arrival channel per
// gateway, and because the downlink for a chosen reception has to be planned
// from what that gateway actually heard.
type Reception struct {
	GatewayID lorawan.EUI64
	// UplinkID is the transport token of *this* copy. It has to travel with the
	// reception: the downlink timestamp is derived from the concentrator
	// timestamp of whichever copy the downlink is anchored to, and using
	// another gateway's token would place the transmission at the wrong moment.
	UplinkID      [8]byte
	FrequencyHz   uint32
	DataRateIndex int
	RxWindow      RxWindow
	RSSI          float64
	SNR           float64
	ReceivedAt    time.Time
}

// AllReceptions returns every copy of the frame, synthesising a single-element
// list when a transport has not populated Receptions. It keeps the aggregation
// logic from having to special-case a frame that arrived before it existed.
func (f UplinkFrame) AllReceptions() []Reception {
	if len(f.Receptions) > 0 {
		return f.Receptions
	}
	return []Reception{{
		GatewayID:     f.GatewayID,
		UplinkID:      f.UplinkID,
		FrequencyHz:   f.FrequencyHz,
		DataRateIndex: f.DataRateIndex,
		RxWindow:      f.RxWindow,
		RSSI:          f.RSSI,
		SNR:           f.SNR,
		ReceivedAt:    f.ReceivedAt,
	}}
}

// DownlinkFrame is one downlink transmission the engine asks a gateway to
// perform.
type DownlinkFrame struct {
	GatewayID lorawan.EUI64
	RFRegion  string
	// DeviceEUI is the device this downlink is for. The UDP transport has no
	// use for it; the Basics Station transport does, because a station echoes
	// it back in the transmission confirmation and refuses a dnmsg that omits
	// it. Carrying it here keeps that transport from having to invent one.
	DeviceEUI lorawan.EUI64
	UplinkID  [8]byte
	Window    RxWindow
	// Delay is how long after the associated uplink the transmission must
	// start. The UDP transport converts it into a concentrator timestamp
	// (either from UplinkID or from its own clock).
	Delay       time.Duration
	FrequencyHz uint32
	// DataRateIndex is the band data-rate index; the transport resolves it
	// to SF/BW/CR (or FSK bitrate) using the same region tables.
	DataRateIndex int
	TxPowerdBm    int8
	PHYPayload    []byte
}

// Presence is what a transport reports about one gateway it serves.
//
// Both transports can answer the same operator questions — is this gateway
// reachable, when was it last heard from, did its downlinks go out — using
// different protocols to get there. Reporting through one shape is what lets the
// status endpoint and the dashboard show a single gateway list instead of one
// list per transport, which is what an operator actually wants to see.
type Presence struct {
	EUI       lorawan.EUI64
	Transport string // "semtech-udp" or "basic-station"
	Address   string
	// Protocol names the dialect within the transport, for example the Semtech
	// packet-forwarder protocol version.
	Protocol string

	Connected  bool
	LastSeen   time.Time
	Packets    uint64
	StatsAt    time.Time
	RxReceived uint32
	RxOk       uint32
	TxReceived uint32
	Latitude   float64
	Longitude  float64
	Altitude   int

	DownlinksSent   uint64
	DownlinkErrors  uint64
	LastTxError     string
	DownlinkConfirm uint64
	// DownlinkUnconfirmed counts downlinks with no transmission confirmation.
	// It is an inference, not a report: neither protocol sends a negative
	// acknowledgement, so "no confirmation arrived" is the only evidence.
	DownlinkUnconfirmed uint64

	// Station, Firmware and Model are what a Basics Station reported in its
	// handshake. The UDP protocol offers nothing equivalent, so these stay empty
	// there. They are worth carrying because "which firmware is this gateway on"
	// is the first question when one behaves differently from the rest.
	Station  string
	Firmware string
	Model    string
}

// Reporter is a transport that can report the gateways it serves.
type Reporter interface {
	Presence() []Presence
	// OnlineTimeout is the silence beyond which a gateway counts as offline. It
	// differs per transport — a UDP forwarder sends PULL_DATA on a fixed
	// interval, a station holds a TCP connection — so the caller cannot guess it.
	OnlineTimeout() time.Duration
}

// Compile-time proof that the wiring indirection satisfies the port.
var _ Gateway = (*Lazy)(nil)

// Gateway is the downlink transmitter abstraction.
type Gateway interface {
	SendDownlink(ctx context.Context, f DownlinkFrame) error
}

// Handler is the network-server side of a transport. engine.Engine implements
// it, and every transport — Semtech UDP, Basics Station, anything added later —
// converts its own wire format into UplinkFrame and calls this.
//
// It lives next to the frame types rather than inside each transport so that a
// transport cannot define a subtly different contract, and so the engine's
// conformance to it is checked once.
type Handler interface {
	HandleUplink(ctx context.Context, f UplinkFrame) error
}

// Reachable is an optional port a transport implements to say whether it could
// route to a gateway right now.
//
// It exists because a server can speak two gateway protocols at once, and a
// station connected over WebSocket cannot be reached through the UDP transport
// no matter what it tries. A transport that does not implement this is still
// usable; the Router just has to try it in order.
type Reachable interface {
	CanReach(gatewayID lorawan.EUI64) bool
}

// Router sends each downlink through whichever transport holds the gateway.
//
// Why this is needed rather than one transport per process: the same deployment
// routinely has both kinds of gateway — a few UDP forwarders already installed
// and a station that can only speak WebSocket — and the engine must not care.
// The device's session names a gateway, and the gateway's transport is a fact
// about the field, not about the device.
//
// Selection prefers a transport that claims the gateway. If none claims it, every
// transport is tried in registration order and the errors are joined, because
// "no transport knows this gateway" is a much more useful message when it lists
// what each one said.
type Router struct {
	mu         sync.RWMutex
	transports []namedTransport
}

type namedTransport struct {
	name string
	gw   Gateway
}

// NewRouter builds a Router with the given transports, in the order they are
// tried when no transport claims a gateway.
func NewRouter(transports ...NamedTransport) *Router {
	r := &Router{}
	for _, t := range transports {
		r.Add(t.Name, t.Gateway)
	}
	return r
}

// NamedTransport pairs a transport with the name it is reported under.
type NamedTransport struct {
	Name    string
	Gateway Gateway
}

// Add registers a transport. Registering the same name twice replaces it, which
// is what a test that swaps a fake transport in needs.
func (r *Router) Add(name string, gw Gateway) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, t := range r.transports {
		if t.name == name {
			r.transports[i].gw = gw
			return
		}
	}
	r.transports = append(r.transports, namedTransport{name: name, gw: gw})
}

// SendDownlink routes one downlink.
func (r *Router) SendDownlink(ctx context.Context, f DownlinkFrame) error {
	r.mu.RLock()
	transports := append([]namedTransport(nil), r.transports...)
	r.mu.RUnlock()

	if len(transports) == 0 {
		return errors.New("gateway: no transport is configured")
	}

	// Pass 1: whoever claims the gateway.
	for _, t := range transports {
		if reachable(t.gw, f.GatewayID) {
			if err := t.gw.SendDownlink(ctx, f); err != nil {
				return fmt.Errorf("gateway: send via %s: %w", t.name, err)
			}
			return nil
		}
	}

	// Pass 2: nobody claims it. Try each in turn. A transport that answers "not
	// connected" has told us something worth keeping, so the errors are joined
	// rather than the last one winning.
	var errs []error
	for _, t := range transports {
		if err := t.gw.SendDownlink(ctx, f); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", t.name, err))
			continue
		}
		return nil
	}
	return fmt.Errorf("gateway: no transport could reach %s: %w",
		f.GatewayID, errors.Join(errs...))
}

// reachable reports whether a transport says it can route to a gateway. A
// transport that does not implement Reachable is treated as a possible route, so
// adding the interface never silently disables a transport.
func reachable(gw Gateway, id lorawan.EUI64) bool {
	r, ok := gw.(Reachable)
	return ok && r.CanReach(id)
}

// CanReach reports whether any registered transport claims the gateway.
func (r *Router) CanReach(id lorawan.EUI64) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, t := range r.transports {
		if reachable(t.gw, id) {
			return true
		}
	}
	return false
}

// Presence merges the gateway reports of every transport that provides one.
//
// A gateway EUI appearing on two transports is real — the same hardware can be
// configured to speak both, or be mid-migration between them — and both rows are
// kept rather than deduplicated, because which one is currently carrying traffic
// is exactly what an operator needs to see during that switch-over.
func (r *Router) Presence() []Presence {
	r.mu.RLock()
	transports := append([]namedTransport(nil), r.transports...)
	r.mu.RUnlock()

	var out []Presence
	for _, t := range transports {
		rep, ok := t.gw.(Reporter)
		if !ok {
			continue
		}
		out = append(out, rep.Presence()...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].EUI.String() != out[j].EUI.String() {
			return out[i].EUI.String() < out[j].EUI.String()
		}
		return out[i].Transport < out[j].Transport
	})
	return out
}

// OnlineTimeouts returns each transport's online threshold, keyed by name. One
// number cannot serve both protocols: a UDP forwarder is judged by the gap
// between PULL_DATA packets, a station by the liveness of a held connection.
func (r *Router) OnlineTimeouts() map[string]time.Duration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := map[string]time.Duration{}
	for _, t := range r.transports {
		rep, ok := t.gw.(Reporter)
		if !ok {
			continue
		}
		out[t.name] = rep.OnlineTimeout()
	}
	return out
}

// Names lists the registered transports, for the status endpoint.
func (r *Router) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.transports))
	for _, t := range r.transports {
		out = append(out, t.name)
	}
	return out
}

// The engine needs a Gateway when it is built, and the transport needs the
// engine as its uplink handler, so one of the two must be wired in afterwards.
// Wiring it through Lazy keeps that cycle out of both constructors and makes
// the "no transport yet" case an explicit error instead of a nil dereference.
// Lazy defers to a Gateway installed after construction.
//
// The engine needs a Gateway when it is built, and each transport needs the
// engine as its uplink handler, so one of the two must be wired in afterwards.
// Wiring it through Lazy keeps that cycle out of both constructors and makes the
// "no transport yet" case an explicit error instead of a nil dereference. In
// practice the value installed is a Router, so the engine sees one transmitter
// however many protocols the deployment speaks.
type Lazy struct {
	mu sync.RWMutex
	gw Gateway
}

// Set installs (or replaces, for tests) the transport.
func (l *Lazy) Set(gw Gateway) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.gw = gw
}

// SendDownlink forwards to the installed transport.
func (l *Lazy) SendDownlink(ctx context.Context, f DownlinkFrame) error {
	l.mu.RLock()
	gw := l.gw
	l.mu.RUnlock()
	if gw == nil {
		return errors.New("gateway: no transport installed yet")
	}
	return gw.SendDownlink(ctx, f)
}
