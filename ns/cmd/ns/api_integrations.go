package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/fieldnode/lorawan-server/ns/internal/integration"
	"github.com/fieldnode/lorawan-server/ns/internal/pgstore"
)

// Integration API: where decoded events get pushed.
//
// Two rules shape this file.
//
// A credential is never returned. Reads carry has_secret, and a write either
// replaces the stored value or explicitly leaves it alone. A panel that cannot
// see a token cannot leak it in a render, a screenshot or a browser extension,
// and the operator does not need to see it to use the integration.
//
// Every successful write reloads the live sinks before the response is sent.
// Returning 200 and reloading asynchronously would let the panel show a
// destination as saved while events were still going nowhere, with the operator
// left to discover that from a log line.

// IntegrationStore is the persistence this API needs. It is the pgstore method
// set, named here so the handlers can be tested without a database.
type IntegrationStore interface {
	ListIntegrations(ctx context.Context) ([]*pgstore.Integration, error)
	IntegrationByID(ctx context.Context, id string) (*pgstore.Integration, error)
	SaveIntegration(ctx context.Context, d *integration.Destination, replaceSecret bool) error
	DeleteIntegration(ctx context.Context, id string) (bool, error)
}

// Integrations is the live side: reload after a write, and report what the
// running process currently believes.
type Integrations interface {
	Reload(ctx context.Context) error
	Reports() []integration.Report
}

