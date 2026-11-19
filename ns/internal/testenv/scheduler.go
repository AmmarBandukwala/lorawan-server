package testenv

import (
	"sync"
	"testing"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
)

// DeferredScheduler holds scheduled work until a test runs it, which is how the
// aggregation window becomes observable.
//
// The default test scheduler runs callbacks inline, and that is right for
// everything except a feature whose whole behaviour is "wait, then act": inline
// execution closes the window before the second gateway's copy can arrive, so
// the test would be asserting that aggregation does nothing.
type DeferredScheduler struct {
	mu    sync.Mutex
	items []deferredItem
	// ran accumulates the delays of everything already executed, so a test can
	// assert on the timing of work it had to let run.
	ran []time.Duration
}

type deferredItem struct {
	delay time.Duration
	fn    func()
}

// Pending is how much work is waiting.
func (d *DeferredScheduler) Pending() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.items)
}

// Reset forgets both the queued work and the history of what already ran.
//
// A test that has just completed a join still has that join's RX1/RX2 delays in
// its execution history, and a timing assertion made afterwards would be reading
// the join's numbers. Clearing both halves at once keeps "measure from here"
// impossible to get half-done.
func (d *DeferredScheduler) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.items = nil
	d.ran = nil
}

// Delays reports the delay each waiting item was scheduled with, in the order it
// was scheduled. Assertions about *when* a downlink should go out — the remaining
// slack after aggregation — are made against this.
func (d *DeferredScheduler) Delays() []time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()

	out := make([]time.Duration, 0, len(d.items))
	for _, it := range d.items {
		out = append(out, it.delay)
	}
	return out
}

// Run executes everything waiting and reports how much there was. Callbacks are
// taken out of the queue before running, so one that schedules more work leaves
// it for the next Run rather than recursing.
func (d *DeferredScheduler) Run() int {
	d.mu.Lock()
	items := d.items
	d.items = nil
	for _, it := range items {
		d.ran = append(d.ran, it.delay)
	}
	d.mu.Unlock()

	for _, it := range items {
		it.fn()
	}
	return len(items)
}

// ExecutedDelays reports the delay each item was scheduled with, across every Run
// so far. Asserting on the *dispatch* delay of a downlink means reading this
// after the window has run, at which point Pending and Delays are empty.
func (d *DeferredScheduler) ExecutedDelays() []time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]time.Duration(nil), d.ran...)
}

// NewDeferred builds an environment whose scheduled work waits for the test.
func NewDeferred(t *testing.T, mutate func(*engine.Config)) (*Env, *DeferredScheduler) {
	t.Helper()

	store := engine.NewMemStore()
	sched := &DeferredScheduler{}
	env := buildEnv(t, store, store.Store(), mutate, func(delay time.Duration, fn func()) {
		sched.mu.Lock()
		defer sched.mu.Unlock()
		sched.items = append(sched.items, deferredItem{delay: delay, fn: fn})
	})
	env.sched = sched
	return env, sched
}

// buildEnv constructs the engine. scheduler nil means the inline one, which is
// what nearly every test wants.
