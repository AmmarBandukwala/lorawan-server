// Package assistant answers questions about the network by asking a model, and letting
// that model look things up.
//
// It is deliberately narrow, and the narrowness is the design.
//
// Everything the model can reach is a read. Tools are filtered to the ones the MCP
// listener itself marks read-only before the model is ever told they exist, so a write
// is not something the model must be told not to do — it is not offered. That is a
// stronger position than a prompt saying "please do not change anything", for two
// reasons: a model that has decided on a course of action is poor at restraining
// itself, and a prompt is not an access control. Writes stay where they already are,
// behind the console's own buttons, which show a confirmation dialog a human can read.
//
// The model is not trusted to know this network. It is given tools and told to use
// them, and the system prompt says so in as many words. A model answering from general
// LoRaWAN knowledge will confidently describe behaviour this build does not implement —
// class C is the standing example — and an operator cannot tell that from a correct
// answer.
//
// The loop is bounded. A small local model can decide to look at one more thing
// forever, and an unbounded loop on CPU is not a slow answer, it is a hung request and
// a loaded machine. The limits are low and stated, because the useful failure is "I ran
// out of lookups, here is what I have" rather than silence.
package assistant

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fieldnode/lorawan-server/api/internal/llm"
	"github.com/fieldnode/lorawan-server/api/internal/mcpclient"
)

// Network is everything the assistant needs from an MCP listener. It is satisfied by
// *mcpclient.Session and exists so the loop can be tested without a socket.
//
// Reading resources is deliberately absent: the vocabulary is fetched once at startup
// and passed in through Config, so the loop's port stays the two things it actually
// does — ask what is callable, and call it.
type Network interface {
	Tools(context.Context) ([]mcpclient.Tool, error)
	Call(ctx context.Context, name string, args json.RawMessage) (string, error)
}

// Model is the chat endpoint. Stream rather than Complete because on CPU a response
// that arrives all at once reads as a hang, and an operator who thinks the assistant
// has hung will reload the page and start the expensive generation over again.
type Model interface {
	Stream(ctx context.Context, req llm.Request, onContent func(string) error) (*llm.Response, error)
}

// Limits on one question. Both are low on purpose.
const (
	// maxRounds bounds how many times the model may ask for tools and get answers.
	// Five is generous for "why is this device not joining", which is four lookups at
	// most: device, session, events, and one explanation of a reason string.
	maxRounds = 5

	// maxCalls bounds total tool invocations across all rounds, so one round in which
	// a model asks for thirty devices cannot spend the whole budget.
	maxCalls = 12
)

// Event is one thing that happens while a question is being answered. The handler
// turns these into SSE frames; nothing here knows about HTTP.
//
// The shape is deliberately chatty. A model on CPU takes tens of seconds, and an
// interface that shows nothing for that long is indistinguishable from one that has
// failed. Each event says what is happening so the page can say it too.
type Event struct {
	// Type is one of the Event* constants below.
	Type string `json:"type"`

	// Text carries the payload for status, token and error events.
	Text string `json:"text,omitempty"`

	// Tool names the tool a tool_call or tool_result event is about.
	Tool string `json:"tool,omitempty"`

	// Arguments is what the model asked to call the tool with, shown to the operator
	// so an answer can be checked against what was actually looked up.
	Arguments json.RawMessage `json:"arguments,omitempty"`

	// OK reports whether a tool call succeeded. The failure text is not repeated
	// here: it went back to the model, which is the party that has to act on it.
	OK bool `json:"ok,omitempty"`

	// Model and Endpoint appear on the done event. Which model answered, and from
	// where, is the fact an operator needs in order to trust or discount the answer.
	Model    string `json:"model,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`

	// Tokens appears on done when the endpoint reported usage.
	Tokens int `json:"tokens,omitempty"`
}

// Event types.
const (
	EventStatus     = "status"
	EventToolCall   = "tool_call"
	EventToolResult = "tool_result"
	EventToken      = "token"
	EventError      = "error"
	EventDone       = "done"
)

// Service answers questions against one network and one model.
type Service struct {
	network Network
	// dial, when set, supplies a fresh network per question. See Config.Dial.
	dial  func(context.Context) (Network, func(), error)
	model Model
	kb    *Knowledge

	// modelID is empty unless the caller wants to override the model's default.
	modelID string
	// endpoint is reported on the done event so the page can name what answered.
	endpoint string
}

// Config wires a Service.
type Config struct {
	Network Network
	Model   Model

	// Dial opens a session for one question and returns the function that releases
	// it. It is the alternative to Network, for a transport where a connection is
	// per-conversation rather than per-process — an MCP session is exactly that, and
	// holding one open for the life of the console would mean every question after a
	// listener restart fails until the console restarts too.
	//
	// Set at most one of Network and Dial. Tests pass Network; a real deployment
	// passes Dial.
	Dial func(ctx context.Context) (Network, func(), error)

	// Vocabulary is the event-reason text read from the listener's
	// fieldnode://event-vocabulary resource, or empty when it could not be read. It
	// is passed in rather than fetched here so that a listener which is down
	// produces one clear error at the top of the answer instead of a half-built
	// prompt.
	Vocabulary string

	// ModelID and Endpoint are for display.
	ModelID  string
	Endpoint string
}

// New builds a service. A missing dependency is a programming error rather than a
// runtime state, so it is reported at construction, where both the compiler and the
// startup path can see it.
func New(cfg Config) (*Service, error) {
	if cfg.Network == nil && cfg.Dial == nil {
		return nil, fmt.Errorf("assistant: a network or a dialer is required")
	}
	if cfg.Model == nil {
		return nil, fmt.Errorf("assistant: a model is required")
	}
	return &Service{
		network:  cfg.Network,
		dial:     cfg.Dial,
		model:    cfg.Model,
		kb:       NewKnowledge(cfg.Vocabulary),
		modelID:  cfg.ModelID,
		endpoint: cfg.Endpoint,
	}, nil
}

// session returns what one question should read through, and the function that lets
// go of it. Without a dialer that is the fixed network and a no-op, which is why the
// tests never mention the difference.
func (s *Service) session(ctx context.Context) (Network, func(), error) {
	if s.dial == nil {
		return s.network, func() {}, nil
	}
	return s.dial(ctx)
}
