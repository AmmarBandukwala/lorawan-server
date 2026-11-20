package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Device models and decoded uplink history, relayed to the network server.
//
// Both are relays rather than first-class resources of this service, and that is
// the design rather than a shortcut:
//
//   - A model carries a JavaScript payload formatter. The network server is the
//     component that compiles and runs it, so it is the only thing that can say
//     whether a script is acceptable, and it says so with the parser's own message
//     naming the line and token. Re-implementing that here would either disagree
//     with the verdict or ship a second JavaScript parser.
//   - Uplink history is decoded at ingest by the network server and stored with
//     the reading. Re-deriving it here would mean re-running formatters in a
//     service that has no codec.
//
// So this file owns only what a relay must own anyway: the shape of the URL, the
// size of the body, and a clear failure when the network server is not
// configured. Everything else passes through byte for byte, including a failing
// test-decode, which is a verdict rather than an error.

// modelIDPattern mirrors the network server's slug rule. Checking it here turns a
// mistyped slug into a rejection that names the field before a round trip, and it
// is what makes modelPath safe to build by concatenation.
var modelIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,63}$`)

// MaxModelBodyBytes bounds a model request. A payload formatter may be 64 KiB at
// the network server, so the relay cannot use the 16 KiB cap the rest of the API
// applies — a vendor decoder between those two sizes would be refused here with a
// message about invalid JSON, which is a confusing way to say "too big".
const MaxModelBodyBytes = (64 << 10) + 4096

// maxUplinkLimit matches the network server's own ceiling, so a console asking
// for more gets a refusal here instead of a silently truncated answer.
const maxUplinkLimit = 500

// pathModelID reads and validates the slug from the route.
func pathModelID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := strings.TrimSpace(r.PathValue("model_id"))
	if !modelIDPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, ErrModelIDInvalid)
		return "", false
	}
	return id, true
}

// modelBody reads a model request body verbatim.
//
// The JSON is validated but not decoded into a struct: the console's request is
// already the network server's request, and re-encoding it would drop the fields
// this service does not know about — which is how a new ns field silently stops
// being settable from the console after an upgrade.
func modelBody(w http.ResponseWriter, r *http.Request) (json.RawMessage, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxModelBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, ErrModelTooLarge)
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

// requireController is the guard every relay shares: without a configured network
// server there is nothing to ask, and saying so beats a 502 that implies one is
// merely unreachable.
func (s *Server) requireController(w http.ResponseWriter) bool {
	if s.controller == nil {
		writeError(w, http.StatusServiceUnavailable, ErrControllerNotConfigured)
		return false
	}
	return true
}

func (s *Server) listModels(w http.ResponseWriter, r *http.Request) {
	if !s.requireController(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), controlTimeout)
	defer cancel()

	resp, err := s.controller.ListModels(ctx)
	if err != nil {
		writeUpstreamError(w, "list device models", "", err, ErrModelListFailed)
		return
	}
	writeRaw(w, http.StatusOK, resp)
}

func (s *Server) getModel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathModelID(w, r)
	if !ok || !s.requireController(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), controlTimeout)
	defer cancel()

	resp, err := s.controller.Model(ctx, id)
	if err != nil {
		writeUpstreamError(w, "read device model", id, err, ErrModelListFailed)
		return
	}
	writeRaw(w, http.StatusOK, resp)
}

func (s *Server) createModel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathModelID(w, r)
	if !ok || !s.requireController(w) {
		return
	}
	body, ok := modelBody(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), controlTimeout)
	defer cancel()

	resp, err := s.controller.CreateModel(ctx, id, body)
	if err != nil {
		writeUpstreamError(w, "create device model", id, err, ErrModelSaveFailed)
		return
	}
	// 201 comes from the network server's own verdict; a create that collided
	// arrives as a 409 through writeUpstreamError with its detail intact, which
	// is what lets the console say "edit it instead" rather than "save failed".
	writeRaw(w, http.StatusCreated, resp)
}

func (s *Server) updateModel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathModelID(w, r)
	if !ok || !s.requireController(w) {
		return
	}
	body, ok := modelBody(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), controlTimeout)
	defer cancel()

	resp, err := s.controller.UpdateModel(ctx, id, body)
	if err != nil {
		writeUpstreamError(w, "save device model", id, err, ErrModelSaveFailed)
		return
	}
	writeRaw(w, http.StatusOK, resp)
}

func (s *Server) deleteModel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathModelID(w, r)
	if !ok || !s.requireController(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), controlTimeout)
	defer cancel()

	resp, err := s.controller.DeleteModel(ctx, id)
	if err != nil {
		writeUpstreamError(w, "delete device model", id, err, ErrModelDeleteFailed)
		return
	}
	writeRaw(w, http.StatusOK, resp)
}

// testModelDecode relays a formatter trial run.
//
// A script that fails to compile or rejects the bytes still produces a 200 from
// the network server, with ok:false and the reason. That is passed through
// unchanged: the request was well formed, and turning the answer into a 4xx would
// make a console treat it as a transport failure and hide the message that names
// the offending line.
func (s *Server) testModelDecode(w http.ResponseWriter, r *http.Request) {
	id, ok := pathModelID(w, r)
	if !ok || !s.requireController(w) {
		return
	}
	body, ok := modelBody(w, r)
	if !ok {
		return
	}
	// A trial run executes untrusted JavaScript. The network server bounds it with
	// its own timeout; this relay adds its request deadline on top, so the worst
	// case is bounded twice rather than not at all.
	ctx, cancel := context.WithTimeout(r.Context(), controlTimeout)
	defer cancel()

	resp, err := s.controller.TestModelDecode(ctx, id, body)
	if err != nil {
		writeUpstreamError(w, "test device model", id, err, ErrModelTestFailed)
		return
	}
	writeRaw(w, http.StatusOK, resp)
}

// listUplinks relays recent stored uplinks, decoded where the device's model has
// a formatter.
//
// The limit is parsed and bounded here so an out-of-range value is a refusal that
// says so rather than a page of rows the console then has to trim.
func (s *Server) listUplinks(w http.ResponseWriter, r *http.Request) {
	if !s.requireController(w) {
		return
	}
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 || n > maxUplinkLimit {
			writeError(w, http.StatusBadRequest, ErrLimitOutOfRange)
			return
		}
		limit = n
	}
	devEUI := normalizeHex(r.URL.Query().Get("dev_eui"))
	if devEUI != "" && !devEUIPattern.MatchString(devEUI) {
		writeError(w, http.StatusBadRequest, ErrDevEUIHex)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), controlTimeout)
	defer cancel()

	resp, err := s.controller.Uplinks(ctx, devEUI, limit)
	if err != nil {
		writeUpstreamError(w, "read uplinks", devEUI, err, ErrUplinkListFailed)
		return
	}
	writeRaw(w, http.StatusOK, resp)
}

// validateModelIDField checks a model_id sent on a device edit, returning the code
// to report or "" when it is acceptable. An empty string is acceptable: it clears
// the association, which is a legitimate instruction rather than a missing value.
func validateModelIDField(value *string) string {
	if value == nil {
		return ""
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return ""
	}
	if utf8.RuneCountInString(trimmed) > 64 || !modelIDPattern.MatchString(trimmed) {
		return ErrModelIDInvalid
	}
	return ""
}
