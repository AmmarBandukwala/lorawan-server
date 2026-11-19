// Package integration carries decoded traffic from the network server to the
// applications that asked for it.
//
// It sits between the engine and the sinks by composition rather than by
// modification: Dispatcher implements engine.Recorder, writes through to the real
// store, and fans the same records out to the configured sinks. The engine
// therefore does not know integrations exist, which keeps the radio path free of
// HTTP client code and lets a deployment run with no sinks at all.
package integration

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
)

// Event kinds an application can receive.
const (
	KindUplink = "uplink"
	KindJoin   = "join"
)

// Delivery defaults, applied when a Config leaves the field unset.
const (
	// DefaultBufferSize is how many events the queue holds before the radio path
	// starts dropping. It is deliberately generous: an endpoint that restarts
	// should drain on its own rather than cost events.
	DefaultBufferSize = 256
	// DefaultWorkers is one, which keeps events in order for a consumer. More
	// workers deliver faster and out of order, so raising it is a decision a
	// deployment makes explicitly.
	DefaultWorkers = 1
	// drainTimeout bounds a single delivery during shutdown. It stays well
	// inside the orchestrator's kill window so a restart finishes cleanly rather
	// than being SIGKILLed mid-request.
	drainTimeout = 3 * time.Second
)

// Event is the application-facing view of one thing the network observed.
//
// It is a separate type from engine.UplinkRecord on purpose. The record is
// protocol bookkeeping — key epoch, raw frame, replay state — and its shape is
// free to change as the engine evolves; an event is a contract with somebody's
// consumer, so changing it breaks them. Keeping them apart lets the engine be
// refactored without a deprecation cycle for every webhook.
type Event struct {
	Kind string `json:"kind"`

	DeviceEUI string `json:"dev_eui"`
	DevAddr   string `json:"dev_addr"`

	// ReceivedAt is when the network heard the frame. A timestamp rather than a
	// relative string: a queued or proxied event has to stay interpretable.
	ReceivedAt time.Time `json:"received_at"`

	// Uplink fields. Data is the decrypted application payload, base64 encoded
	// because JSON has no byte type; RawPayload is the whole frame as hex, for
	// consumers that verify or replay frames themselves.
	FCntUp         uint32 `json:"f_cnt_up,omitempty"`
	FPort          int    `json:"f_port,omitempty"`
	Data           string `json:"data,omitempty"`
	RawPayload     string `json:"raw_payload,omitempty"`
	Retransmission bool   `json:"retransmission,omitempty"`
	Acknowledged   bool   `json:"acknowledged,omitempty"`

	// Radio metadata, for coverage mapping and for debugging a device that
	// reports a link problem.
	GatewayID   string  `json:"gateway_id,omitempty"`
	FrequencyHz uint32  `json:"frequency_hz,omitempty"`
	DataRate    int     `json:"data_rate_index,omitempty"`
	RSSI        float64 `json:"rssi,omitempty"`
	SNR         float64 `json:"snr,omitempty"`
}

// Sink delivers events to one destination.
type Sink interface {
	// Publish blocks until the event is accepted or the context is done. The
	// dispatcher calls it from its own worker, never from the radio path.
	Publish(ctx context.Context, ev Event) error
	// Name identifies the sink in logs and on the status page.
	Name() string
}

// Config configures a Dispatcher.
type Config struct {
	// BufferSize is how many events wait for a worker. It absorbs a webhook blip
	// without dropping; when it fills, events are dropped and counted, because
	// the alternative — blocking the uplink handler on somebody else's HTTP
	// endpoint — would let a dead application take the network server down.
	BufferSize int

	// Workers is how many events may be in flight. One keeps strict order,
	// including within a device, which is what consumers of an uplink stream
	// usually expect; more buys throughput at the cost of that ordering whenever
	// a delivery is retried.
	Workers int

	Logger *slog.Logger
}

// Dispatcher is the Recorder the engine is built with.
type Dispatcher struct {
	inner engine.Recorder
	sinks []Sink

	queue chan Event

	// Counters are read for a status page, where a slightly stale value is fine
	// and a mutex on every uplink is not.
	published atomic.Int64
	failed    atomic.Int64
	dropped   atomic.Int64
	queued    atomic.Int64

	log      *slog.Logger
	cancel   context.CancelFunc
	workers  sync.WaitGroup
	closeOne sync.Once
}

// Stats is the dispatcher's view of its own health, for /status and for logs.
type Stats struct {
	Published int64 `json:"published"`
	Failed    int64 `json:"delivery_failures"`
	Dropped   int64 `json:"dropped"`
	Queued    int64 `json:"queued"`
}

