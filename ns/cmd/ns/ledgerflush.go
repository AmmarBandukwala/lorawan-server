package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
)

// ledgerSource is the part of the engine this persists.
type ledgerSource interface {
	PruneAirtime()
	AirtimeSnapshot() []engine.AirtimeRecord
	AirtimeWindow() time.Duration
	DedupWindow() time.Duration
}

// ledgerSink is the durable half. Only the Postgres store implements it.
type ledgerSink interface {
	SaveAirtime(ctx context.Context, records []engine.AirtimeRecord, window time.Duration) error
	PruneSeen(ctx context.Context, window time.Duration) (int, error)
}

// DefaultLedgerFlushInterval is how often the ledger is written.
//
// The interval is the exposure: a crash loses the bookings made since the last
// flush, so a shorter interval means a restart under-counts less occupancy. It
// is not on the radio's critical path the way a per-packet write would be — this
// runs on a timer, and a downlink never waits for it.
const DefaultLedgerFlushInterval = 15 * time.Second

// startLedgerPersister keeps ns_airtime and ns_seen in step with the engine's
// in-memory books.
//
// Why a timer and not a write per transmission: the booking has to be immediate
// where it matters, which is the decision to transmit, and that reads memory.
// Durability is about surviving a restart, and a restart does not need the last
// fifteen seconds of occupancy to be on disk to be honest about the hour.
//
// The returned stop function flushes once more before returning, so a clean
// shutdown — the common case — loses nothing at all.
func startLedgerPersister(ctx context.Context, log *slog.Logger, src ledgerSource,
	sink ledgerSink, interval time.Duration) func() {

	if sink == nil {
		// Info, not Debug: the default level is Info, and whether the compliance
		// books outlive the process is the first thing to know when a duty-cycle
		// refusal looks unexplained. It is informational rather than a warning
		// because a single-process deployment is correct without it.
		log.Info("no ledger persistence configured; duty-cycle accounting stays in memory",
			"note", "a restart forgets the current window's occupancy")
		return func() {}
	}
	if interval <= 0 {
		interval = DefaultLedgerFlushInterval
	}

	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				flushLedger(ctx, log, src, sink)
			}
		}
	}()

	return func() {
		close(done)
		<-stopped
		// A fresh context: ctx is already cancelled when shutdown calls this,
		// and a cancelled context would turn the final flush into a no-op —
		// discarding exactly the state the flush exists to preserve.
		finalCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		flushLedger(finalCtx, log, src, sink)
	}
}

// ledgerSinkOf extracts the durable books from the engine's store.
//
// The nil check comes before the type assertion deliberately. A nil interface
// field asserted into a non-interface type yields a non-nil interface holding a
// nil pointer, which would sail past `sink == nil` and panic on first use — the
// in-memory store would then fail at its first flush instead of logging that
// there is nothing to persist to.
func ledgerSinkOf(store engine.Store) ledgerSink {
	if store.Airtime == nil {
		return nil
	}
	sink, ok := store.Airtime.(ledgerSink)
	if !ok {
		return nil
	}
	return sink
}

func flushLedger(ctx context.Context, log *slog.Logger, src ledgerSource, sink ledgerSink) {
	// Prune first so the write holds only what still constrains a transmission.
	src.PruneAirtime()

	records := src.AirtimeSnapshot()
	if err := sink.SaveAirtime(ctx, records, src.AirtimeWindow()); err != nil {
		// Warn rather than error: a database outage is already visible as every
		// gateway ageing out and every session write failing, and enforcement
		// continues from the in-memory ledger either way.
		log.Warn("could not persist the duty-cycle ledger", "bookings", len(records), "err", err)
		return
	}

	pruned, err := sink.PruneSeen(ctx, src.DedupWindow())
	if err != nil {
		log.Warn("could not prune the de-duplication book", "err", err)
	}
	log.Debug("persisted the duty-cycle ledger",
		"bookings", len(records), "window", src.AirtimeWindow(), "seen_rows_pruned", pruned)
}
