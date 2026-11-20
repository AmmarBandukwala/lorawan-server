package codec

import (
	"sort"
	"sync"
)

// Registry caches compiled formatters, keyed by model.
//
// Compiling a JavaScript program is far more expensive than running it, and a
// fleet has one formatter shared by every device of that model, so the cache is
// what makes per-uplink decoding affordable. Entries are keyed by model ID and
// recompiled only when the script text actually changes, which is the event an
// operator causes and the only one that should cost anything.
//
// A script that fails to compile is cached as a failure rather than retried on
// every frame. A broken formatter is a configuration mistake that will not fix
// itself between uplinks, and recompiling it ten thousand times an hour spends
// CPU to rediscover the same typo while adding nothing to the log.
type Registry struct {
	mu      sync.Mutex
	entries map[string]registryEntry

	// opts are applied to every formatter built here, so a deployment sets its
	// execution bounds once instead of per model.
	opts Options
}

type registryEntry struct {
	kind   Kind
	script string
	dec    Decoder
	err    error
}

// NewRegistry returns an empty registry. A zero Options means the package
// defaults.
func NewRegistry(opts Options) *Registry {
	return &Registry{entries: map[string]registryEntry{}, opts: opts.withDefaults()}
}

// DecoderFor returns the formatter configured for a model, compiling it if this
// is the first time it has been seen or if its script changed since.
//
// The returned error is a problem with the formatter itself — a syntax error, an
// oversized script, a JS model with no script. Callers record it against the
// frame rather than treating the uplink as failed.
func (r *Registry) DecoderFor(modelID string, kind Kind, script string) (Decoder, error) {
	if modelID == "" {
		// Without a model there is nothing to key on, and compiling per device
		// would defeat the cache. A NONE decoder is the honest answer.
		return New(kind, script)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if e, ok := r.entries[modelID]; ok && e.kind == kind && e.script == script {
		return e.dec, e.err
	}

	dec, err := r.build(kind, script)
	// A NONE decoder is stateless and cheap; caching it per model would just
	// occupy a slot, but it also keeps the caller's code path identical.
	r.entries[modelID] = registryEntry{kind: kind, script: script, dec: dec, err: err}
	return dec, err
}

// build compiles outside the cached path so a failure is still recorded.
func (r *Registry) build(kind Kind, script string) (Decoder, error) {
	if kind == KindJS {
		dec, err := NewJSWithOptions(script, r.opts)
		if err != nil {
			// Returning None{} alongside the error would let a caller that
			// ignores the error silently decode nothing, so the decoder is nil
			// and the caller must handle the failure explicitly.
			return nil, err
		}
		return dec, nil
	}
	dec, err := New(kind, script)
	if err != nil {
		return nil, err
	}
	return dec, nil
}

// Forget drops a model's cached formatter. Called when a model is deleted or
// edited through the API so a change takes effect without a restart; the cache
// also notices a changed script on its own, so this is only needed for removal.
func (r *Registry) Forget(modelID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, modelID)
}

// RegistryStats is the status-page view: how many formatters are compiled and
// how many are broken, and which ones.
//
// The counts alone are not actionable. "2 broken formatters" tells an operator
// that some devices are reporting raw bytes; only BrokenModel names the model
// whose script they have to go and fix, and the reason is the compiler's own
// message, which is the difference between a two-minute fix and reading every
// script in the fleet.
type RegistryStats struct {
	Compiled     int           `json:"compiled"`
	Broken       int           `json:"broken"`
	BrokenModels []BrokenModel `json:"broken_models,omitempty"`
}

// BrokenModel is one model whose payload formatter will not compile.
type BrokenModel struct {
	ModelID string `json:"model_id"`
	Reason  string `json:"reason"`
}

// Stats snapshots the cache.
//
// Broken models come back sorted by ID: the map's iteration order changes
// between calls, and a status page whose rows reshuffle on every refresh cannot
// be read, and cannot be compared between two screenshots taken while
// troubleshooting.
func (r *Registry) Stats() RegistryStats {
	r.mu.Lock()
	var s RegistryStats
	broken := make([]BrokenModel, 0)
	for id, e := range r.entries {
		if e.err != nil {
			broken = append(broken, BrokenModel{ModelID: id, Reason: e.err.Error()})
			continue
		}
		s.Compiled++
	}
	r.mu.Unlock()

	sort.Slice(broken, func(i, j int) bool { return broken[i].ModelID < broken[j].ModelID })
	s.Broken = len(broken)
	s.BrokenModels = broken
	return s
}
