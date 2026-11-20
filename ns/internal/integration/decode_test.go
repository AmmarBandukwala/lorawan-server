package integration

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
)

// fakeModels is a ModelSource with call counting, so the cache can be observed
// rather than assumed.
type fakeModels struct {
	devices     map[lorawan.EUI64]*engine.Device
	models      map[string]*engine.DeviceModel
	deviceCalls int
	modelCalls  int
}

func (f *fakeModels) DeviceByDevEUI(_ context.Context, eui lorawan.EUI64) (*engine.Device, error) {
	f.deviceCalls++
	d, ok := f.devices[eui]
	if !ok {
		return nil, engine.ErrNotFound
	}
	return d, nil
}

func (f *fakeModels) ModelByID(_ context.Context, id string) (*engine.DeviceModel, error) {
	f.modelCalls++
	m, ok := f.models[id]
	if !ok {
		return nil, engine.ErrNotFound
	}
	return m, nil
}

func euiMust(s string) lorawan.EUI64 {
	v, _ := lorawan.ParseEUI64(s)
	return v
}

const probeScript = `function decodeUplink(input) {
  return { data: { temperature: (input.bytes[0] | (input.bytes[1] << 8)) / 10 } };
}`

func newProbeFixture() (*fakeModels, lorawan.EUI64) {
	eui := euiMust("0011223344556677")
	return &fakeModels{
		devices: map[lorawan.EUI64]*engine.Device{
			eui: {DevEUI: eui, ModelID: "cold-chain-probe"},
		},
		models: map[string]*engine.DeviceModel{
			"cold-chain-probe": {ModelID: "cold-chain-probe", CodecKind: "JS", CodecScript: probeScript},
		},
	}, eui
}

func probeRecord(eui lorawan.EUI64) engine.UplinkRecord {
	return engine.UplinkRecord{
		At: time.Now(), DevEUI: eui, FCntUp: 1, FPort: 10,
		FRMPayload: []byte{0x64, 0x00}, // 100 -> 10.0 degrees
	}
}

func TestPayloadDecoderDecodesByModel(t *testing.T) {
	src, eui := newProbeFixture()
	p := NewPayloadDecoder(src, DecoderConfig{Logger: quietLogger()})

	res := p.Decode(context.Background(), probeRecord(eui))
	if res.Err != "" {
		t.Fatalf("decode error: %s", res.Err)
	}
	if v, ok := num(res.Decoded["temperature"]); !ok || v != 10.0 {
		t.Errorf("decoded = %v, want temperature 10.0", res.Decoded)
	}
	if res.Elapsed <= 0 {
		t.Error("Elapsed must record how long the formatter ran")
	}
}

// TestPayloadDecoderCachesLookups is the performance guarantee: without it every
// frame costs two queries, which is the first thing a deployment feels.
func TestPayloadDecoderCachesLookups(t *testing.T) {
	src, eui := newProbeFixture()
	p := NewPayloadDecoder(src, DecoderConfig{Logger: quietLogger()})

	for i := 0; i < 5; i++ {
		if res := p.Decode(context.Background(), probeRecord(eui)); res.Err != "" {
			t.Fatalf("decode %d: %s", i, res.Err)
		}
	}
	if src.deviceCalls != 1 || src.modelCalls != 1 {
		t.Errorf("lookups = %d device / %d model after 5 frames, want 1 each",
			src.deviceCalls, src.modelCalls)
	}

	// An edit must be visible on the very next frame, not after a TTL.
	p.Invalidate("cold-chain-probe")
	src.models["cold-chain-probe"].CodecScript =
		`function decodeUplink(i) { return { data: { temperature: 99 } }; }`
	res := p.Decode(context.Background(), probeRecord(eui))
	if res.Err != "" {
		t.Fatalf("decode after edit: %s", res.Err)
	}
	if v, ok := num(res.Decoded["temperature"]); !ok || v != 99.0 {
		t.Errorf("decoded = %v, want the edited formatter's 99", res.Decoded)
	}
}

