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
}

// DownlinkFrame is one downlink transmission the engine asks a gateway to
// perform.
type DownlinkFrame struct {
	GatewayID lorawan.EUI64
	RFRegion  string
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

// Gateway is the downlink transmitter abstraction.
type Gateway interface {
	SendDownlink(ctx context.Context, f DownlinkFrame) error
}

// Lazy defers to a Gateway installed after construction.
//
// The engine needs a Gateway when it is built, and the transport needs the
// engine as its uplink handler, so one of the two must be wired in afterwards.
// Wiring it through Lazy keeps that cycle out of both constructors and makes
// the "no transport yet" case an explicit error instead of a nil dereference.
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
