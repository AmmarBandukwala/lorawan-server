package codec

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

const probeFormatter = `function decodeUplink(input) {
  return { data: { temperature: (input.bytes[0] | (input.bytes[1] << 8)) / 10 } };
}`

func TestRegistryCompilesOncePerModel(t *testing.T) {
	r := NewRegistry(Options{})

	dec1, err := r.DecoderFor("probe", KindJS, probeFormatter)
	if err != nil {
		t.Fatalf("first compile: %v", err)
	}
	dec2, err := r.DecoderFor("probe", KindJS, probeFormatter)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	// The same instance means the program was not recompiled, which is the entire
	// reason this cache exists.
	if dec1 != dec2 {
		t.Error("the same model recompiled; the cache is not being used")
	}
	if got := r.Stats(); got.Compiled != 1 || got.Broken != 0 {
		t.Errorf("stats = %+v, want 1 compiled", got)
	}
}

func TestRegistryRecompilesWhenTheScriptChanges(t *testing.T) {
	r := NewRegistry(Options{})
	edited := strings.Replace(probeFormatter, "/ 10", "/ 100", 1)

	first, err := r.DecoderFor("probe", KindJS, probeFormatter)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := r.DecoderFor("probe", KindJS, edited)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first == second {
		t.Error("an edited formatter must not keep serving the old program")
	}

	// And the new one must actually be the new one.
	out, err := second.Decode(context.Background(), Input{Bytes: []byte{0x64, 0x00}})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	wantNum(t, out, "temperature", 1.0)
}

// TestBrokenScriptIsCachedNotRetried is about cost: a typo in a formatter would
// otherwise be rediscovered by the compiler on every frame the fleet reports.
func TestBrokenScriptIsCachedNotRetried(t *testing.T) {
	r := NewRegistry(Options{})
	broken := "function decodeUplink(input) { this is not javascript"

	dec, err := r.DecoderFor("broken", KindJS, broken)
	if err == nil {
		t.Fatal("a syntax error must be reported")
	}
	if dec != nil {
		t.Error("a broken formatter must return a nil decoder, not one that silently decodes nothing")
	}
	if got := r.Stats(); got.Broken != 1 {
		t.Errorf("stats = %+v, want 1 broken", got)
	}
	// The count is not actionable on its own: an operator needs the model whose
	// script to go and fix.
	if models := r.Stats().BrokenModels; len(models) != 1 ||
		models[0].ModelID != "broken" || models[0].Reason == "" {
		t.Errorf("broken models = %+v, want one entry naming \"broken\" with a reason", models)
	}

	// The second call returns the cached failure.
	_, err2 := r.DecoderFor("broken", KindJS, broken)
	if err2 == nil || err2.Error() != err.Error() {
		t.Errorf("cached failure = %v, want the same error as the first (%v)", err2, err)
	}
}

func TestRegistryForget(t *testing.T) {
	r := NewRegistry(Options{})
	before, err := r.DecoderFor("probe", KindJS, probeFormatter)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	r.Forget("probe")
	if got := r.Stats(); got.Compiled != 0 {
		t.Errorf("stats after Forget = %+v, want nothing compiled", got)
	}
	after, err := r.DecoderFor("probe", KindJS, probeFormatter)
	if err != nil {
		t.Fatalf("recompile: %v", err)
	}
	if after == before {
		t.Error("Forget must drop the cached program")
	}
}

func TestRegistryNoneNeedsNoScript(t *testing.T) {
	r := NewRegistry(Options{})
	dec, err := r.DecoderFor("plain", KindNone, "")
	if err != nil {
		t.Fatalf("NONE model: %v", err)
	}
	out, err := dec.Decode(context.Background(), Input{Bytes: []byte{1, 2, 3}})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out != nil {
		t.Errorf("NONE must produce no reading, got %v", out)
	}
}

// TestRegistryWithoutAModelIDStillWorks covers the direct-call path used by the
// test-decode endpoint, where there is no model to key a cache entry on.
func TestRegistryWithoutAModelIDStillWorks(t *testing.T) {
	r := NewRegistry(Options{})
	dec, err := r.DecoderFor("", KindJS, probeFormatter)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	out, err := dec.Decode(context.Background(), Input{Bytes: []byte{0x64, 0x00}})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	wantNum(t, out, "temperature", 10.0)
	if got := r.Stats(); got.Compiled != 0 {
		t.Errorf("an uncached one-off must not occupy the cache: %+v", got)
	}
}

