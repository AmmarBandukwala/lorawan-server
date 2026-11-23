package httpapi

import (
	"context"
	"net/http"

	"github.com/fieldnode/lorawan-server/api/internal/assistant"
	"github.com/fieldnode/lorawan-server/api/internal/llm"
)

// Assistant answers one question about the network, emitting events as it works.
//
// It is a port rather than a *assistant.Service so the handler can be tested without
// a model or an MCP listener, and so "no assistant is configured" is a nil value
// rather than a state every caller has to remember — the same shape as AgentAccess.
type Assistant interface {
	Ask(ctx context.Context, transcript []llm.Message, emit func(assistant.Event) error) error
}

// AssistantInfo describes the configured assistant for the page. It is passed in
// rather than read from the Service because the handler only ever displays it, and an
// interface with getters for three strings would be three more methods for the fake.
type AssistantInfo struct {
	// Model and Endpoint are what the page names in the footer of an answer. The
	// endpoint is included on purpose: the argument for a local model is that nothing
	// leaves the network, and that claim is only reassuring if the page shows where
	// the request went.
	Model    string
	Endpoint string
	// HasVocabulary records whether the event vocabulary was read from the listener.
	// Without it the assistant still answers, but it will paraphrase reason strings
	// instead of quoting them, which is worth saying out loud.
	HasVocabulary bool
}

// WithAssistant enables the assistant endpoints. A nil assistant is ignored, which
// lets main pass WithAssistant(cfg.Assistant, cfg.info()) and let an unset LLM_URL
// mean "configured off" without a branch at the call site.
func WithAssistant(a Assistant, info AssistantInfo) Option {
	return func(s *Server) {
		if a != nil {
			s.assistant = a
			s.assistantInfo = info
		}
	}
}

// assistantView is what the page is told about the assistant before it asks anything.
type assistantView struct {
	Configured    bool   `json:"configured"`
	Model         string `json:"model"`
	Endpoint      string `json:"endpoint"`
	HasVocabulary bool   `json:"has_vocabulary"`
	Detail        string `json:"detail"`
}

// getAssistant serves GET /api/v1/assistant.
//
// 200 whether or not an assistant is configured, for the reason that governs
// /api/v1/mcp: this service succeeded at reporting its own state, and a 503 would
// route the operator to the console's health rather than to the missing variable.
func (s *Server) getAssistant(w http.ResponseWriter, _ *http.Request) {
	if s.assistant == nil {
		writeJSON(w, http.StatusOK, assistantView{
			Configured: false,
			Detail: "no model is configured. Set LLM_BASE_URL and LLM_MODEL on the " +
				"console API and restart it; the assistant answers nothing until then",
		})
		return
	}
	writeJSON(w, http.StatusOK, assistantView{
		Configured:    true,
		Model:         s.assistantInfo.Model,
		Endpoint:      s.assistantInfo.Endpoint,
		HasVocabulary: s.assistantInfo.HasVocabulary,
	})
}
