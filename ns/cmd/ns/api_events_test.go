package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/pgstore"
)

// fakeEvents is the event endpoint's own stub: unlike fakeTraffic it has rows to
// hand back, because what is being asserted here is mostly what a row looks like
// on the wire.
type fakeEvents struct {
	gotDevEUI string
	gotKind   string
	gotLimit  int
	rows      []pgstore.EventRow
}

func (f *fakeEvents) RecentUplinks(context.Context, string, int) ([]pgstore.UplinkRow, error) {
	return nil, nil
}

func (f *fakeEvents) RecentEvents(_ context.Context, devEUI, kind string, limit int,
) ([]pgstore.EventRow, error) {
	f.gotDevEUI, f.gotKind, f.gotLimit = devEUI, kind, limit
	return f.rows, nil
}

func TestEventsRequiresTheToken(t *testing.T) {
	srv, _ := newTestAPI(t)
	srv.traffic = &fakeEvents{}
	rec := do(srv, http.MethodGet, "/api/v1/events", "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

// TestEventsEndpointNeedsADatabaseStore: the in-memory store has no event table.
// A 503 says so; an empty list would read as a network where nothing happened,
// which is the more dangerous of the two answers.
func TestEventsEndpointNeedsADatabaseStore(t *testing.T) {
	srv, _ := newTestAPI(t)
	rec := do(srv, http.MethodGet, "/api/v1/events", testToken, "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
}

func TestEventsPassesEveryFilterThroughToTheStore(t *testing.T) {
	srv, _ := newTestAPI(t)
	traffic := &fakeEvents{}
	srv.traffic = traffic

	rec := do(srv, http.MethodGet,
		"/api/v1/events?dev_eui=0011223344556677&kind=uplink_drop&limit=7", testToken, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if traffic.gotDevEUI != "0011223344556677" || traffic.gotKind != "uplink_drop" ||
		traffic.gotLimit != 7 {
		t.Errorf("query = %q / %q / %d", traffic.gotDevEUI, traffic.gotKind, traffic.gotLimit)
	}
}

// TestEventsDefaultsTheLimit: an absent limit must not become "everything". The
// store clamps too, but the endpoint states the default it intends so the two
// cannot disagree about what an unqualified call means.
func TestEventsDefaultsTheLimit(t *testing.T) {
	srv, _ := newTestAPI(t)
	traffic := &fakeEvents{}
	srv.traffic = traffic
	if rec := do(srv, http.MethodGet, "/api/v1/events", testToken, ""); rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if traffic.gotLimit != 50 {
		t.Errorf("limit = %d, want the default 50", traffic.gotLimit)
	}
	if traffic.gotDevEUI != "" || traffic.gotKind != "" {
		t.Errorf("absent filters must reach the store as empty: %q / %q",
			traffic.gotDevEUI, traffic.gotKind)
	}
}

func TestEventsRejectsABadFilter(t *testing.T) {
	srv, _ := newTestAPI(t)
	srv.traffic = &fakeEvents{}

	for name, path := range map[string]string{
		"a malformed DevEUI":  "/api/v1/events?dev_eui=xyz",
		"a short DevEUI":      "/api/v1/events?dev_eui=001122334455667",
		"limit of zero":       "/api/v1/events?limit=0",
		"a negative limit":    "/api/v1/events?limit=-1",
		"a non-numeric limit": "/api/v1/events?limit=many",
	} {
		rec := do(srv, http.MethodGet, path, testToken, "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400: %s", name, rec.Code, rec.Body.String())
		}
	}
}

// TestEventsRejectsAnUnknownKind: the store would answer an unrecognised kind with
// an empty list, and "no join-accepts happened" is a different claim from "you
// typed jion_accept". The rejection has to name the valid values, because the
// caller is usually a person or an agent guessing at a vocabulary.
func TestEventsRejectsAnUnknownKind(t *testing.T) {
	srv, _ := newTestAPI(t)
	srv.traffic = &fakeEvents{}
	rec := do(srv, http.MethodGet, "/api/v1/events?kind=jion_accept", testToken, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "jion_accept") {
		t.Errorf("the rejection should quote what was rejected: %s", body)
	}
	if !strings.Contains(body, "join_accept") || !strings.Contains(body, "uplink_drop") {
		t.Errorf("the rejection should list what is valid: %s", body)
	}
}

// TestEveryRecordedKindIsFilterable is the drift guard.
//
// knownEventKinds is written with the engine's constants, so a kind added to the
// vocabulary file does not fail a compile here — it just quietly becomes
// unfilterable, and the endpoint goes on rejecting a kind the engine really does
// record. Reading the vocabulary file as text is the only way to notice, and that
// is the same trick check-stale-refs.sh exists for: the compiler cannot see a list
// that forgot to grow.
func TestEveryRecordedKindIsFilterable(t *testing.T) {
	// Two levels up: this test's package is ns/cmd/ns, and the vocabulary file is
	// ns/internal/engine/events.go.
	src, err := os.ReadFile("../../internal/engine/events.go")
	if err != nil {
		t.Fatalf("read the event vocabulary: %v", err)
	}
	matches := regexp.MustCompile(`(?m)^\s*(Event[A-Za-z]+)\s*=\s*"([a-z_]+)"`).
		FindAllStringSubmatch(string(src), -1)
	if len(matches) == 0 {
		t.Fatal("no event kinds found in the vocabulary file; did its shape change?")
	}

	recorded := make(map[string]string, len(matches))
	for _, m := range matches {
		recorded[m[2]] = m[1]
	}
	if len(recorded) != len(knownEventKinds) {
		t.Errorf("the vocabulary records %d kinds, this endpoint offers %d: %v",
			len(recorded), len(knownEventKinds), knownEventKinds)
	}
	for kind, constName := range recorded {
		if !isKnownEventKind(kind) {
			t.Errorf("kind %q (%s) is recorded by the engine but not filterable here",
				kind, constName)
		}
	}
	for _, kind := range knownEventKinds {
		if _, ok := recorded[kind]; !ok {
			t.Errorf("kind %q is offered here but the vocabulary file does not record it", kind)
		}
	}
}

// TestEventsOmitsWhatDidNotHappen pins the absences a reader cannot infer: a join
// has no application port, and a frame the server could not attribute has no
// device behind it. Both must be absent from the JSON rather than rendered as
// zero, because zero is a different and wrong claim.
func TestEventsOmitsWhatDidNotHappen(t *testing.T) {
	srv, _ := newTestAPI(t)
	srv.traffic = &fakeEvents{rows: []pgstore.EventRow{
		{EventID: 1, OccurredAt: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC),
			Kind: "join_accept", DevEUI: "0011223344556677", DevAddr: "01020304",
			Detail: "session established", FPort: -1},
		{EventID: 2, OccurredAt: time.Date(2026, 11, 17, 10, 0, 1, 0, time.UTC),
			Kind: "uplink_drop", Reason: "unknown_dev_addr", Detail: "no session",
			FPort: -1},
		{EventID: 3, OccurredAt: time.Date(2026, 11, 17, 10, 0, 2, 0, time.UTC),
			Kind: "uplink", DevEUI: "0011223344556677", GatewayID: "aa55555555555555",
			FCntUp: 42, FPort: 10},
	}}

	rec := do(srv, http.MethodGet, "/api/v1/events", testToken, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Events []eventView `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if len(out.Events) != 3 {
		t.Fatalf("events = %d, want 3", len(out.Events))
	}

	join, orphan, uplink := out.Events[0], out.Events[1], out.Events[2]
	if join.FPort != nil {
		t.Errorf("a join-accept has no application port, got %d", *join.FPort)
	}
	if join.Reason != "" {
		t.Errorf("an accepted join has no refusal reason: %q", join.Reason)
	}
	if join.OccurredAt != "2026-11-17T10:00:00Z" {
		t.Errorf("occurred_at = %q, want UTC RFC3339", join.OccurredAt)
	}
	if orphan.DevEUI != "" || orphan.DevAddr != "" || orphan.GatewayID != "" {
		t.Errorf("an unattributed frame must stay unattributed: %+v", orphan)
	}
	if orphan.Reason != "unknown_dev_addr" {
		t.Errorf("the reason is the finding and must survive: %+v", orphan)
	}
	if uplink.FPort == nil || *uplink.FPort != 10 {
		t.Errorf("f_port = %v, want 10", uplink.FPort)
	}
	if uplink.FCntUp != 42 || uplink.GatewayID != "aa55555555555555" {
		t.Errorf("the counter and the reception must come back: %+v", uplink)
	}
}

// TestEventsReturnsAnEmptyArrayNotAnull: a client iterating the result should not
// have to special-case a network that has not recorded anything yet.
func TestEventsReturnsAnEmptyArrayNotAnull(t *testing.T) {
	srv, _ := newTestAPI(t)
	srv.traffic = &fakeEvents{}
	rec := do(srv, http.MethodGet, "/api/v1/events", testToken, "")
	if got := strings.TrimSpace(rec.Body.String()); got != `{"events":[]}` {
		t.Errorf("body = %s, want {\"events\":[]}", got)
	}
}
