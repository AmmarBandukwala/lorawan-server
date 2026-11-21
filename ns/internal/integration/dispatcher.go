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
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/codec"
	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
)

// Event kinds an application can receive.
const (
	KindUplink = "uplink"
	KindJoin   = "join"

	// KindIntegrationTest marks a probe published by the panel's test button.
	// It is a distinct kind rather than a flag on a real event so a consumer can
	// filter test traffic out entirely, and so a test can never be mistaken for
	// a device reading.
	KindIntegrationTest = "integration-test"
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

	// Decoded is the device model's payload formatter reading of Data, and
	// DecodeError why it refused. Both absent means no formatter is configured,
	// which a consumer should treat as "decode this yourself" rather than as a
	// device fault.
	//
	// Data stays present regardless: the formatter is vendor code, and a consumer
	// that disagrees with its interpretation still has the bytes to do better.
	Decoded     map[string]any `json:"decoded,omitempty"`
	DecodeError string         `json:"decode_error,omitempty"`
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

	// sinks is swapped atomically when the operator edits integrations, so a
	// config change takes effect without restarting the process or pausing the
	// radio path. Readers load the pointer once per delivery and iterate a slice
	// that is never mutated in place — a swap always installs a fresh slice.
	sinks atomic.Pointer[sinkTable]

	// decoder turns an uplink's bytes into JSON. It runs in the worker, never on
	// the radio path, so a formatter with a 100 ms deadline cannot delay a frame.
	decoder *PayloadDecoder
	// persist stores that reading against the row that is already written.
	persist DecodePersister

	queue chan job

	// Counters are read for a status page, where a slightly stale value is fine
	// and a mutex on every uplink is not.
	published    atomic.Int64
	failed       atomic.Int64
	dropped      atomic.Int64
	queued       atomic.Int64
	decoded      atomic.Int64
	decodeFailed atomic.Int64

	log      *slog.Logger
	cancel   context.CancelFunc
	workers  sync.WaitGroup
	closeOne sync.Once
}

// sinkTable is the immutable set of destinations. Replacing the whole table
// rather than editing a slice is what makes a hot swap safe to read from workers
// without a lock.
type sinkTable struct {
	list []Sink
}

// SetSinks replaces the delivery destinations at runtime and closes the ones
// being dropped, so a removed MQTT sink stops reconnecting to a broker nobody
// configured any more.
//
// Closing the replaced sinks happens before the new table is installed only for
// the removed ones; sinks that survive the edit are handed over untouched and
// keep their connections, which is why the caller passes the same *Webhook or
// *MQTT instances where a destination did not change.
func (d *Dispatcher) SetSinks(sinks []Sink) {
	old := d.sinks.Swap(&sinkTable{list: sinks})
	if old == nil {
		return
	}
	keep := map[Sink]struct{}{}
	for _, s := range sinks {
		keep[s] = struct{}{}
	}
	for _, s := range old.list {
		if _, still := keep[s]; still {
			continue
		}
		if c, ok := s.(interface{ Close() error }); ok {
			if err := c.Close(); err != nil {
				d.log.Warn("closing a replaced integration failed", "sink", s.Name(), "err", err)
			}
		}
	}
}

// job is one unit of work for a worker: the event to deliver, and the uplink
// record to decode when there is one.
//
// The record travels alongside the event rather than being decoded in
// RecordUplink because decoding must not run on the caller's goroutine; the
// record is what the decoder needs, so it has to cross the channel too.
type job struct {
	ev  Event
	rec *engine.UplinkRecord
}

// DecodePersister stores a formatter's reading against an already-written
// uplink. pgstore implements it; a store that does not simply leaves the reading
// to the webhooks.
type DecodePersister interface {
	UpdateUplinkDecoded(ctx context.Context, devEUI lorawan.EUI64,
		keyEpoch, fCntUp uint32, decoded []byte, decodeError string) error
}

