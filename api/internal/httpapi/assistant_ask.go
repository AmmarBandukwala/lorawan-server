package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fieldnode/lorawan-server/api/internal/assistant"
	"github.com/fieldnode/lorawan-server/api/internal/llm"
)

// assistantRequest is the body of a question.
//
// History travels with it because this endpoint is stateless: no conversation is
// stored server-side, so a reload does not resurrect a session that was never
// persisted, and nothing accumulates in a process meant to run for months on a
// machine that also runs a packet forwarder.
type assistantRequest struct {
	Question string             `json:"question"`
	History  []assistantHistory `json:"history"`
}

type assistantHistory struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// transcript assembles what the assistant replays: the earlier turns, then this
// question as the newest user turn.
func (a assistantRequest) transcript() []llm.Message {
	messages := make([]llm.Message, 0, len(a.History)+1)
	for _, turn := range a.History {
		messages = append(messages, llm.Message{Role: turn.Role, Content: turn.Content})
	}
	return append(messages, llm.Message{Role: llm.RoleUser, Content: a.Question})
}

// askAssistant serves POST /api/v1/assistant/ask.
//
// The answer streams as server-sent events. A model on CPU takes tens of seconds, and
// a request that shows nothing for that long is indistinguishable from one that has
// failed; an operator who believes it failed reloads the page and starts the expensive
// generation over again. Every event is flushed the moment it exists.
func (s *Server) askAssistant(w http.ResponseWriter, r *http.Request) {
	if s.assistant == nil {
		writeError(w, http.StatusServiceUnavailable, ErrAssistantNotConfigured)
		return
	}
	flusher, canFlush := w.(http.Flusher)
	if !canFlush {
		writeError(w, http.StatusInternalServerError, ErrAssistantCannotStream)
		return
	}

	payload, ok := decodeAssistantRequest(w, r)
	if !ok {
		return
	}

	// The server sets a write deadline that suits an ordinary API response, and a
	// streamed answer outlives it by design. Clearing it here rather than raising it
	// globally keeps a hung ordinary request dying on the clock it always did; this
	// route is bounded instead by the client disconnecting, which cancels the context
	// and stops the generation.
	if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil {
		// Not fatal: a writer that refuses to clear the deadline still streams, it
		// just stops at the server's timeout. One header so the page can explain a
		// truncated answer rather than ending mid-sentence silently.
		w.Header().Set("X-Assistant-Deadline", "unchanged")
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	// nginx buffers a response by default, which would collect the whole answer and
	// deliver it at the end — the exact failure this endpoint exists to avoid. The
	// header is a no-op for a server that does not know it and a fix for the one that
	// does.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	stream := &sseWriter{w: w, flusher: flusher}
	err := s.assistant.Ask(r.Context(), payload.transcript(), stream.write)
	switch {
	case err == nil, errors.Is(err, context.Canceled):
		// A cancelled context means the operator closed the tab. That is not a fault,
		// and logging it would fill the log with the browser's behaviour.
	case stream.broken:
		// The page is already gone; there is nobody left to tell.
	default:
		// The error reached the page as an in-stream frame from inside Ask. This is
		// for whoever owns the server, which is often the same person.
		slog.Error("assistant answer failed", "endpoint", s.assistantInfo.Endpoint, "err", err)
	}
}

// decodeAssistantRequest reads and validates the body, writing its own rejection.
func decodeAssistantRequest(w http.ResponseWriter, r *http.Request) (assistantRequest, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxAssistantBodyBytes)

	// Unknown fields are refused rather than ignored, as everywhere else in this API:
	// a client that misspells "history" would otherwise get an answer to a question
	// asked without any of the context it meant to supply, and a wrong answer that
	// looks confident is worse than a 400.
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	var payload assistantRequest
	if err := decoder.Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidJSON)
		return assistantRequest{}, false
	}
	if decoder.More() {
		writeError(w, http.StatusBadRequest, ErrInvalidJSON)
		return assistantRequest{}, false
	}

	question := strings.TrimSpace(payload.Question)
	// Counted in runes: an operator asking in a non-Latin script is not writing
	// longer questions than one writing in Latin.
	if question == "" || utf8.RuneCountInString(question) > MaxQuestionRunes {
		writeError(w, http.StatusBadRequest, ErrQuestionRequired)
		return assistantRequest{}, false
	}
	if len(payload.History) > MaxHistoryTurns {
		writeError(w, http.StatusBadRequest, ErrTooManyTurns)
		return assistantRequest{}, false
	}
	for _, turn := range payload.History {
		// Only user and assistant survive. A client-supplied system turn is an
		// instruction the operator never wrote — the assistant builds that turn
		// itself, and it is where the read-only restriction lives — and a tool turn
		// would let a browser plant an answer the model never produced.
		if turn.Role != llm.RoleUser && turn.Role != llm.RoleAssistant {
			writeError(w, http.StatusBadRequest, ErrRoleNotAllowed)
			return assistantRequest{}, false
		}
		if strings.TrimSpace(turn.Content) == "" {
			writeError(w, http.StatusBadRequest, ErrQuestionRequired)
			return assistantRequest{}, false
		}
	}
	payload.Question = question
	return payload, true
}

// sseWriter turns assistant events into server-sent-event frames.
//
// A write failure is remembered rather than raised again on later events: once the
// peer is gone every subsequent write fails too, and the loop wants to stop on the
// first one, which is what returning an error from emit achieves.
type sseWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	broken  bool
}

func (s *sseWriter) write(event assistant.Event) error {
	if s.broken {
		return context.Canceled
	}
	frame, err := json.Marshal(event)
	if err != nil {
		// An event that cannot be marshalled is a programming error in the assistant,
		// not a network condition, and the answer cannot continue without it.
		return fmt.Errorf("assistant produced an event that could not be encoded: %w", err)
	}
	// One data line carrying the event's own JSON, whose "type" field is what the page
	// switches on. Named SSE event types were the alternative and were dropped: they
	// would put the same information in two places, and the two drift.
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", frame); err != nil {
		s.broken = true
		return err
	}
	s.flusher.Flush()
	return nil
}
