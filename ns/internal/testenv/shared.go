package testenv

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
	"github.com/fieldnode/lorawan-server/ns/internal/region"
)

// SharedState is what two engine instances must share to behave like one
// network rather than two that happen to hear the same radio.
type SharedState struct {
	// Seen is the frame de-duplication book. Two instances fed by one gateway
	// each receive the same frame; only one may answer it.
	Seen engine.SeenRepo
	// Airtime is the duty-cycle ledger. Sharing it makes the two instances'
	// combined transmissions add up, which is what compliance requires.
	Airtime engine.AirtimeRepo
}

// NewShared builds an engine with its own device and session store but a
// caller-supplied shared book.
//
// It exists so a test can stand up two engines against one gateway and assert
// they answer a frame once, which is the property a single-instance test cannot
// observe at all. Each engine keeps its own sessions: sharing those would be a
// different, larger change.
func NewShared(t *testing.T, shared SharedState, mutate func(*engine.Config)) *Env {
	t.Helper()

	store := engine.NewMemStore()
	st := store.Store()
	st.Seen = shared.Seen
	st.Airtime = shared.Airtime
	return newOnStore(t, store, st, mutate)
}

// newOnStore is the shared construction path behind New and NewShared.
func newOnStore(t *testing.T, store *engine.MemStore, st engine.Store,
	mutate func(*engine.Config)) *Env {

	return buildEnv(t, store, st, mutate, nil)
}

// SharedSeen is an in-memory de-duplication book that several engines can point
// at, standing in for the Postgres table in a test of multi-instance behaviour.
//
// It implements the same window semantics as the real one: a claim succeeds when
// the key is new or has aged out.
type SharedSeen struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

// NewSharedSeen returns an empty book.
func NewSharedSeen() *SharedSeen {
	return &SharedSeen{seen: map[string]time.Time{}}
}

// ClaimFrame implements engine.SeenRepo.
func (s *SharedSeen) ClaimFrame(_ context.Context, key string, window time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	if at, ok := s.seen[key]; ok && now.Sub(at) < window {
		return false, nil
	}
	s.seen[key] = now
	return true, nil
}

// Claims is how many distinct frames the book holds, for a test that wants to
// assert the book was actually consulted rather than bypassed.
// Claims reports how many frames are held, for assertions about the book itself.
func (s *SharedSeen) Claims() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.seen)
}

// SharedLedger is an in-memory stand-in for ns_airtime, so the durability of the
// duty-cycle books can be tested without a database.
//
// It stores what it was given rather than mirroring the engine's own structure:
// SaveAirtime replaces the whole window, exactly as the Postgres flush does, so a
// test that writes the ledger and hands the same object to a second engine
// reproduces a restart rather than approximating one.
//
// Ageing is judged by the engine's clock, which WithClock wires up. Judging it by
// the wall clock instead compares a fake-clock booking against real time, and the
// test passes only while the fake timestamp happens to sit within a window of
// now — deterministic-looking, and wrong on a Monday.
type SharedLedger struct {
	mu      sync.Mutex
	now     func() time.Time
	records []engine.AirtimeRecord
	flushes int
}

// NewSharedLedger returns an empty ledger.
func NewSharedLedger() *SharedLedger { return &SharedLedger{now: time.Now} }

// WithClock points the ledger at an environment's clock so its idea of "inside
// the window" matches the engine's.
func (l *SharedLedger) WithClock(c *Clock) *SharedLedger {
	l.mu.Lock()
	defer l.mu.Unlock()
	if c != nil {
		l.now = c.Now
	}
	return l
}

// SaveAirtime replaces the window with what was handed over.
func (l *SharedLedger) SaveAirtime(_ context.Context, records []engine.AirtimeRecord,
	window time.Duration) error {

	l.mu.Lock()
	defer l.mu.Unlock()
	l.flushes++

	cutoff := l.now().Add(-window)
	l.records = l.records[:0]
	for _, r := range records {
		if r.Start.Before(cutoff) {
			continue
		}
		l.records = append(l.records, r)
	}
	return nil
}

// LoadAirtime returns the bookings still inside the window.
func (l *SharedLedger) LoadAirtime(_ context.Context, window time.Duration) ([]engine.AirtimeRecord, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	cutoff := l.now().Add(-window)
	var out []engine.AirtimeRecord
	for _, r := range l.records {
		if r.Start.Before(cutoff) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// Flushes counts writes, so a test can assert the engine actually persisted
// rather than merely being able to.
func (l *SharedLedger) Flushes() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.flushes
}

// Booked is the airtime recorded for one gateway and sub-band.
func (l *SharedLedger) Booked(gateway lorawan.EUI64, minHz uint32) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()

	var total time.Duration
	for _, r := range l.records {
		if r.Gateway == gateway && r.MinHz == minHz {
			total += r.Airtime
		}
	}
	return total
}

// --- parsers that fail the test instead of returning an error -----------------
//
// Every test needs these. A test that ignores a parse error is a test that has
// quietly stopped testing anything, so these stop the run.

func MustNetID(t *testing.T, s string) lorawan.NetID {
	t.Helper()
	n, err := lorawan.ParseNetID(s)
	if err != nil {
		t.Fatalf("ParseNetID(%q): %v", s, err)
	}
	return n
}

func MustEUI64(t *testing.T, s string) lorawan.EUI64 {
	t.Helper()
	e, err := lorawan.ParseEUI64(s)
	if err != nil {
		t.Fatalf("ParseEUI64(%q): %v", s, err)
	}
	return e
}

func MustKey(t *testing.T, s string) lorawan.AES128Key {
	t.Helper()
	k, err := lorawan.ParseAES128Key(s)
	if err != nil {
		t.Fatalf("ParseAES128Key(%q): %v", s, err)
	}
	return k
}

func MustDevAddr(t *testing.T, s string) lorawan.DevAddr {
	t.Helper()
	a, err := lorawan.ParseDevAddr(s)
	if err != nil {
		t.Fatalf("ParseDevAddr(%q): %v", s, err)
	}
	return a
}

// MustRegion loads a band plan, failing the test on an unknown name.
func MustRegion(t *testing.T, name string) region.Region {
	t.Helper()
	r, err := region.New(name)
	if err != nil {
		t.Fatalf("region.New(%q): %v", name, err)
	}
	return r
}

// MustEUI64Static is the form without a *testing.T, for package-level fixtures.
// It panics, which is right: a malformed constant is a bug in the test file, and
// it should surface before any assertion runs.
func MustEUI64Static(s string) lorawan.EUI64 {
	e, err := lorawan.ParseEUI64(s)
	if err != nil {
		panic("testenv: bad EUI64 constant " + s + ": " + err.Error())
	}
	return e
}