// Stats is the dispatcher's view of its own health, for /status and for logs.
type Stats struct {
	Published int64 `json:"published"`
	Failed    int64 `json:"delivery_failures"`
	Dropped   int64 `json:"dropped"`
	Queued    int64 `json:"queued"`

	// Decoded counts frames a formatter produced a reading for, and
	// DecodeErrors frames it rejected or could not run. A DecodeErrors count that
	// grows while a fleet is otherwise healthy is a broken formatter, and this is
	// the only place that shows it.
	Decoded      int64 `json:"decoded"`
	DecodeErrors int64 `json:"decode_errors"`
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
		queue: make(chan job, cfg.BufferSize),
		log:   cfg.Logger,
	}
	d.sinks.Store(&sinkTable{list: sinks})
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	for i := 0; i < cfg.Workers; i++ {
		d.workers.Add(1)
		go d.worker(ctx)
	}
	return d
}

// EnableDecoding installs payload-formatter decoding and where to store its
// readings. It is separate from New because the decoder needs the same store the
// dispatcher wraps, which cannot be passed in before the dispatcher exists.
//
// Calling it also means the queue is used for uplinks even with no sinks
// configured: decoding is for the console and the database, not only for
// webhooks.
func (d *Dispatcher) EnableDecoding(p *PayloadDecoder, persist DecodePersister) {
	d.decoder = p
	d.persist = persist
}

// InvalidateDecoder tells the live payload decoder that a device or model
// changed, so the next frame resolves the new formatter instead of serving the
// cached one. Passing a model ID also drops that model's compiled script.
//
// The API calls this after every save or delete. Without it an operator edits a
// formatter, sees no change for the TTL of some cache they forgot existed, and
// concludes the console is broken.
func (d *Dispatcher) InvalidateDecoder(modelID string) {
	if d.decoder == nil {
		return
	}
	d.decoder.Invalidate(modelID)
}

// FormatterStats reports the payload-formatter cache, or nil when decoding is not
// enabled. A non-zero Broken count means some model's script does not compile,
// which is otherwise visible only as a decode error on individual frames.
func (d *Dispatcher) FormatterStats() *codec.RegistryStats {
	if d.decoder == nil {
		return nil
	}
	st := d.decoder.Stats()
	return &st
}

// DecodingEnabled reports whether payload formatters are being run.
func (d *Dispatcher) DecodingEnabled() bool { return d.decoder != nil }

// currentSinks reads the table once. Every caller iterates that snapshot, so a
// concurrent SetSinks cannot change the set mid-delivery.
func (d *Dispatcher) currentSinks() []Sink {
	if t := d.sinks.Load(); t != nil {
		return t.list
	}
	return nil
}

// Sinks reports the configured destinations, for the status endpoint.
func (d *Dispatcher) Sinks() []string {
	list := d.currentSinks()
	out := make([]string, 0, len(list))
	for _, s := range list {
		out = append(out, s.Name())
	}
	return out
}

// Stats snapshots the counters.
func (d *Dispatcher) Stats() Stats {
	return Stats{
		Published:    d.published.Load(),
		Failed:       d.failed.Load(),
		Dropped:      d.dropped.Load(),
		Queued:       d.queued.Load(),
		Decoded:      d.decoded.Load(),
		DecodeErrors: d.decodeFailed.Load(),
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
		case j := <-d.queue:
			d.queued.Add(-1)
			d.run(ctx, j)
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
		case j := <-d.queue:
			d.queued.Add(-1)
			ctx, cancel := context.WithTimeout(context.Background(), drainTimeout)
			d.run(ctx, j)
			cancel()
		default:
			return
		}
	}
}

