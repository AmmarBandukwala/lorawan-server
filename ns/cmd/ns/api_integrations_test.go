package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/fieldnode/lorawan-server/ns/internal/integration"
	"github.com/fieldnode/lorawan-server/ns/internal/pgstore"
)

// fakeIntegrationStore is the persistence the CRUD handlers need, without a
// database. It records the replaceSecret flag, because "was the credential
// touched" is the behaviour most worth pinning: getting it wrong silently
// disarms a working destination.
type fakeIntegrationStore struct {
	byID map[string]*pgstore.Integration
	// secrets is kept apart from the rows because pgstore.Integration embeds
	// Destination, so its Secret field is the very one reads must not surface.
	// Storing it anywhere inside the row would make the fake leak on a read,
	// which is the behaviour the real store is careful about.
	secrets    map[string]string
	saves      []saveCall
	deleteDone bool
	listErr    error
}

type saveCall struct {
	id            string
	secret        string
	replaceSecret bool
}

func newFakeIntegrationStore() *fakeIntegrationStore {
	return &fakeIntegrationStore{
		byID:    map[string]*pgstore.Integration{},
		secrets: map[string]string{},
	}
}

func (f *fakeIntegrationStore) ListIntegrations(context.Context) ([]*pgstore.Integration, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]*pgstore.Integration, 0, len(f.byID))
	for _, in := range f.byID {
		out = append(out, in)
	}
	return out, nil
}

func (f *fakeIntegrationStore) IntegrationByID(_ context.Context, id string) (*pgstore.Integration, error) {
	in, ok := f.byID[id]
	if !ok {
		return nil, pgx.ErrNoRows
	}
	copied := *in
	return &copied, nil
}

func (f *fakeIntegrationStore) SaveIntegration(_ context.Context, d *integration.Destination,
	replaceSecret bool) error {

	f.saves = append(f.saves, saveCall{id: d.ID, secret: d.Secret, replaceSecret: replaceSecret})

	// The credential follows the same rule as the SQL: an update that did not
	// carry one keeps what is stored, and an explicit empty string clears it.
	if replaceSecret {
		f.secrets[d.ID] = d.Secret
	}
	// A create with no secret at all simply leaves the map entry absent, which
	// reads back as "no credential", as it should.

	row := pgstore.Integration{Destination: *d}
	row.HasSecret = f.secrets[d.ID] != ""
	// Reads never surface the credential, so the row keeps it empty exactly as
	// the real SELECT does.
	row.Secret = ""
	f.byID[d.ID] = &row
	return nil
}

func (f *fakeIntegrationStore) DeleteIntegration(_ context.Context, id string) (bool, error) {
	if _, ok := f.byID[id]; !ok {
		return false, nil
	}
	delete(f.byID, id)
	delete(f.secrets, id)
	f.deleteDone = true
	return true, nil
}

// storedSecret reaches past the API's redaction to assert what the row holds,
// which is the only way to test that an edit preserved a credential.
func (f *fakeIntegrationStore) storedSecret(id string) string {
	return f.secrets[id]
}

// fakeReloader stands in for the supervisor.
type fakeReloader struct {
	reloads int
	err     error
}

func (f *fakeReloader) Reload(context.Context) error { f.reloads++; return f.err }
func (f *fakeReloader) Reports() []integration.Report {
	return []integration.Report{{ID: "cloud-mq", Kind: integration.KindMQTT, Connected: true}}
}

func newIntegrationAPI(t *testing.T) (*apiServer, *fakeIntegrationStore, *fakeReloader) {
	t.Helper()
	srv, _ := newTestAPI(t)
	store := newFakeIntegrationStore()
	reloader := &fakeReloader{}
	srv.integrations = store
	srv.reloadIntegrations = reloader
	srv.environmentSinkNames = []string{"webhook https://env.example/hook"}
	return srv, store, reloader
}

const validMQTTBody = `{"id":"cloud-mq","kind":"mqtt","name":"Cloud broker",` +
	`"mqtt":{"broker_url":"tls://broker.example:8883","topic":"fieldnode/up","qos":1},` +
	`"secret":"broker-password"}`