// New starts the delivery workers. Close must be called to stop them.
func New(inner engine.Recorder, sinks []Sink, cfg Config) *Dispatcher {
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = DefaultBufferSize
	}
	if cfg.Workers <= 0 {
		cfg.Workers = DefaultWorkers
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	d := &Dispatcher{
		inner: inner,
		sinks: sinks,
		queue: make(chan Event, cfg.BufferSize),
		log:   cfg.Logger,
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	for i := 0; i < cfg.Workers; i++ {
		d.workers.Add(1)
		go d.worker(ctx)
	}
	return d
}

// Sinks reports the configured destinations, for the status endpoint.
func (d *Dispatcher) Sinks() []string {
	out := make([]string, 0, len(d.sinks))
	for _, s := range d.sinks {
		out = append(out, s.Name())
	}
	return out
}

// Stats snapshots the counters.
func (d *Dispatcher) Stats() Stats {
	return Stats{
		Published: d.published.Load(),
		Failed:    d.failed.Load(),
		Dropped:   d.dropped.Load(),
		Queued:    d.queued.Load(),
	}
}

// Close stops accepting events, attempts what is already queued, and waits.
//
// The wait is bounded: a container stop must not hang on an unreachable
// webhook, so the drain uses a short detached context and gives up on whatever
// is still in flight afterwards. Anything not delivered is counted as dropped,
// which is the honest outcome — it is not coming back.
func (d *Dispatcher) Close() error {
	d.closeOne.Do(func() {
		d.cancel()
		d.workers.Wait()
	})
	return nil
}

func (d *Dispatcher) worker(ctx context.Context) {
	defer d.workers.Done()
	for {
		select {
		case ev := <-d.queue:
			d.queued.Add(-1)
			d.deliver(ctx, ev)
		case <-ctx.Done():
			d.drain()
			return
		}
	}
}

// drain attempts the events still buffered during shutdown. The worker context
// is already cancelled, so each attempt gets its own short-lived one; a sink
// that ignores deadlines would otherwise keep the process past the
// orchestrator's kill window.
func (d *Dispatcher) drain() {
	for {
		select {
		case ev := <-d.queue:
			d.queued.Add(-1)
			ctx, cancel := context.WithTimeout(context.Background(), drainTimeout)
			d.deliver(ctx, ev)
			cancel()
		default:
			return
		}
	}
}

func (d *Dispatcher) deliver(ctx context.Context, ev Event) {
	for _, s := range d.sinks {
		if err := s.Publish(ctx, ev); err != nil {
			d.failed.Add(1)
			d.log.Warn("integration delivery failed",
				"sink", s.Name(), "kind", ev.Kind, "dev_eui", ev.DeviceEUI, "err", err)
			continue
		}
		d.published.Add(1)
	}
}

// enqueue never blocks. A full buffer means a destination is not keeping up, and
// the honest response is a counter and a log line rather than a slower radio.
func (d *Dispatcher) enqueue(ev Event) {
	if len(d.sinks) == 0 {
		return
	}
	select {
	case d.queue <- ev:
		d.queued.Add(1)
	default:
		d.dropped.Add(1)
		d.log.Warn("dropping an integration event: the queue is full",
			"kind", ev.Kind, "dev_eui", ev.DeviceEUI, "queue", cap(d.queue))
	}
}

// RecordUplink implements engine.Recorder: store first, then fan out.
//
// The order matters. The store write is what the protocol depends on — a counter
// that is not persisted is a replay waiting to happen — so an integration that
// fails must never be able to prevent it.
func (d *Dispatcher) RecordUplink(ctx context.Context, r engine.UplinkRecord) error {
	if err := d.inner.RecordUplink(ctx, r); err != nil {
		return err
	}
	// A frame with no FPort carries MAC commands only. There is no application
	// payload in it, and emitting an empty event for every ADR exchange would
	// bury the traffic a consumer actually wants.
	if r.FPort < 0 {
		return nil
	}
	d.enqueue(Event{
		Kind: KindUplink, DeviceEUI: r.DevEUI.String(), DevAddr: r.DevAddr.String(),
		ReceivedAt: r.At, FCntUp: r.FCntUp, FPort: r.FPort,
		Data:           encodeBase64(r.FRMPayload),
		RawPayload:     encodeHex(r.RawPHYPayload),
		Retransmission: r.Retransmission, Acknowledged: r.ACK,
		GatewayID: r.GatewayID.String(), FrequencyHz: r.FrequencyHz,
		DataRate: r.DataRateIndex, RSSI: r.RSSI, SNR: r.SNR,
	})
	return nil
}

// RecordDownlink implements engine.Recorder. Downlink results are stored but not
// forwarded: whoever queued the downlink already knows about it, and a webhook
// per transmission is mostly noise for the consumer.
func (d *Dispatcher) RecordDownlink(ctx context.Context, r engine.DownlinkRecord) error {
	return d.inner.RecordDownlink(ctx, r)
}

// RecordEvent implements engine.Recorder, forwarding join acceptances so an
// application learns that its device is on the network.
func (d *Dispatcher) RecordEvent(ctx context.Context, ev engine.Event) error {
	if err := d.inner.RecordEvent(ctx, ev); err != nil {
		return err
	}
	if ev.Kind != engine.EventJoinAccept {
		return nil
	}
	d.enqueue(Event{
		Kind: KindJoin, DeviceEUI: ev.DevEUI.String(), DevAddr: ev.DevAddr.String(),
		ReceivedAt: ev.At, GatewayID: ev.GatewayID.String(),
	})
	return nil
}

// encodeBase64 renders a payload for JSON. nil stays empty rather than becoming
// the string "null" or an empty array marker, so a consumer can treat "no data"
// and "no field" the same way.
func encodeBase64(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString(b)
}

func encodeHex(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return hex.EncodeToString(b)
}

var _ engine.Recorder = (*Dispatcher)(nil)