// TestDeviceWithoutModelIsNotAnError: most fleets have devices nobody has
// described yet, and their frames must stay clean rather than noisy.
func TestDeviceWithoutModelIsNotAnError(t *testing.T) {
	eui := euiMust("0011223344556677")
	src := &fakeModels{devices: map[lorawan.EUI64]*engine.Device{eui: {DevEUI: eui}}}
	p := NewPayloadDecoder(src, DecoderConfig{Logger: quietLogger()})

	res := p.Decode(context.Background(), probeRecord(eui))
	if res.Err != "" || res.Decoded != nil {
		t.Errorf("res = %+v, want an empty result with no error", res)
	}
	if src.modelCalls != 0 {
		t.Errorf("looked up %d models for a device that has none", src.modelCalls)
	}
}

func TestUnknownDeviceReportsWithoutCaching(t *testing.T) {
	src := &fakeModels{
		devices: map[lorawan.EUI64]*engine.Device{},
		models:  map[string]*engine.DeviceModel{},
	}
	p := NewPayloadDecoder(src, DecoderConfig{Logger: quietLogger()})
	eui := euiMust("aabbccddeeff0011")

	if res := p.Decode(context.Background(), probeRecord(eui)); res.Err == "" {
		t.Fatal("an unknown device must be reported")
	}
	// Provisioning the device afterwards must heal immediately.
	src.devices[eui] = &engine.Device{DevEUI: eui, ModelID: "cold-chain-probe"}
	src.models["cold-chain-probe"] = &engine.DeviceModel{
		ModelID: "cold-chain-probe", CodecKind: "JS", CodecScript: probeScript,
	}
	if res := p.Decode(context.Background(), probeRecord(eui)); res.Err != "" {
		t.Errorf("after provisioning, decode still fails: %s", res.Err)
	}
}

// TestDeletedModelHealsWithoutRestart: the foreign key sets devices free rather
// than deleting them, so a device whose model vanished must not stay broken
// after the model is recreated.
func TestDeletedModelHealsWithoutRestart(t *testing.T) {
	src, eui := newProbeFixture()
	p := NewPayloadDecoder(src, DecoderConfig{Logger: quietLogger()})

	delete(src.models, "cold-chain-probe")
	if res := p.Decode(context.Background(), probeRecord(eui)); res.Err == "" {
		t.Fatal("a device whose model disappeared must report it")
	}
	src.models["cold-chain-probe"] = &engine.DeviceModel{
		ModelID: "cold-chain-probe", CodecKind: "JS", CodecScript: probeScript,
	}
	if res := p.Decode(context.Background(), probeRecord(eui)); res.Err != "" {
		t.Errorf("recreating the model did not heal the device: %s", res.Err)
	}
}

func TestBrokenFormatterReportsOnEveryFrame(t *testing.T) {
	src, eui := newProbeFixture()
	src.models["cold-chain-probe"].CodecScript = "function decodeUplink( { syntax"
	p := NewPayloadDecoder(src, DecoderConfig{Logger: quietLogger()})

	for i := 0; i < 3; i++ {
		res := p.Decode(context.Background(), probeRecord(eui))
		if res.Err == "" {
			t.Fatalf("frame %d: a broken formatter must report on every frame", i)
		}
	}
	if got := p.Stats().Broken; got != 1 {
		t.Errorf("broken formatters = %d, want 1", got)
	}
}

// TestFormatterRuntimeErrorIsPerFrame: one malformed frame must not stop the
// device's other frames being decoded.
func TestFormatterRuntimeErrorIsPerFrame(t *testing.T) {
	src, eui := newProbeFixture()
	src.models["cold-chain-probe"].CodecScript = `function decodeUplink(input) {
  if (input.bytes[0] === 0xff) { throw new Error("sensor fault flag set"); }
  return { data: { temperature: 1 } };
}`
	p := NewPayloadDecoder(src, DecoderConfig{Logger: quietLogger()})

	bad := probeRecord(eui)
	bad.FRMPayload = []byte{0xff}
	if res := p.Decode(context.Background(), bad); res.Err == "" {
		t.Fatal("the formatter's error must reach the caller")
	}
	if res := p.Decode(context.Background(), probeRecord(eui)); res.Err != "" {
		t.Errorf("a later good frame failed: %s", res.Err)
	}
}

