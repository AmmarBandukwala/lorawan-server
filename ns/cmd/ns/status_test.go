package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/gateway"
	"github.com/fieldnode/lorawan-server/ns/internal/integration"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
)

func quietStatusLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// statusDecodeView mirrors only the part of /status this test cares about, so a
// rename anywhere else in the payload does not read as a failure here.
type statusDecodeView struct {
	Integrations *struct {
		Decoding *struct {
			Enabled      bool  `json:"enabled"`
			FramesFailed int64 `json:"frames_failed"`
			Broken       int   `json:"broken_formatters"`
			BrokenModels []struct {
				ModelID string `json:"model_id"`
				Reason  string `json:"reason"`
			} `json:"broken_models"`
		} `json:"decoding"`
	} `json:"integrations"`
}

// TestStatusNamesTheModelThatCannotDecode is the reason the status payload
// carries model IDs rather than a count.
//
// A formatter that will not compile is the least visible failure this server
// has: the device joins, reports on schedule, and its signal looks fine, while
// every frame is stored as bytes nobody can read. "broken_formatters: 1" tells
// an operator that something is wrong somewhere; naming the model and quoting
// the compiler is what makes it fixable from a browser.
func TestStatusNamesTheModelThatCannotDecode(t *testing.T) {
	ctx := context.Background()
	store := engine.NewMemStore()

	devEUI := mustEUI(t, "0011223344556677")
	appKey, err := lorawan.ParseAES128Key("2b7e151628aed2a6abf7158809cf4f3c")
	if err != nil {
		t.Fatalf("parse the app key: %v", err)
	}
	dev := engine.NewOTAADevice(devEUI, mustEUI(t, "0102030405060708"), appKey)
	dev.ModelID = "aqi-pro"
	if err := store.SaveDevice(ctx, dev); err != nil {
		t.Fatalf("SaveDevice: %v", err)
	}
	if err := store.SaveModel(ctx, &engine.DeviceModel{
		ModelID:     "aqi-pro",
		Name:        "Air quality probe",
		CodecKind:   "JS",
		CodecScript: "function decodeUplink(input) { this is not javascript",
	}); err != nil {
		t.Fatalf("SaveModel: %v", err)
	}

	eng, err := engine.New(engine.DefaultConfig(), store.Store(), &gateway.Lazy{},
		engine.WithLogger(quietStatusLog()))
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}

	dispatcher := integration.New(store, nil, integration.Config{
		Logger: quietStatusLog(),
	})
	defer dispatcher.Close()
	dispatcher.EnableDecoding(integration.NewPayloadDecoder(store,
		integration.DecoderConfig{Logger: quietStatusLog()}), nil)

	srv := newStatusServer("127.0.0.1:0", quietStatusLog(), gateway.NewRouter(),
		eng, dispatcher, nil)

	// One frame is what it takes: the registry only learns a model is broken by
	// being asked to use it, which is why the panel says these counters cover
	// traffic rather than the whole fleet.
	if err := dispatcher.RecordUplink(ctx, engine.UplinkRecord{
		At: time.Now(), DevEUI: devEUI, KeyEpoch: 1, FCntUp: 1,
		FPort: 1, FRMPayload: []byte{0x01, 0x02},
	}); err != nil {
		t.Fatalf("RecordUplink: %v", err)
	}

	read := func() statusDecodeView {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.handleStatus(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status returned %d: %s", rec.Code, rec.Body.String())
		}
		var out statusDecodeView
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode the status payload: %v", err)
		}
		return out
	}

	deadline := time.Now().Add(3 * time.Second)
	var got statusDecodeView
	for time.Now().Before(deadline) {
		got = read()
		if got.Integrations != nil && got.Integrations.Decoding != nil &&
			got.Integrations.Decoding.Broken > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if got.Integrations == nil || got.Integrations.Decoding == nil {
		t.Fatalf("the status payload reports no decoding at all: %+v", got.Integrations)
	}
	dec := got.Integrations.Decoding
	if !dec.Enabled {
		t.Error("decoding must be reported as enabled once a decoder is installed")
	}
	if dec.Broken != 1 {
		t.Fatalf("broken_formatters = %d, want 1", dec.Broken)
	}
	if dec.FramesFailed != 1 {
		t.Errorf("frames_failed = %d, want 1", dec.FramesFailed)
	}
	if len(dec.BrokenModels) != 1 {
		t.Fatalf("broken_models holds %d entries, want 1: the count alone is not actionable",
			len(dec.BrokenModels))
	}
	if dec.BrokenModels[0].ModelID != "aqi-pro" {
		t.Errorf("broken_models[0].model_id = %q, want \"aqi-pro\"", dec.BrokenModels[0].ModelID)
	}
	if dec.BrokenModels[0].Reason == "" {
		t.Error("the compiler's own message must come back, or the operator has to guess")
	}
}

// TestStatusOmitsDecodingWithoutADecoder: a build with no integrations must not
// advertise a decoding section the console would then render as a wall of zeros.
func TestStatusOmitsDecodingWithoutADecoder(t *testing.T) {
	store := engine.NewMemStore()
	eng, err := engine.New(engine.DefaultConfig(), store.Store(), &gateway.Lazy{},
		engine.WithLogger(quietStatusLog()))
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	srv := newStatusServer("127.0.0.1:0", quietStatusLog(), gateway.NewRouter(), eng, nil, nil)

	rec := httptest.NewRecorder()
	srv.handleStatus(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status returned %d", rec.Code)
	}
	// No integrations section at all, rather than one full of zeroes that reads
	// like a healthy fleet with nothing to report.
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode the status payload: %v", err)
	}
	if _, ok := raw["integrations"]; ok {
		t.Errorf("integrations must be absent without a dispatcher: %s", rec.Body.String())
	}
}