// run decodes an uplink and then delivers it.
//
// Decoding comes first so the event a consumer receives carries the reading, and
// a formatter failure cannot stop delivery: the raw payload is the contract, the
// decoded object is a convenience.
func (d *Dispatcher) run(ctx context.Context, j job) {
	if j.rec != nil && d.decoder != nil {
		res := d.decoder.Decode(ctx, *j.rec)
		raw, decodeErr, marshalErr := res.JSON()
		switch {
		case marshalErr != nil:
			// A formatter that returns something JSON cannot represent is
			// reported the same way as a runtime error, because to the operator
			// it is the same problem.
			decodeErr = marshalErr.Error()
			d.decodeFailed.Add(1)
		case decodeErr != "":
			d.decodeFailed.Add(1)
		case len(raw) > 0:
			d.decoded.Add(1)
		}
		j.ev.Decoded = res.Decoded
		j.ev.DecodeError = decodeErr
		if decodeErr != "" {
			d.log.Debug("payload formatter rejected a frame",
				"dev_eui", j.ev.DeviceEUI, "f_cnt_up", j.ev.FCntUp, "err", decodeErr)
		}

		if d.persist != nil && (len(raw) > 0 || decodeErr != "") {
			// A failed write here costs the console its decoded view of one
			// frame; it must not stop the webhook that already has the reading.
			if err := d.persist.UpdateUplinkDecoded(ctx, j.rec.DevEUI,
				j.rec.KeyEpoch, j.rec.FCntUp, raw, decodeErr); err != nil {
				d.log.Warn("cannot store a decode outcome", "dev_eui", j.ev.DeviceEUI, "err", err)
			}
		}
	}
	d.deliver(ctx, j.ev)
}

func (d *Dispatcher) deliver(ctx context.Context, ev Event) {
	for _, s := range d.currentSinks() {
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
func (d *Dispatcher) enqueue(j job) {
	// With neither a sink to deliver to nor a formatter to run, the queue would
	// only add a copy and a goroutine hand-off between two no-ops. Read live:
	// integrations can be added at runtime, so this must not be decided once.
	if len(d.currentSinks()) == 0 && d.decoder == nil {
		return
	}
	select {
	case d.queue <- j:
		d.queued.Add(1)
	default:
		d.dropped.Add(1)
		d.log.Warn("dropping an integration event: the queue is full",
			"kind", j.ev.Kind, "dev_eui", j.ev.DeviceEUI, "queue", cap(d.queue))
	}
}

// RecordUplink implements engine.Recorder: store first, then fan out.
//
// The order matters. The store write is what the protocol depends on — a counter
// that is not persisted is a replay waiting to happen — so an integration that
// fails must never be able to prevent it.
//
// The record is handed to the worker as well, because decoding vendor formatters
// belongs off this goroutine: a formatter is allowed 100 ms, and an uplink
// handler that spends it is an uplink handler that misses the next RX window.
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
	ev := Event{
		Kind: KindUplink, DeviceEUI: r.DevEUI.String(), DevAddr: r.DevAddr.String(),
		ReceivedAt: r.At, FCntUp: r.FCntUp, FPort: r.FPort,
		Data:           encodeBase64(r.FRMPayload),
		RawPayload:     encodeHex(r.RawPHYPayload),
		Retransmission: r.Retransmission, Acknowledged: r.ACK,
		GatewayID: r.GatewayID.String(), FrequencyHz: r.FrequencyHz,
		DataRate: r.DataRateIndex, RSSI: r.RSSI, SNR: r.SNR,
	}
	// Only frames with an application payload can be decoded; a zero-length one
	// would only produce a formatter error.
	var rec *engine.UplinkRecord
	if d.decoder != nil && len(r.FRMPayload) > 0 {
		rec = &r
	}
	d.enqueue(job{ev: ev, rec: rec})
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
	d.enqueue(job{ev: Event{
		Kind: KindJoin, DeviceEUI: ev.DevEUI.String(), DevAddr: ev.DevAddr.String(),
		ReceivedAt: ev.At, GatewayID: ev.GatewayID.String(),
	}})
	return nil
}

// encodeBase64 renders a payload for JSON. nil stays empty rather than becoming
// the string "null" or an empty array marker, so a consumer can treat "no data"
// and "no field" the same way.
// marshalEvent renders the JSON body every sink sends.
//
// One function rather than a json.Marshal at each sink because the body is the
// public contract: a consumer subscribing to MQTT and a consumer receiving a
// webhook should be able to parse the same object. Letting two sinks each
// marshal Event independently is how they quietly diverge.
func marshalEvent(ev Event) ([]byte, error) {
	body, err := json.Marshal(ev)
	if err != nil {
		return nil, fmt.Errorf("integration: encode event: %w", err)
	}
	return body, nil
}

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
