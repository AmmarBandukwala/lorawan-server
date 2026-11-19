package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
)

// Compile-time proof that this store satisfies both optional engine ports. A
// signature drift here fails the build instead of silently disabling
// persistence at runtime, which is the kind of bug that only shows up after the
// first restart.
var (
	_ engine.AirtimeRepo = (*Store)(nil)
	_ engine.SeenRepo    = (*Store)(nil)
)

// SaveAirtime writes the ledger as it currently stands.
//
// Every booking inside the window is written on every flush, so this must be
// idempotent — ON CONFLICT DO NOTHING makes a repeat a no-op rather than an
// error or a double count. Writing the whole ledger rather than a delta is
// deliberate: a delta would have to survive a crash between the write and the
// in-memory record of what was written, and the table holds hundreds of rows,
// not millions.
//
// Expired rows are deleted in the same transaction, so the table cannot grow
// without bound and a restart cannot inherit a window that has already expired.
func (s *Store) SaveAirtime(ctx context.Context, records []engine.AirtimeRecord,
	window time.Duration) error {

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pgstore: begin airtime flush: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if len(records) > 0 {
		gateways := make([]string, len(records))
		regions := make([]string, len(records))
		minHz := make([]int64, len(records))
		starts := make([]time.Time, len(records))
		millis := make([]int64, len(records))
		for i, r := range records {
			gateways[i] = r.Gateway.String()
			regions[i] = r.Region
			minHz[i] = int64(r.MinHz)
			starts[i] = r.Start.UTC()
			millis[i] = ceilMillis(r.Airtime)
		}

		_, err = tx.Exec(ctx, `
			INSERT INTO ns_airtime (gateway_id, region, min_hz, started_at, airtime_ms)
			SELECT g, r, m, t, ms
			FROM unnest($1::text[], $2::text[], $3::bigint[], $4::timestamptz[], $5::bigint[])
			      AS u(g, r, m, t, ms)
			ON CONFLICT DO NOTHING`,
			gateways, regions, minHz, starts, millis)
		if err != nil {
			return fmt.Errorf("pgstore: write airtime ledger: %w", err)
		}
	}

	if _, err := tx.Exec(ctx,
		`DELETE FROM ns_airtime WHERE started_at < now() - $1::interval`, intervalOf(window)); err != nil {
		return fmt.Errorf("pgstore: prune airtime ledger: %w", err)
	}
	return tx.Commit(ctx)
}

// LoadAirtime returns the bookings still inside the window, ordered so the
// caller can rebuild its ledger in the order the emissions happened.
func (s *Store) LoadAirtime(ctx context.Context, window time.Duration) ([]engine.AirtimeRecord, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT gateway_id, region, min_hz, started_at, airtime_ms
		FROM ns_airtime
		WHERE started_at > now() - $1::interval
		ORDER BY gateway_id, min_hz, started_at`,
		intervalOf(window))
	if err != nil {
		return nil, fmt.Errorf("pgstore: read airtime ledger: %w", err)
	}
	defer rows.Close()

	var out []engine.AirtimeRecord
	for rows.Next() {
		var (
			gatewayID string
			rec       engine.AirtimeRecord
			millis    int64
		)
		if err := rows.Scan(&gatewayID, &rec.Region, &rec.MinHz, &rec.Start, &millis); err != nil {
			return nil, fmt.Errorf("pgstore: scan airtime row: %w", err)
		}
		eui, err := lorawan.ParseEUI64(gatewayID)
		if err != nil {
			// A row we cannot parse is a corrupted ledger, not a reason to
			// refuse the whole restart. Skipping it costs one gateway's
			// bookings, which errs toward transmitting rather than toward a
			// server that will not start.
			continue
		}
		rec.Gateway = eui
		rec.Airtime = time.Duration(millis) * time.Millisecond
		out = append(out, rec)
	}
	return out, rows.Err()
}

// ClaimFrame reports whether this frame has not been handled by any instance
// inside the window, and claims it if so.
//
// The whole operation is one statement, and that is the point. A SELECT followed
// by an INSERT would let two instances both see "unseen" and both answer, which
// is the exact failure this table exists to prevent — and the race is not rare,
// it is the normal case whenever a frame reaches two gateways.
//
// A claim succeeds when the key is absent or has aged out. ON CONFLICT ... WHERE
// encodes both: the UPDATE only fires on an expired row, so a live claim leaves
// the row untouched and RETURNING yields nothing.
func (s *Store) ClaimFrame(ctx context.Context, key string, window time.Duration) (bool, error) {
	var claimed string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO ns_seen (dedup_key, seen_at)
		VALUES ($1, now())
		ON CONFLICT (dedup_key) DO UPDATE
		    SET seen_at = now()
		    WHERE ns_seen.seen_at < now() - $2::interval
		RETURNING dedup_key`,
		key, intervalOf(window)).Scan(&claimed)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // claimed by someone else inside the window
	}
	if err != nil {
		return false, fmt.Errorf("pgstore: claim frame: %w", err)
	}
	return true, nil
}

// PruneSeen drops claims that have aged out, keeping the table proportional to
// the traffic of the last window rather than to the lifetime of the deployment.
func (s *Store) PruneSeen(ctx context.Context, window time.Duration) (int, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM ns_seen WHERE seen_at < now() - $1::interval`, intervalOf(window))
	if err != nil {
		return 0, fmt.Errorf("pgstore: prune de-duplication book: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// ceilMillis converts an airtime to whole milliseconds, rounding up and never
// returning zero. The column carries a CHECK that it is positive, and rounding
// up keeps the ledger's total at or above the airtime actually spent — the only
// direction that is safe to be wrong in when the limit is a regulatory one.
func ceilMillis(d time.Duration) int64 {
	ms := (d + time.Millisecond - 1) / time.Millisecond
	if ms < 1 {
		return 1
	}
	return int64(ms)
}

// intervalOf renders a Go duration as a Postgres interval, faithfully, including
// a negative one.
//
// It deliberately does not substitute a default for a zero or negative duration.
// An earlier version did, and that turned every "has this aged out?" comparison
// into a lie: a caller asking whether a claim is older than minus one second —
// which is how the expiry path is exercised, and how an operator disables a
// window — got the default window instead and was told the row was still fresh.
// A window that cannot expire is the worse failure, because it looks like
// working de-duplication.
//
// The engine validates its windows to positive values in New, so a non-positive
// duration reaching here is either a test or a caller that means it; both are
// served better by doing exactly what was asked. Postgres evaluates
// `now() - interval '-1 second'` as now() plus one second, which is precisely the
// "everything is aged" reading the caller intended.
func intervalOf(d time.Duration) string {
	return fmt.Sprintf("%d microseconds", d.Microseconds())
}
