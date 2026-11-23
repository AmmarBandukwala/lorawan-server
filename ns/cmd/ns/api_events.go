package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
	"github.com/fieldnode/lorawan-server/ns/internal/pgstore"
)

// Event history: what the network heard, and what it refused and why.
//
// This endpoint exists because state alone does not explain a device. A session
// read says the network believes the device sits at data rate 5 with counter 4117;
// only the trace says it got there through four rejected frames and one ADR
// decision. The engine has recorded all of it since M2 — every kind and reason
// string is pinned in internal/engine/events.go — and until now the dashboard was
// the only thing that could read it.
//
// It is read-only, and it sits behind the same token as everything else here.
// Per-device history is precisely what the unauthenticated /status endpoint
// declines to carry, and that division is the reason this route belongs on this
// server and not on that one.

// eventView is the read shape of one trace entry.
//
// Kind and reason are returned as the recorded pair rather than as a sentence.
// The dashboard assembles its own wording from them, and a client filtering
// history needs values that do not change when a rendering does.
type eventView struct {
	EventID    int64  `json:"event_id"`
	OccurredAt string `json:"occurred_at"`
	Kind       string `json:"kind"`
	Reason     string `json:"reason,omitempty"`
	Detail     string `json:"detail,omitempty"`

	// All three identifiers are optional: a frame the server could not attribute
	// to a session is recorded with none of them, and that absence is the finding
	// rather than a gap in the record.
	DevEUI    string `json:"dev_eui,omitempty"`
	DevAddr   string `json:"dev_addr,omitempty"`
	GatewayID string `json:"gateway_id,omitempty"`

	FCntUp uint32 `json:"f_cnt_up,omitempty"`

	// FPort is absent for a join or a MAC exchange, which have no application
	// port. The store carries that as -1; rendering it as port 0 would be a
	// different and wrong claim.
	FPort *int `json:"f_port,omitempty"`
}

func newEventView(row pgstore.EventRow) eventView {
	v := eventView{
		EventID:    row.EventID,
		OccurredAt: row.OccurredAt.UTC().Format(time.RFC3339Nano),
		Kind:       row.Kind,
		Reason:     row.Reason,
		Detail:     row.Detail,
		DevEUI:     row.DevEUI,
		DevAddr:    row.DevAddr,
		GatewayID:  row.GatewayID,
		FCntUp:     row.FCntUp,
	}
	if row.FPort >= 0 {
		fPort := row.FPort
		v.FPort = &fPort
	}
	return v
}

// knownEventKinds is every kind the engine records, in the order the vocabulary
// file lists them.
//
// It names the constants rather than repeating their values, so a kind becomes
// filterable the moment it is recorded and cannot quietly go missing from this
// list. The order is fixed because it appears in a rejection message, and a
// message that reshuffles itself between two calls is a message nobody can quote
// back.
var knownEventKinds = []string{
	engine.EventJoinRequest,
	engine.EventJoinAccept,
	engine.EventJoinReject,
	engine.EventUplink,
	engine.EventDownlink,
	engine.EventUplinkDrop,
	engine.EventMAC,
	engine.EventError,
}

func isKnownEventKind(kind string) bool {
	for _, k := range knownEventKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// handleListEvents returns recent trace entries, newest first, optionally for one
// device or one kind.
func (s *apiServer) handleListEvents(w http.ResponseWriter, r *http.Request) {
	if s.traffic == nil {
		// The in-memory store keeps its records in engine shapes and has no event
		// table. An empty list here would read as a network where nothing happened.
		writeError(w, http.StatusServiceUnavailable,
			"event history is not available with this database configuration")
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			badRequest(w, "limit must be a positive number")
			return
		}
		limit = n
	}
	devEUI := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("dev_eui")))
	if devEUI != "" {
		if _, err := lorawan.ParseEUI64(devEUI); err != nil {
			badRequest(w, "dev_eui must be 16 hexadecimal characters")
			return
		}
	}
	// An unrecognised kind is refused rather than passed down: the store would
	// answer with an empty list, and "no join-accepts have happened" is a very
	// different thing from "you asked for jion_accept".
	kind := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("kind")))
	if kind != "" && !isKnownEventKind(kind) {
		badRequest(w, fmt.Sprintf("kind %q is not one of: %s",
			kind, strings.Join(knownEventKinds, ", ")))
		return
	}

	rows, err := s.traffic.RecentEvents(r.Context(), devEUI, kind, limit)
	if err != nil {
		s.log.Error("cannot read recent events", "err", err)
		writeError(w, http.StatusInternalServerError, msgEventListFailed)
		return
	}
	views := make([]eventView, 0, len(rows))
	for _, row := range rows {
		views = append(views, newEventView(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": views})
}