// integrationView is one destination as the API returns it. There is no secret
// field, so leaking one is impossible by omission rather than by care.
type integrationView struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`

	Webhook integration.WebhookDestination `json:"webhook"`
	MQTT    integration.MQTTDestination    `json:"mqtt"`

	HasSecret bool `json:"has_secret"`

	// Connected and DeliveryError come from the running supervisor, so the panel
	// can distinguish "saved" from "actually delivering".
	Connected     bool   `json:"connected"`
	DeliveryError string `json:"delivery_error,omitempty"`
	// ManagedByEnvironment marks a destination that comes from NS_WEBHOOK_URLS.
	// Shown so the panel tells the truth about everything that is live, and
	// read-only because there is no row behind it.
	ManagedByEnvironment bool `json:"managed_by_environment"`
}

func newIntegrationView(in *pgstore.Integration, report *integration.Report) integrationView {
	v := integrationView{
		ID:        in.ID,
		Kind:      in.Kind,
		Name:      in.Name,
		Enabled:   in.Enabled,
		Webhook:   in.Webhook,
		MQTT:      in.MQTT,
		HasSecret: in.HasSecret,
	}
	if report != nil {
		v.Connected = report.Connected
		v.DeliveryError = report.Error
	}
	return v
}

// reportFor finds the supervisor's report for one id, or nil.
func reportFor(reports []integration.Report, id string) *integration.Report {
	for i := range reports {
		if reports[i].ID == id {
			return &reports[i]
		}
	}
	return nil
}

func (s *apiServer) noIntegrationsStore(w http.ResponseWriter) {
	writeError(w, http.StatusServiceUnavailable,
		"integrations need the PostgreSQL store; this process is running with in-memory state")
}

// writeIntegrationError maps a store error to a status. A missing row is a 404
// naming the id, because "which of these four did I just typo" is the question
// an operator is actually asking.
func (s *apiServer) writeIntegrationError(w http.ResponseWriter, id string, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "no integration with id "+id)
		return
	}
	s.log.Error("integration request failed", "id", id, "err", err)
	writeError(w, http.StatusInternalServerError, "integration_request_failed")
}

// integrationReports returns the supervisor's view, or nothing when there is no
// supervisor. That is not an error: the stored configuration is still worth
// showing, and the panel renders those rows as not loaded.
func (s *apiServer) integrationReports() []integration.Report {
	if s.reloadIntegrations == nil {
		return nil
	}
	return s.reloadIntegrations.Reports()
}

// environmentIntegrations lists destinations configured through NS_WEBHOOK_URLS.
// They have no row, so they are appended rather than merged: hiding them would
// leave the panel unable to explain traffic the operator can see arriving.
func (s *apiServer) environmentIntegrations() []integrationView {
	var out []integrationView
	for _, name := range s.environmentSinkNames {
		out = append(out, integrationView{
			ID:                   name,
			Kind:                 integration.KindWebhook,
			Name:                 name,
			Enabled:              true,
			ManagedByEnvironment: true,
		})
	}
	return out
}

// handleListIntegrations returns every destination, without credentials.
func (s *apiServer) handleListIntegrations(w http.ResponseWriter, r *http.Request) {
	if s.integrations == nil {
		s.noIntegrationsStore(w)
		return
	}
	stored, err := s.integrations.ListIntegrations(r.Context())
	if err != nil {
		s.log.Error("cannot list integrations", "err", err)
		writeError(w, http.StatusInternalServerError, "integration_list_failed")
		return
	}
	reports := s.integrationReports()
	views := make([]integrationView, 0, len(stored))
	for _, in := range stored {
		views = append(views, newIntegrationView(in, reportFor(reports, in.ID)))
	}
	views = append(views, s.environmentIntegrations()...)
	writeJSON(w, http.StatusOK, map[string]any{"integrations": views})
}

func (s *apiServer) handleGetIntegration(w http.ResponseWriter, r *http.Request) {
	if s.integrations == nil {
		s.noIntegrationsStore(w)
		return
	}
	id := r.PathValue("id")
	in, err := s.integrations.IntegrationByID(r.Context(), id)
	if err != nil {
		s.writeIntegrationError(w, id, err)
		return
	}
	writeJSON(w, http.StatusOK, newIntegrationView(in, reportFor(s.integrationReports(), id)))
}

// integrationRequest is the write body. Secret is a pointer so the API can tell
// "leave the stored credential alone" (absent) from "clear it" (empty string),
// which a plain string cannot express.
type integrationRequest struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Enabled *bool  `json:"enabled"`

	Webhook integration.WebhookDestination `json:"webhook"`
	MQTT    integration.MQTTDestination    `json:"mqtt"`

	Secret *string `json:"secret"`
}

// maxIntegrationBody bounds one request body. The fields are short strings; the
// limit exists so a mistaken or hostile POST cannot make this process allocate.
const maxIntegrationBody = 16 << 10

// decodeIntegrationRequest reads and validates the body. The error text goes
// back to the operator verbatim, so it has to read as advice rather than as a
// parser complaint -- this is the message that appears when a URL is wrong.
func decodeIntegrationRequest(r *http.Request) (*integrationRequest, error) {
	var body integrationRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, maxIntegrationBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		return nil, fmt.Errorf("body must be a JSON object with id, kind, name and the kind's fields: %w", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("body holds more than one JSON value")
	}
	return &body, nil
}

// toDestination turns a request into the domain type, applying the defaults an
// omitted field means.
func (req *integrationRequest) toDestination(existing *pgstore.Integration) integration.Destination {
	d := integration.Destination{
		ID:      strings.TrimSpace(req.ID),
		Kind:    strings.ToLower(strings.TrimSpace(req.Kind)),
		Name:    strings.TrimSpace(req.Name),
		Enabled: true, // an omitted enabled means "on", which is what saving implies
		Webhook: req.Webhook,
		MQTT:    req.MQTT,
	}
	if req.Enabled != nil {
		d.Enabled = *req.Enabled
	}
	if existing != nil {
		if d.Name == "" {
			d.Name = existing.Name
		}
		if d.Kind == "" {
			d.Kind = existing.Kind
		}
	}
	// A nil Secret means "not stated", which on an update keeps what is stored.
	if req.Secret != nil {
		d.Secret = *req.Secret
	}
	return d
}

func (s *apiServer) handleCreateIntegration(w http.ResponseWriter, r *http.Request) {
	if s.integrations == nil {
		s.noIntegrationsStore(w)
		return
	}
	req, err := decodeIntegrationRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	d := req.toDestination(nil)
	if err := d.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.integrations.SaveIntegration(r.Context(), &d, true); err != nil {
		s.writeIntegrationError(w, d.ID, err)
		return
	}
	s.reloadAfterWrite(w, r, d.ID, http.StatusCreated)
}

func (s *apiServer) handleUpdateIntegration(w http.ResponseWriter, r *http.Request) {
	if s.integrations == nil {
		s.noIntegrationsStore(w)
		return
	}
	id := r.PathValue("id")
	existing, err := s.integrations.IntegrationByID(r.Context(), id)
	if err != nil {
		s.writeIntegrationError(w, id, err)
		return
	}
	req, err := decodeIntegrationRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID != "" && strings.TrimSpace(req.ID) != id {
		writeError(w, http.StatusBadRequest,
			"the id in the body must match the id in the path; use POST to add a destination")
		return
	}
	d := req.toDestination(existing)
	// A PUT to a path that already names the resource need not repeat the id.
	// toDestination inherits the name and kind from the stored row but not the
	// id, and without this the request the handler just declared legal would
	// fail validation on an empty identifier.
	if d.ID == "" {
		d.ID = id
	}
	// Only replace the credential when the request actually carried one, so an
	// edit of the name or the topic cannot silently disarm the destination.
	replace := req.Secret != nil
	if err := d.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.integrations.SaveIntegration(r.Context(), &d, replace); err != nil {
		s.writeIntegrationError(w, id, err)
		return
	}
	s.reloadAfterWrite(w, r, id, http.StatusOK)
}

func (s *apiServer) handleDeleteIntegration(w http.ResponseWriter, r *http.Request) {
	if s.integrations == nil {
		s.noIntegrationsStore(w)
		return
	}
	id := r.PathValue("id")
	removed, err := s.integrations.DeleteIntegration(r.Context(), id)
	if err != nil {
		s.writeIntegrationError(w, id, err)
		return
	}
	if !removed {
		writeError(w, http.StatusNotFound, "no integration with id "+id)
		return
	}
	s.reloadAfterWrite(w, r, id, http.StatusNoContent)
}

// reloadAfterWrite applies a change to the live sinks, then answers.
//
// A reload failure is reported as 500 even though the row was written. The
// operator's next action depends on which half happened, and a 2xx that left
// events undelivered would send them looking in the wrong place.
func (s *apiServer) reloadAfterWrite(w http.ResponseWriter, r *http.Request, id string, success int) {
	if s.reloadIntegrations != nil {
		if err := s.reloadIntegrations.Reload(r.Context()); err != nil {
			s.log.Error("integration saved but live sinks not reloaded", "id", id, "err", err)
			writeError(w, http.StatusInternalServerError,
				"saved, but the running integrations could not be reloaded: "+err.Error())
			return
		}
	}
	if success == http.StatusNoContent {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	in, err := s.integrations.IntegrationByID(r.Context(), id)
	if err != nil {
		// The write succeeded; a bare status beats turning a good save into a
		// confusing 500.
		w.WriteHeader(success)
		return
	}
	writeJSON(w, success, newIntegrationView(in, reportFor(s.integrationReports(), id)))
}

// tester is what a built sink must support for the test button. Both current
// sinks do; asserting it here means a third sink without it degrades rather than
// panics.
type tester interface {
	Test(ctx context.Context) error
}

// integrationTestTimeout bounds one probe. It is generous because the point is
// to reach a destination across the internet, and a fast failure here would
// report a working but distant broker as broken.
const integrationTestTimeout = 15 * time.Second

// handleTestIntegration sends one probe to a stored destination.
//
// The probe goes through a sink built fresh from the stored row rather than the
// live one, so the answer reflects what is saved now. Testing the live sink would
// answer a question nobody asked.
//
// A failed test is still 200 with ok:false. The request succeeded; the
// destination did not. Reserving 4xx/5xx for the request keeps the panel from
// having to guess whether a 500 meant the console or the broker.
func (s *apiServer) handleTestIntegration(w http.ResponseWriter, r *http.Request) {
	if s.integrations == nil {
		s.noIntegrationsStore(w)
		return
	}
	id := r.PathValue("id")
	in, err := s.integrations.IntegrationByID(r.Context(), id)
	if err != nil {
		s.writeIntegrationError(w, id, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), integrationTestTimeout)
	defer cancel()

	sink, err := in.Destination.BuildSink(s.log)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "detail": err.Error()})
		return
	}
	// A probe sink must not outlive the request: an MQTT client left here would
	// keep reconnecting to a broker for the life of the process.
	if c, ok := sink.(interface{ Close() error }); ok {
		defer c.Close()
	}

	t, ok := sink.(tester)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     false,
			"detail": "this destination kind cannot be tested on demand",
		})
		return
	}
	if err := t.Test(ctx); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"detail": "a test event was accepted (kind " + integration.KindIntegrationTest + ")",
	})
}
