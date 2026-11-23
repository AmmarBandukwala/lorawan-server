// Package pgstore persists the network server's protocol state in PostgreSQL.
//
// It implements the engine's DeviceRepo, SessionRepo and Recorder ports, so an
// Engine backed by pgstore survives restarts and can be shared by several
// replicas. Schema is owned by this package through embedded migrations, applied
// under a transaction-level advisory lock so replicas starting at the same time
// cannot race each other.
package pgstore

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migrationLockKey serialises schema migrations across processes. The value is
// arbitrary but stable, chosen to avoid clashing with other advisory locks in a
// Fieldnode deployment.
const migrationLockKey = int64(0x4649454c44) // "FIEL"

// Store is a PostgreSQL-backed engine store.
type Store struct {
	pool *pgxpool.Pool
}

// Open connects, verifies reachability and applies migrations.
func Open(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("pgstore: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pgstore: ping: %w", err)
	}
	s := &Store{pool: pool}
	if err := s.Migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

// New wraps an existing pool without migrating; use it when migrations are run
// by a separate job.
func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Pool exposes the underlying pool for health checks.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Close releases the pool.
func (s *Store) Close() { s.pool.Close() }

// Compile-time proof that this type satisfies every port the engine can be wired
// with. These exist because a field of engine.Store is an interface, and an
// interface left unset is a perfectly valid zero value: nothing fails to compile,
// nothing fails to start, and the API simply answers 503 at runtime.
//
// That is how device models went missing from every PostgreSQL deployment since
// this file was written. internal/pgstore/models.go implemented all four
// ModelRepo methods, migration 011 created the table, and the one line that
// connected them to the bundle was never added. The in-memory store does set it,
// so every test that ran against memserver passed.
var (
	_ engine.DeviceRepo  = (*Store)(nil)
	_ engine.SessionRepo = (*Store)(nil)
	_ engine.Recorder    = (*Store)(nil)
	_ engine.ModelRepo   = (*Store)(nil)
	_ engine.AirtimeRepo = (*Store)(nil)
	_ engine.SeenRepo    = (*Store)(nil)
)

// Store wires this implementation into the engine's port bundle.
//
// Every field must be set explicitly. Adding a port to engine.Store should be
// accompanied by a line here and by the assertion above; wiring_test.go fails if
// a port this type implements arrives as nil.
func (s *Store) Store() engine.Store {
	return engine.Store{
		Devices:  s,
		Sessions: s,
		Recorder: s,
		// Payload formatters live here. Without this line the models endpoints
		// answer 503 and no device can be given a formatter, which makes the
		// deployment read raw bytes forever while the console offers a Models
		// page it cannot fill.
		Models: s,
		// The two optional books are what make duty-cycle accounting and frame
		// de-duplication survive a restart or span processes.
		Airtime: s,
		Seen:    s,
	}
}

// Migrate applies every embedded migration that has not run yet, in one
// transaction guarded by an advisory lock.
func (s *Store) Migrate(ctx context.Context) error {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("pgstore: read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pgstore: begin migration: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", migrationLockKey); err != nil {
		return fmt.Errorf("pgstore: migration lock: %w", err)
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS ns_schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("pgstore: migration bookkeeping: %w", err)
	}

	for _, name := range names {
		var applied bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM ns_schema_migrations WHERE version = $1)`, name,
		).Scan(&applied); err != nil {
			return fmt.Errorf("pgstore: check migration %s: %w", name, err)
		}
		if applied {
			continue
		}
		stmt, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("pgstore: read migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, string(stmt)); err != nil {
			return fmt.Errorf("pgstore: apply migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO ns_schema_migrations (version) VALUES ($1)`, name); err != nil {
			return fmt.Errorf("pgstore: record migration %s: %w", name, err)
		}
	}
	return tx.Commit(ctx)
}

// --- encoding helpers -------------------------------------------------------

// Identifiers are stored as upper-case hex TEXT, matching db/init.sql.

func euiText(e lorawan.EUI64) string { return e.String() }

func keyText(k lorawan.AES128Key) string { return k.String() }

func devAddrText(a lorawan.DevAddr) string { return a.String() }

func parseEUI(s string) (lorawan.EUI64, error) {
	if s == "" {
		return lorawan.EUI64{}, nil
	}
	return lorawan.ParseEUI64(s)
}

func parseKey(s string) (lorawan.AES128Key, error) {
	if s == "" {
		return lorawan.AES128Key{}, nil
	}
	return lorawan.ParseAES128Key(s)
}

func parseDevAddr(s string) (lorawan.DevAddr, error) {
	if s == "" {
		return lorawan.DevAddr(0), nil
	}
	return lorawan.ParseDevAddr(s)
}

// devNoncesJSON converts the replay map for JSONB storage.
func devNoncesJSON(m map[lorawan.EUI64][]uint16) ([]byte, error) {
	if len(m) == 0 {
		return []byte("{}"), nil
	}
	out := make(map[string][]uint16, len(m))
	for joinEUI, nonces := range m {
		out[joinEUI.String()] = nonces
	}
	return json.Marshal(out)
}

func devNoncesFromJSON(b []byte) (map[lorawan.EUI64][]uint16, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var raw map[string][]uint16
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("pgstore: used_dev_nonces: %w", err)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[lorawan.EUI64][]uint16, len(raw))
	for joinEUI, nonces := range raw {
		parsed, err := parseEUI(joinEUI)
		if err != nil {
			return nil, fmt.Errorf("pgstore: used_dev_nonces key: %w", err)
		}
		out[parsed] = nonces
	}
	return out, nil
}
