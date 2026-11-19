package lbs

import (
	"sync"
	"time"
)

// pendingDownlink is one dnmsg awaiting its dntxed.
type pendingDownlink struct {
	device string
	window string
	sentAt time.Time
}

// txTracker correlates transmission confirmations with the downlinks they
// belong to.
//
// The protocol offers no negative acknowledgement, so a downlink with no
// confirmation is presumed lost once it is older than the pending window. That
// is an inference and it is named as one: the counter is called unconfirmed
// rather than failed, because the server cannot tell a dropped transmission
// from a station that simply did not report it.
type txTracker struct {
	mu      sync.Mutex
	pending map[int64]pendingDownlink

	// pendingWindow is how long to wait for a dntxed. A class-A confirmation
	// arrives within the RX delay plus the transmission itself, so a handful of
	// seconds is generous, and keeping it short bounds the map.
	pendingWindow time.Duration

	confirmed   uint64
	unconfirmed uint64
}

func newTXTracker(window time.Duration) *txTracker {
	if window <= 0 {
		window = 10 * time.Second
	}
	return &txTracker{
		pending:       make(map[int64]pendingDownlink),
		pendingWindow: window,
	}
}

// track records a downlink as awaiting confirmation, pruning what has expired.
// Pruning on the send path rather than on a timer means an idle station holds no
// goroutine and no stale entries.
func (t *txTracker) track(diid int64, device, window string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneLocked(now)
	t.pending[diid] = pendingDownlink{device: device, window: window, sentAt: now}
}

// confirm marks a downlink as transmitted and reports whether it was known.
func (t *txTracker) confirm(diid int64, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	entry, ok := t.pending[diid]
	if !ok {
		return false
	}
	delete(t.pending, diid)
	t.confirmed++
	// A confirmation that arrives after the entry was already counted as lost
	// corrects that count rather than double-counting the same downlink.
	if now.Sub(entry.sentAt) > t.pendingWindow && t.unconfirmed > 0 {
		t.unconfirmed--
	}
	return true
}

// pruneLocked drops expired entries and counts them as unconfirmed.
func (t *txTracker) pruneLocked(now time.Time) {
	for diid, entry := range t.pending {
		if now.Sub(entry.sentAt) > t.pendingWindow {
			delete(t.pending, diid)
			t.unconfirmed++
		}
	}
}

// snapshot returns the counters and the current depth, for the status endpoint.
func (t *txTracker) snapshot() (confirmed, unconfirmed uint64, pending int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.confirmed, t.unconfirmed, len(t.pending)
}