func TestDecodeResultJSON(t *testing.T) {
	res := DecodeResult{Decoded: map[string]any{"temperature": 10.0}}
	raw, decodeErr, err := res.JSON()
	if err != nil || decodeErr != "" {
		t.Fatalf("JSON: %v / %s", err, decodeErr)
	}
	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("the JSON is not parseable: %v", err)
	}
	if back["temperature"] != 10.0 {
		t.Errorf("round trip = %v", back)
	}

	// An error takes precedence and produces no payload.
	raw, decodeErr, err = DecodeResult{Err: "boom", Decoded: map[string]any{"x": 1}}.JSON()
	if err != nil || decodeErr != "boom" || raw != nil {
		t.Errorf("error case = %q / %q / %v", raw, decodeErr, err)
	}

	// Empty means "no formatter", which is neither a payload nor an error.
	raw, decodeErr, err = DecodeResult{}.JSON()
	if err != nil || decodeErr != "" || raw != nil {
		t.Errorf("empty case = %q / %q / %v", raw, decodeErr, err)
	}
}

func TestDecodeSkippedWithoutPayload(t *testing.T) {
	src, eui := newProbeFixture()
	p := NewPayloadDecoder(src, DecoderConfig{Logger: quietLogger()})

	r := probeRecord(eui)
	r.FRMPayload = nil
	if res := p.Decode(context.Background(), r); res.Err != "" || res.Decoded != nil {
		t.Errorf("res = %+v, want nothing done for an empty payload", res)
	}
	if src.deviceCalls != 0 {
		t.Error("an empty payload must not even resolve the device")
	}
}

// TestDecodeRespectsItsDeadline covers the case the whole design exists for: a
// formatter that hangs must cost one frame, not a worker.
func TestDecodeRespectsItsDeadline(t *testing.T) {
	src, eui := newProbeFixture()
	src.models["cold-chain-probe"].CodecScript = `function decodeUplink(i) { for (;;) {} }`
	p := NewPayloadDecoder(src, DecoderConfig{Timeout: 200 * time.Millisecond, Logger: quietLogger()})

	done := make(chan DecodeResult, 1)
	go func() { done <- p.Decode(context.Background(), probeRecord(eui)) }()
	select {
	case res := <-done:
		if res.Err == "" {
			t.Fatal("an infinite loop must be reported as a failure")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the decoder ignored its own deadline")
	}
}

// --- dispatcher integration ---

// capturePersister stands in for pgstore's decode write.
type capturePersister struct {
	mu   sync.Mutex
	rows []capturedDecode
}

type capturedDecode struct {
	devEUI      lorawan.EUI64
	keyEpoch    uint32
	fCntUp      uint32
	decoded     []byte
	decodeError string
}

func (c *capturePersister) UpdateUplinkDecoded(_ context.Context, devEUI lorawan.EUI64,
	keyEpoch, fCntUp uint32, decoded []byte, decodeError string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rows = append(c.rows, capturedDecode{devEUI, keyEpoch, fCntUp, decoded, decodeError})
	return nil
}

func (c *capturePersister) all() []capturedDecode {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]capturedDecode(nil), c.rows...)
}

// TestDispatcherDecodesOffTheRadioPath is the point of the whole arrangement:
// RecordUplink returns promptly even though the formatter is slow, and the
// decoded reading still reaches the sink and the store.
func TestDispatcherDecodesOffTheRadioPath(t *testing.T) {
	src, eui := newProbeFixture()
	p := NewPayloadDecoder(src, DecoderConfig{Logger: quietLogger()})

	store := engine.NewMemStore()
	sink := &recordingSink{name: "sink"}
	d := New(store, []Sink{sink}, Config{Logger: quietLogger()})
	persist := &capturePersister{}
	d.EnableDecoding(p, persist)
	defer d.Close()

	if err := d.RecordUplink(context.Background(), probeRecord(eui)); err != nil {
		t.Fatalf("RecordUplink: %v", err)
	}

	waitFor(t, "the decoded event", func() bool { return len(sink.events()) == 1 })
	ev := sink.events()[0]
	if v, ok := num(ev.Decoded["temperature"]); !ok || v != 10.0 {
		t.Errorf("event carried no reading: %+v", ev)
	}
	if ev.Data == "" {
		t.Error("the raw payload must still ship alongside the reading")
	}

	waitFor(t, "the stored decode outcome", func() bool { return len(persist.all()) == 1 })
	row := persist.all()[0]
	if row.decodeError != "" || len(row.decoded) == 0 {
		t.Errorf("stored outcome = %+v", row)
	}
	if got := d.Stats(); got.Decoded != 1 || got.DecodeErrors != 0 {
		t.Errorf("stats = %+v, want 1 decoded", got)
	}
}

