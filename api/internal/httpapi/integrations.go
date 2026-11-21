package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Integration destinations, relayed to the network server.
//
// This is a relay for the same reason device models are: the network server owns
// the MQTT connection, the webhook credential and the delivery counters, so it
// is the only component that can say whether a destination works, and it says so
// with its own reason. Re-implementing that judgement here would produce a
// second, weaker opinion.
//
// The credential deserves a note, because it is the reason this file is separate
// rather than a generic proxy: a destination's secret is accepted on write and
// never returned by the network server. Nothing here may log a request body, and
// nothing here returns one. Bodies are forwarded byte for byte and responses are
// passed through, so the relay cannot be the place a credential leaks from.

// integrationIDPattern mirrors the network server's destination id rule, so a
// mistyped id is refused here with a message naming the field rather than after a
// round trip — and it is what makes building the path by concatenation safe.
var integrationIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,63}$`)

// maxIntegrationBody mirrors the network server's own cap. Matching it means a
// body this service refuses is one the network server would have refused too,
// for the same stated reason.
const maxIntegrationBody = 16 << 10

// integrationTestTimeout bounds a connectivity probe. It is far longer than
// controlTimeout because the probe dials a broker or an HTTPS endpoint across the
// internet: reporting a distant but working destination as broken is worse than
// waiting for it, and the network server applies a comparable budget itself.
const integrationTestTimeout = 20 * time.Second

// pathIntegrationID reads and validates the destination id from the route.
func pathIntegrationID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := strings.TrimSpace(r.PathValue("id"))
	if !integrationIDPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, ErrIntegrationIDInvalid)
		return "", false
	}
	return id, true
}

// integrationBody reads a destination body verbatim, for the reason given in
// modelBody: the console's request is already the network server's request.
//
// Unlike a model body, this one may carry a credential. It is forwarded and then
// dropped; it is never parsed into a struct that outlives the request, never
// logged, and never echoed back in an error.
func integrationBody(w http.ResponseWriter, r *http.Request) (json.RawMessage, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxIntegrationBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, ErrIntegrationTooLarge)
			return nil, false
		}
		writeError(w, http.StatusBadRequest, ErrInvalidJSON)
		return nil, false
	}
	if len(body) == 0 || !json.Valid(body) {
		writeError(w, http.StatusBadRequest, ErrInvalidJSON)
		return nil, false
	}
	return json.RawMessage(body), true
}

func (s *Server) listIntegrations(w http.ResponseWriter, r *http.Request) {
	if !s.requireController(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), controlTimeout)
	defer cancel()

	resp, err := s.controller.ListIntegrations(ctx)
	if err != nil {
		writeUpstreamError(w, "list integrations", "", err, ErrIntegrationListFailed)
		return
	}
	writeRaw(w, http.StatusOK, resp)
}

func (s *Server) getIntegration(w http.ResponseWriter, r *http.Request) {
	id, ok := pathIntegrationID(w, r)
	if !ok || !s.requireController(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), controlTimeout)
	defer cancel()

	resp, err := s.controller.Integration(ctx, id)
	if err != nil {
		writeUpstreamError(w, "read integration", id, err, ErrIntegrationListFailed)
		return
	}
	writeRaw(w, http.StatusOK, resp)
}

func (s *Server) createIntegration(w http.ResponseWriter, r *http.Request) {
	if !s.requireController(w) {
		return
	}
	body, ok := integrationBody(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), controlTimeout)
	defer cancel()

	resp, err := s.controller.CreateIntegration(ctx, body)
	if err != nil {
		// The network server's own validation message is what tells an operator
		// that tls:// wants a reachable broker or that a topic has a bad
		// wildcard, so it is relayed rather than replaced with a bare code.
		writeUpstreamError(w, "create integration", "", err, ErrIntegrationSaveFailed)
		return
	}
	writeRaw(w, http.StatusCreated, resp)
}

func (s *Server) updateIntegration(w http.ResponseWriter, r *http.Request) {
	id, ok := pathIntegrationID(w, r)
	if !ok || !s.requireController(w) {
		return
	}
	body, ok := integrationBody(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), controlTimeout)
	defer cancel()

	resp, err := s.controller.UpdateIntegration(ctx, id, body)
	if err != nil {
		writeUpstreamError(w, "save integration", id, err, ErrIntegrationSaveFailed)
		return
	}
	writeRaw(w, http.StatusOK, resp)
}

func (s *Server) deleteIntegration(w http.ResponseWriter, r *http.Request) {
	id, ok := pathIntegrationID(w, r)
	if !ok || !s.requireController(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), controlTimeout)
	defer cancel()

	resp, err := s.controller.DeleteIntegration(ctx, id)
	if err != nil {
		writeUpstreamError(w, "delete integration", id, err, ErrIntegrationDeleteFailed)
		return
	}
	// The network server answers 204 with no body. Passing that through keeps a
	// console's "it is gone" handling honest rather than inventing a JSON shell.
	if len(resp) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeRaw(w, http.StatusOK, resp)
}

func (s *Server) testIntegration(w http.ResponseWriter, r *http.Request) {
	id, ok := pathIntegrationID(w, r)
	if !ok || !s.requireController(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), integrationTestTimeout)
	defer cancel()

	resp, err := s.controller.TestIntegration(ctx, id)
	if err != nil {
		writeUpstreamError(w, "test integration", id, err, ErrIntegrationTestFailed)
		return
	}
	// A failing probe arrives as a 200 with ok:false, and that is the answer, not
	// an error: the request worked, the destination did not. Relaying it keeps the
	// console able to say "the broker refused us" instead of "the console broke".
	writeRaw(w, http.StatusOK, resp)
}
