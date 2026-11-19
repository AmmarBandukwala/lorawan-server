package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
)

// writeEngineError maps the engine's errors onto HTTP statuses a client can act
// on. The distinction matters: 404 means provision the device, 409 means it
// exists but cannot receive a frame yet, and neither is worth retrying blindly.
func (s *apiServer) writeEngineError(w http.ResponseWriter, devEUI lorawan.EUI64, err error) {
	switch {
	case errors.Is(err, engine.ErrNotFound):
		// The engine's message names the DevEUI and says what is missing, which
		// is safe to show and far more useful than a bare "not found".
		notFound(w, err.Error())
	case errors.Is(err, engine.ErrPendingDownlinkExists):
		writeError(w, http.StatusConflict,
			"the device already has a downlink waiting; the network server keeps one "+
				"per device so a queue cannot silently reorder application frames. "+
				"DELETE the pending frame first")
	default:
		// Logged in full, reported in general: an internal message can carry SQL
		// or a connection string.
		s.log.Error("API request failed", "dev_eui", devEUI, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

// pathDevEUI parses the {dev_eui} path segment. A malformed identifier is the
// client's problem and is reported as such rather than reaching the store.
func pathDevEUI(w http.ResponseWriter, r *http.Request) (lorawan.EUI64, bool) {
	raw := r.PathValue("dev_eui")
	v, err := lorawan.ParseEUI64(raw)
	if err != nil {
		badRequest(w, fmt.Sprintf("dev_eui %q is not 16 hex digits", raw))
		return lorawan.EUI64{}, false
	}
	return v, true
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// The status line is already on the wire; logging is all that is left.
		slog.Default().Error("encode the API response", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func badRequest(w http.ResponseWriter, msg string) { writeError(w, http.StatusBadRequest, msg) }
func notFound(w http.ResponseWriter, msg string)   { writeError(w, http.StatusNotFound, msg) }

func unauthorized(w http.ResponseWriter, msg string) {
	// WWW-Authenticate makes a browser or curl report the reason instead of
	// failing with an opaque connection error.
	w.Header().Set("WWW-Authenticate", `Bearer realm="fieldnode-ns"`)
	writeError(w, http.StatusUnauthorized, msg)
}