// TestDispatcherRecordsDecodeFailureSeparatelyFromNoFormatter: the console has to
// tell "nobody configured a decoder" apart from "the decoder rejected this
// frame", because only one of those is a fault.
func TestDispatcherRecordsDecodeFailureSeparatelyFromNoFormatter(t *testing.T) {
	src, eui := newProbeFixture()
	src.models["cold-chain-probe"].CodecScript = "function decodeUplink( { broken"
	p := NewPayloadDecoder(src, DecoderConfig{Logger: quietLogger()})

	store := engine.NewMemStore()
	sink := &recordingSink{name: "sink"}
	d := New(store, []Sink{sink}, Config{Logger: quietLogger()})
	persist := &capturePersister{}
	d.EnableDecoding(p, persist)
	defer d.Close()

	if err := d.RecordUplink(context.Background(), probeRecord(eui)); err != nil {
		t.Fatalf("RecordUplink: %v", err)
	}
	waitFor(t, "the failed event", func() bool { return len(sink.events()) == 1 })

	ev := sink.events()[0]
	if ev.DecodeError == "" {
		t.Error("the event must carry the formatter's failure")
	}
	if ev.Data == "" {
		t.Error("a frame the formatter rejected must still deliver its raw bytes")
	}
	waitFor(t, "the stored failure", func() bool { return len(persist.all()) == 1 })
	if row := persist.all()[0]; row.decodeError == "" || len(row.decoded) != 0 {
		t.Errorf("stored failure = %+v, want an error and no decoded payload", row)
	}
	if got := d.Stats(); got.DecodeErrors != 1 || got.Decoded != 0 {
		t.Errorf("stats = %+v, want 1 decode error", got)
	}
}

// TestDispatcherWithoutADecoderIsUnchanged guards the default deployment: no
// models, no formatters, and nothing extra happening on the path.
func TestDispatcherWithoutADecoderIsUnchanged(t *testing.T) {
	store := engine.NewMemStore()
	sink := &recordingSink{name: "sink"}
	d := New(store, []Sink{sink}, Config{Logger: quietLogger()})
	defer d.Close()

	if err := d.RecordUplink(context.Background(), probeRecord(euiMust("0011223344556677"))); err != nil {
		t.Fatalf("RecordUplink: %v", err)
	}
	waitFor(t, "the event", func() bool { return len(sink.events()) == 1 })
	ev := sink.events()[0]
	if ev.Decoded != nil || ev.DecodeError != "" {
		t.Errorf("no decoder was configured, yet the event says %+v / %q", ev.Decoded, ev.DecodeError)
	}
	if got := d.Stats(); got.Decoded != 0 || got.DecodeErrors != 0 {
		t.Errorf("stats = %+v", got)
	}
}

// TestDecodingRunsEvenWithNoSinks: the console reads the stored reading, so a
// deployment with no webhook still needs decoding to happen.
func TestDecodingRunsEvenWithNoSinks(t *testing.T) {
	src, eui := newProbeFixture()
	p := NewPayloadDecoder(src, DecoderConfig{Logger: quietLogger()})

	store := engine.NewMemStore()
	d := New(store, nil, Config{Logger: quietLogger()})
	persist := &capturePersister{}
	d.EnableDecoding(p, persist)
	defer d.Close()

	if err := d.RecordUplink(context.Background(), probeRecord(eui)); err != nil {
		t.Fatalf("RecordUplink: %v", err)
	}
	waitFor(t, "the stored reading", func() bool { return len(persist.all()) == 1 })
	if got := d.Stats(); got.Decoded != 1 {
		t.Errorf("stats = %+v, want 1 decoded with no sinks configured", got)
	}
}

// num coerces a formatter's number, which goja exports as int64, int or float64
// depending on how the script produced it. Comparing against a float64 literal
// directly would fail on the integer cases for reasons that have nothing to do
// with the decoder.
func num(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint64:
		return float64(n), true
	default:
		return 0, false
	}
}