func TestIntegrationEndpointsRequireAStore(t *testing.T) {
	srv, _ := newTestAPI(t) // no integration store: the in-memory case

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/integrations"},
		{http.MethodPost, "/api/v1/integrations"},
		{http.MethodGet, "/api/v1/integrations/cloud-mq"},
		{http.MethodPut, "/api/v1/integrations/cloud-mq"},
		{http.MethodDelete, "/api/v1/integrations/cloud-mq"},
		{http.MethodPost, "/api/v1/integrations/cloud-mq/test"},
	} {
		rec := do(srv, tc.method, tc.path, testToken, validMQTTBody)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s = %d, want 503: a write a restart would forget must be refused",
				tc.method, tc.path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "PostgreSQL") {
			t.Errorf("%s %s body = %s, want it to name what is missing",
				tc.method, tc.path, rec.Body.String())
		}
	}
}

func TestIntegrationEndpointsRequireAuth(t *testing.T) {
	srv, _, _ := newIntegrationAPI(t)
	rec := do(srv, http.MethodGet, "/api/v1/integrations", "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestCreateIntegration(t *testing.T) {
	srv, store, reloader := newIntegrationAPI(t)

	rec := do(srv, http.MethodPost, "/api/v1/integrations", testToken, validMQTTBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s, want 201", rec.Code, rec.Body.String())
	}
	if store.deleteDone {
		t.Error("a create must not delete anything")
	}
	if len(store.saves) != 1 || !store.saves[0].replaceSecret {
		t.Errorf("saves = %+v, want one create that writes the credential", store.saves)
	}
	if reloader.reloads != 1 {
		t.Errorf("reloads = %d, want 1: a saved destination must go live without a restart",
			reloader.reloads)
	}

	var view integrationView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode the response: %v (%s)", err, rec.Body.String())
	}
	if view.ID != "cloud-mq" || view.Kind != integration.KindMQTT || !view.Enabled {
		t.Errorf("view = %+v", view)
	}
	if !view.HasSecret {
		t.Error("has_secret must be true so the panel can show a credential is set")
	}
	if view.MQTT.Topic != "fieldnode/up" || view.MQTT.QoS != 1 {
		t.Errorf("mqtt settings = %+v", view.MQTT)
	}
	// The credential must not be readable through the API, in any form.
	if strings.Contains(rec.Body.String(), "broker-password") {
		t.Errorf("the response leaked the credential: %s", rec.Body.String())
	}
	if store.storedSecret("cloud-mq") != "broker-password" {
		t.Errorf("the credential was not stored: %q", store.storedSecret("cloud-mq"))
	}
}

func TestCreateIntegrationRejectsBadBodies(t *testing.T) {
	srv, store, reloader := newIntegrationAPI(t)

	for name, body := range map[string]string{
		"no kind":         `{"id":"x1","name":"n"}`,
		"unknown kind":    `{"id":"x1","kind":"kafka","name":"n"}`,
		"webhook no url":  `{"id":"x1","kind":"webhook","name":"n"}`,
		"webhook bad url": `{"id":"x1","kind":"webhook","name":"n","webhook":{"url":"not a url"}}`,
		"mqtt no broker":  `{"id":"x1","kind":"mqtt","name":"n"}`,
		"empty name":      `{"id":"x1","kind":"webhook","name":"","webhook":{"url":"https://a.test/h"}}`,
		"bad id":          `{"id":"_nope","kind":"webhook","name":"n","webhook":{"url":"https://a.test/h"}}`,
		"not json":        `nonsense`,
		"two values":      `{"id":"x1"} {"id":"x2"}`,
		"unknown field":   `{"id":"x1","kind":"webhook","name":"n","nope":1}`,
	} {
		rec := do(srv, http.MethodPost, "/api/v1/integrations", testToken, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, body = %s, want 400", name, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "integration_create_failed") {
			t.Errorf("%s: a validation problem must not be reported as a server fault", name)
		}
	}
	if len(store.byID) != 0 {
		t.Errorf("rejected requests stored %d rows, want 0", len(store.byID))
	}
	if reloader.reloads != 0 {
		t.Errorf("rejected requests reloaded %d times, want 0", reloader.reloads)
	}
}