func TestRegistryIsSafeForConcurrentUse(t *testing.T) {
	r := NewRegistry(Options{})
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.DecoderFor("probe", KindJS, probeFormatter); err != nil {
				t.Errorf("concurrent compile: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := r.Stats(); got.Compiled != 1 {
		t.Errorf("stats = %+v, want one compiled entry", got)
	}
}

// TestBrokenModelsAreNamedAndSorted is the whole reason Stats reports more than
// a count: with several models failing, the operator has to be able to read the
// list and compare two refreshes, and map order changes between calls.
func TestBrokenModelsAreNamedAndSorted(t *testing.T) {
	r := NewRegistry(Options{})
	broken := "function decodeUplink(input) { this is not javascript"

	// Inserted out of order on purpose.
	for _, id := range []string{"sensor-x", "aqi-pro", "meter-3"} {
		if _, err := r.DecoderFor(id, KindJS, broken); err == nil {
			t.Fatalf("%s: a syntax error must be reported", id)
		}
	}
	if _, err := r.DecoderFor("healthy", KindJS,
		`function decodeUplink(input) { return { data: { ok: 1 } }; }`); err != nil {
		t.Fatalf("healthy model must compile: %v", err)
	}

	got := r.Stats()
	if got.Broken != 3 {
		t.Fatalf("broken = %d, want 3", got.Broken)
	}
	if got.Compiled != 1 {
		t.Errorf("compiled = %d, want 1: a healthy model must not be counted as broken", got.Compiled)
	}
	want := []string{"aqi-pro", "meter-3", "sensor-x"}
	for i, id := range want {
		if i >= len(got.BrokenModels) {
			t.Fatalf("broken_models holds %d entries, want %d", len(got.BrokenModels), len(want))
		}
		if got.BrokenModels[i].ModelID != id {
			t.Errorf("broken_models[%d] = %q, want %q (sorted by model id)",
				i, got.BrokenModels[i].ModelID, id)
		}
	}
	// A second snapshot must be identical, not merely equivalent.
	second := r.Stats()
	for i := range want {
		if second.BrokenModels[i].ModelID != got.BrokenModels[i].ModelID {
			t.Fatalf("the order changed between snapshots: %v then %v",
				got.BrokenModels, second.BrokenModels)
		}
	}
}

// TestNoBrokenModelsReportsNothing: an empty list must not be rendered as a
// phantom row, and a healthy fleet should carry no weight in the payload.
func TestNoBrokenModelsReportsNothing(t *testing.T) {
	r := NewRegistry(Options{})
	if _, err := r.DecoderFor("ok", KindJS,
		`function decodeUplink(input) { return { data: {} }; }`); err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := r.Stats()
	if len(st.BrokenModels) != 0 {
		t.Errorf("broken_models = %+v, want empty", st.BrokenModels)
	}
	// omitempty on an empty slice: the field must vanish rather than arrive as
	// null, which a client would have to guard against for no reason.
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "broken_models") {
		t.Errorf("marshalled stats = %s, want broken_models omitted", raw)
	}
}

// TestRegistryAppliesItsOptions proves the bounds are set once for the whole
// deployment rather than per model, which is what keeps a stray model from
// running with no deadline.
func TestRegistryAppliesItsOptions(t *testing.T) {
	// A deadline shorter than the script's own delay must interrupt it.
	r := NewRegistry(Options{Timeout: 20 * time.Millisecond})
	dec, err := r.DecoderFor("slow", KindJS,
		`function decodeUplink(input) { var t = Date.now(); while (Date.now() - t < 500) {} return { data: {} }; }`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	start := time.Now()
	_, err = dec.Decode(context.Background(), Input{Bytes: []byte{1}})
	if err == nil {
		t.Fatal("the registry's deadline must interrupt the script")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("the interrupt took %v; the deadline is not being enforced", elapsed)
	}
}
