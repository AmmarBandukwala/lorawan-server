package pgstore

import (
	"testing"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
)

// TestStoreWiresEveryPort guards the wiring, not the SQL.
//
// It needs no database: Store() only assembles interface values and never touches
// the pool, so New(nil) is enough to assert which ports the engine is handed.
//
// This test exists because one port was missing for the whole life of this file.
// engine.Store's fields are interfaces, so leaving one out compiles, starts and
// serves traffic; the models endpoints answer 503 and every other feature works.
// The in-memory store sets Models, so the memstore-backed tests that exercise the
// models API through a real handler passed the entire time. A field nobody reads
// in CI is a field nobody notices missing.
func TestStoreWiresEveryPort(t *testing.T) {
	store := New(nil).Store()

	ports := map[string]any{
		"Devices":  store.Devices,
		"Sessions": store.Sessions,
		"Recorder": store.Recorder,
		"Models":   store.Models,
		"Airtime":  store.Airtime,
		"Seen":     store.Seen,
	}
	for name, port := range ports {
		if port == nil {
			t.Errorf("Store().%s is nil, so the API will answer 503 for that feature "+
				"on every PostgreSQL deployment even though *Store implements the port", name)
		}
	}

	// The optional ports are the ones a caller type-asserts or nil-checks, so a
	// nil there degrades quietly rather than failing loudly.
	if _, ok := store.Models.(engine.ModelRepo); !ok {
		t.Error("Store().Models does not satisfy engine.ModelRepo")
	}
}

// TestMemStoreWiresModelsToo keeps the two implementations honest about the same
// claim. The in-memory store has always set Models; if that ever regressed, the
// models API would 503 in development while working in production, which is the
// same bug in the opposite direction and equally invisible.
func TestMemStoreWiresModelsToo(t *testing.T) {
	if engine.NewMemStore().Store().Models == nil {
		t.Error("the in-memory store does not wire Models; the models API would " +
			"report 503 in development while working against PostgreSQL")
	}
}