func TestGetIntegrationAndMissingID(t *testing.T) {
	srv, _, _ := newIntegrationAPI(t)
	do(srv, http.MethodPost, "/api/v1/integrations", testToken, validMQTTBody)

	rec := do(srv, http.MethodGet, "/api/v1/integrations/cloud-mq", testToken, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	// The supervisor's report rides along, which is how the panel tells "saved"
	// from "actually connected".
	if !strings.Contains(rec.Body.String(), `"connected":true`) {
		t.Errorf("the connected flag is missing: %s", rec.Body.String())
	}

	missing := do(srv, http.MethodGet, "/api/v1/integrations/nothing-here", testToken, "")
	if missing.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", missing.Code)
	}
	if !strings.Contains(missing.Body.String(), "nothing-here") {
		t.Errorf("a 404 must name the id the operator asked for: %s", missing.Body.String())
	}
}

// TestUpdateKeepsAnUntouchedCredential is the regression that matters most in
// this surface: editing a topic or a name must not disarm the destination by
// blanking a password nobody mentioned.
func TestUpdateKeepsAnUntouchedCredential(t *testing.T) {
	srv, store, _ := newIntegrationAPI(t)
	do(srv, http.MethodPost, "/api/v1/integrations", testToken, validMQTTBody)

	// An edit that says nothing about the secret.
	rec := do(srv, http.MethodPut, "/api/v1/integrations/cloud-mq", testToken,
		`{"kind":"mqtt","name":"Renamed","mqtt":{"broker_url":"tls://broker.example:8883","topic":"other"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(store.saves) != 2 || store.saves[1].replaceSecret {
		t.Fatalf("saves = %+v, want the update to leave the credential alone", store.saves)
	}
	if got := store.storedSecret("cloud-mq"); got != "broker-password" {
		t.Errorf("stored secret = %q, want the original to survive the edit", got)
	}

	// An explicit empty string means "remove it", which only that can express.
	do(srv, http.MethodPut, "/api/v1/integrations/cloud-mq", testToken,
		`{"kind":"mqtt","name":"Renamed","secret":"","mqtt":{"broker_url":"tls://broker.example:8883"}}`)
	if got := store.storedSecret("cloud-mq"); got != "" {
		t.Errorf("stored secret = %q after an explicit clear, want empty", got)
	}
}

func TestUpdateRejectsAnIDThatDisagreesWithThePath(t *testing.T) {
	srv, _, _ := newIntegrationAPI(t)
	do(srv, http.MethodPost, "/api/v1/integrations", testToken, validMQTTBody)

	rec := do(srv, http.MethodPut, "/api/v1/integrations/cloud-mq", testToken,
		`{"id":"somewhere-else","kind":"mqtt","name":"n","mqtt":{"broker_url":"tls://a.test:8883"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "must match") {
		t.Errorf("body = %s, want it to explain the mismatch", rec.Body.String())
	}
}

func TestUpdateAndDeleteMissingIntegration(t *testing.T) {
	srv, _, _ := newIntegrationAPI(t)

	upd := do(srv, http.MethodPut, "/api/v1/integrations/ghost", testToken, validMQTTBody)
	if upd.Code != http.StatusNotFound {
		t.Errorf("update of a missing id = %d, want 404", upd.Code)
	}
	del := do(srv, http.MethodDelete, "/api/v1/integrations/ghost", testToken, "")
	if del.Code != http.StatusNotFound {
		t.Errorf("delete of a missing id = %d, want 404", del.Code)
	}
}

func TestDeleteIntegration(t *testing.T) {
	srv, store, reloader := newIntegrationAPI(t)
	do(srv, http.MethodPost, "/api/v1/integrations", testToken, validMQTTBody)

	rec := do(srv, http.MethodDelete, "/api/v1/integrations/cloud-mq", testToken, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s, want 204", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Errorf("a 204 must carry no body, got %s", rec.Body.String())
	}
	if len(store.byID) != 0 {
		t.Errorf("the row survived: %+v", store.byID)
	}
	// Two writes, two reloads: removal must stop delivery immediately.
	if reloader.reloads != 2 {
		t.Errorf("reloads = %d, want 2", reloader.reloads)
	}
}

// TestReloadFailureIsReportedEvenThoughTheRowWasWritten: the operator's next
// action depends on which half succeeded, so a 2xx here would send them looking
// at the broker instead of at the console.
func TestReloadFailureIsReportedEvenThoughTheRowWasWritten(t *testing.T) {
	srv, store, reloader := newIntegrationAPI(t)
	reloader.err = errors.New("broker dial timed out")

	rec := do(srv, http.MethodPost, "/api/v1/integrations", testToken, validMQTTBody)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "could not be reloaded") {
		t.Errorf("body = %s, want it to say the save worked but the reload did not", rec.Body.String())
	}
	if len(store.byID) != 1 {
		t.Error("the row must still be written; the reload is retried on the next write")
	}
}

func TestListIncludesEnvironmentDestinations(t *testing.T) {
	srv, _, _ := newIntegrationAPI(t)
	do(srv, http.MethodPost, "/api/v1/integrations", testToken, validMQTTBody)

	rec := do(srv, http.MethodGet, "/api/v1/integrations", testToken, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Integrations []integrationView `json:"integrations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if len(out.Integrations) != 2 {
		t.Fatalf("got %d integrations, want the stored one plus the environment one: %s",
			len(out.Integrations), rec.Body.String())
	}

	var stored, env *integrationView
	for i := range out.Integrations {
		if out.Integrations[i].ManagedByEnvironment {
			env = &out.Integrations[i]
		} else {
			stored = &out.Integrations[i]
		}
	}
	if stored == nil || stored.ID != "cloud-mq" {
		t.Errorf("the stored destination is missing: %+v", out.Integrations)
	}
	// Omitting the environment destination would leave the panel unable to
	// explain traffic the operator can see arriving.
	if env == nil || !env.Enabled {
		t.Errorf("the environment destination is missing or disabled: %+v", out.Integrations)
	}
}

func TestListReportsStoreFailure(t *testing.T) {
	srv, store, _ := newIntegrationAPI(t)
	store.listErr = errors.New("connection reset by peer")

	rec := do(srv, http.MethodGet, "/api/v1/integrations", testToken, "")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "connection reset") {
		t.Errorf("an internal error string must not be echoed to the client: %s", rec.Body.String())
	}
}

// TestTestEndpointNeverReturnsServerErrorsForADestinationProblem: a 500 would
// make the panel guess whether the console or the broker is broken.
func TestTestEndpointNeverReturnsServerErrorsForADestinationProblem(t *testing.T) {
	srv, _, _ := newIntegrationAPI(t)
	// A broker on a port nothing listens on, so the probe must fail.
	do(srv, http.MethodPost, "/api/v1/integrations", testToken,
		`{"id":"dead-broker","kind":"mqtt","name":"Unreachable",`+
			`"mqtt":{"broker_url":"tcp://127.0.0.1:1"},"secret":"x"}`)

	rec := do(srv, http.MethodPost, "/api/v1/integrations/dead-broker/test", testToken, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s, want 200 with ok:false", rec.Code, rec.Body.String())
	}
	var out struct {
		OK     bool   `json:"ok"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if out.OK {
		t.Error("an unreachable broker must not report success")
	}
	if out.Detail == "" {
		t.Error("a failed probe must say why, or the operator has nothing to act on")
	}
}

func TestTestEndpointForMissingIntegration(t *testing.T) {
	srv, _, _ := newIntegrationAPI(t)
	rec := do(srv, http.MethodPost, "/api/v1/integrations/ghost/test", testToken, "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// TestIntegrationRequestBodyIsBounded guards against a POST that makes the
// process allocate on behalf of whoever sent it.
func TestIntegrationRequestBodyIsBounded(t *testing.T) {
	srv, _, _ := newIntegrationAPI(t)
	huge := `{"id":"big","kind":"webhook","name":"n","webhook":{"url":"https://a.test/` +
		strings.Repeat("x", 32<<10) + `"}}`

	rec := do(srv, http.MethodPost, "/api/v1/integrations", testToken, huge)
	if rec.Code == http.StatusCreated {
		t.Fatal("a body far past the limit was accepted")
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if len(rec.Body.String()) > 4<<10 {
		t.Errorf("the rejection is %d bytes; an error response must stay small", len(rec.Body.String()))
	}
}
