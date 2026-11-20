package integration

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/codec"
	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
)

// Payload decoding: device to model to formatter, off the radio path.
//
// Three lookups stand between an uplink and a decoded object — the device, its
// model, the model's compiled formatter — and a fleet does this on every frame.
// The registry caches the expensive part (compiling JavaScript) across devices of
// the same model; this file caches the cheap part too, because two round trips to
// Postgres per uplink is a cost a deployment feels, and invalidating on save keeps
// that cache correct without a TTL.

// ModelSource is the persistence a PayloadDecoder reads. Both methods are on
// engine's own ports, so a store that already serves the engine serves this too.
type ModelSource interface {
	DeviceByDevEUI(ctx context.Context, devEUI lorawan.EUI64) (*engine.Device, error)
	ModelByID(ctx context.Context, modelID string) (*engine.DeviceModel, error)
}

// DecodeResult is the outcome of running a formatter on one frame.
//
// The three states are distinct because the console has to render them
// differently: no formatter is a fact about the fleet's configuration, a decoded
// object is a reading, and an error is a symptom of a specific frame.
type DecodeResult struct {
	Decoded map[string]any
	// Err is why the formatter refused this frame, or why it could not be run
	// at all. Empty when Decoded is set or when no formatter is configured.
	Err string
	// Elapsed is how long the formatter ran, for the status page and for spotting
	// a script that is approaching its deadline.
	Elapsed time.Duration
}

// DecoderConfig configures a PayloadDecoder.
type DecoderConfig struct {
	// Timeout bounds one formatter run. It is separate from the codec's own
	// execution deadline: this one also covers the store lookups.
	Timeout time.Duration
	Logger  *slog.Logger
	// Options are the execution bounds handed to every compiled formatter.
	Options codec.Options
}

// PayloadDecoder resolves and runs payload formatters.
type PayloadDecoder struct {
	src     ModelSource
	reg     *codec.Registry
	log     *slog.Logger
	timeout time.Duration

	// The device-to-model cache is generation-tagged rather than individually
	// invalidated: any save or delete bumps the generation, and an entry from an
	// older generation is refetched. It over-invalidates — editing one device
	// drops every cached device — but the cache refills in one uplink per device,
	// and getting invalidation exactly right for a handful of entries is not
	// worth a bug that keeps serving a deleted formatter.
	mu       sync.RWMutex
	gen      int64
	byDevice map[lorawan.EUI64]decoderCacheEntry
}

type decoderCacheEntry struct {
	gen int64
	// dec is nil when the device has no model or the model has no formatter;
	// err is a compile failure cached against the model.
	dec codec.Decoder
	err string
}

// NewPayloadDecoder returns a decoder reading from src.
func NewPayloadDecoder(src ModelSource, cfg DecoderConfig) *PayloadDecoder {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 2 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &PayloadDecoder{
		src:      src,
		reg:      codec.NewRegistry(cfg.Options),
		log:      cfg.Logger,
		timeout:  cfg.Timeout,
		byDevice: map[lorawan.EUI64]decoderCacheEntry{},
	}
}

// Invalidate drops the cached device-to-model resolutions and a named model's
// compiled formatter. Call it after saving or deleting a device or a model so an
// edit takes effect without a restart.
func (p *PayloadDecoder) Invalidate(modelID string) {
	p.mu.Lock()
	p.gen++
	p.mu.Unlock()
	if modelID != "" {
		p.reg.Forget(modelID)
	}
}

// Stats reports the formatter cache for the status page.
func (p *PayloadDecoder) Stats() codec.RegistryStats { return p.reg.Stats() }

// Decode resolves the formatter for one uplink's device and runs it.
//
// It never returns an error value: a frame that cannot be decoded is still a
// frame, and the caller stores the outcome next to the raw bytes. The result's
// Err field carries what went wrong.
func (p *PayloadDecoder) Decode(ctx context.Context, r engine.UplinkRecord) DecodeResult {
	if p == nil || p.src == nil || len(r.FRMPayload) == 0 {
		return DecodeResult{}
	}
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	dec, cachedErr, err := p.decoderFor(ctx, r.DevEUI)
	if err != nil {
		return DecodeResult{Err: err.Error()}
	}
	if cachedErr != "" {
		return DecodeResult{Err: cachedErr}
	}
	if dec == nil {
		// No model, or a model with no formatter: not a failure, and the raw
		// payload still ships.
		return DecodeResult{}
	}

	start := time.Now()
	decoded, err := dec.Decode(ctx, codec.Input{
		Bytes:      r.FRMPayload,
		FPort:      r.FPort,
		ReceivedAt: r.At,
		DevEUI:     r.DevEUI.String(),
	})
	elapsed := time.Since(start)
	if err != nil {
		return DecodeResult{Err: err.Error(), Elapsed: elapsed}
	}
	return DecodeResult{Decoded: decoded, Elapsed: elapsed}
}

// decoderFor resolves a device to a usable formatter, through the cache.
func (p *PayloadDecoder) decoderFor(ctx context.Context, devEUI lorawan.EUI64,
) (dec codec.Decoder, cachedErr string, err error) {

	p.mu.RLock()
	e, ok := p.byDevice[devEUI]
	gen := p.gen
	p.mu.RUnlock()
	if ok && e.gen == gen {
		return e.dec, e.err, nil
	}

	dev, err := p.src.DeviceByDevEUI(ctx, devEUI)
	if err != nil {
		// Deliberately not cached: a device created a moment after a frame from
		// an unknown address must not stay unresolvable for the rest of the
		// process's life.
		return nil, "", err
	}
	if dev.ModelID == "" {
		p.store(devEUI, decoderCacheEntry{gen: gen})
		return nil, "", nil
	}

	model, err := p.src.ModelByID(ctx, dev.ModelID)
	if err != nil {
		// The model was deleted out from under the device. Uncached, so that
		// re-creating it heals without a restart.
		return nil, "", err
	}

	compiled, compErr := p.reg.DecoderFor(model.ModelID, codec.Kind(model.CodecKind), model.CodecScript)
	entry := decoderCacheEntry{gen: gen, dec: compiled}
	if compErr != nil {
		// A broken formatter is cached: it will not fix itself between uplinks,
		// and recompiling it per frame spends CPU rediscovering one typo.
		entry.err = compErr.Error()
		entry.dec = nil
		p.log.Warn("device model has an unusable payload formatter",
			"model_id", model.ModelID, "err", compErr)
	}
	p.store(devEUI, entry)
	return entry.dec, entry.err, nil
}

func (p *PayloadDecoder) store(devEUI lorawan.EUI64, e decoderCacheEntry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.byDevice[devEUI] = e
}

// JSON renders a decode result for storage and for JSON transport.
//
// Encoding happens here rather than in the store so the same bytes go to the
// database column and to the webhook, and a formatter that returns something
// unmarshalable — a circular reference, a function — is reported as a decode
// error instead of failing the row write.
func (res DecodeResult) JSON() (raw []byte, decodeErr string, err error) {
	if res.Err != "" {
		return nil, res.Err, nil
	}
	if len(res.Decoded) == 0 {
		return nil, "", nil
	}
	out, err := json.Marshal(res.Decoded)
	if err != nil {
		return nil, "", err
	}
	return out, "", nil
}
