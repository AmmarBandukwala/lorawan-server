// Package llm talks to an OpenAI-compatible chat-completions endpoint.
//
// The console's assistant has to ask a model something, and the two obvious ways
// to do that are both worse than the third. Importing a provider SDK pins the
// deployment to one vendor and turns this module's single dependency into a
// dozen. Calling a vendor-specific API buys nothing, because every runtime worth
// targeting — Ollama, LM Studio, vLLM, OpenAI, OpenRouter — already speaks the
// OpenAI chat-completions shape, including tool calling. So this package speaks
// that shape over net/http and nothing else.
//
// The default points at Ollama on the internal Docker network rather than at a
// hosted API, and that is a deliberate position rather than an accident of
// configuration. The README's argument for this project is explaining a device
// "without routing the telemetry through a third-party cloud"; an assistant that
// posts device names, groups, locations and event history to a vendor would
// contradict the sentence above it. A hosted endpoint remains available by setting
// LLM_BASE_URL, and the console states which endpoint is answering so the choice is
// never invisible.
//
// Two properties of local inference shaped the API here. Timeouts are split into
// connect, first-token and total, because a model loading itself into RAM for
// forty seconds is a different fact from a model that is wedged, and collapsing
// them into one client-wide deadline — the shape that works everywhere else in this
// service — would either kill cold starts or never notice a hang. And streaming is
// not an optimisation: on CPU, generation is slow enough that a response arriving
// all at once reads as a hang.
package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Roles, as the endpoint names them. Exported because the caller assembles the
// transcript and must not invent its own spellings for these.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// Tool-choice values accepted by Request.ToolChoice.
const (
	ToolChoiceAuto     = "auto"
	ToolChoiceNone     = "none"
	ToolChoiceRequired = "required"
)

// Message is one turn of the transcript.
//
// ToolCalls exists because the OpenAI shape is stateless in a way that surprises
// people: when a model asks for a tool, the caller must send the assistant message
// containing that call back alongside the tool's result, or the model has no idea
// what it already asked for and asks again. Omitting it is the most common way to
// build a tool loop that loops forever.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`

	// ToolCallID is set on a RoleTool message to say which call this answers.
	ToolCallID string `json:"tool_call_id,omitempty"`

	// ToolCalls is set on a RoleAssistant message being replayed into the next
	// request. Content is usually empty on such a message.
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// Tool is one callable, in the wrapper the endpoint expects.
type Tool struct {
	Type     string   `json:"type"`
	Function ToolSpec `json:"function"`
}

// ToolSpec describes a callable. Parameters is raw JSON Schema, passed through
// untouched: the MCP server already publishes schemas for its tools, and
// re-modelling them into a Go type here would lose whatever a Go struct has no
// field for.
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// FunctionCall is the body of a tool call. Arguments are a JSON *string*, not an
// object — the endpoint's convention, not ours, and the reason this is not typed.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolCall is one call the model asked for.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// Request is a chat-completions call. Model is part of the request rather than
// fixed on the Client so a deployment can run a cheap model for summaries and a
// capable one for diagnosis without holding two clients.
type Request struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Tools       []Tool    `json:"tools,omitempty"`
	ToolChoice  string    `json:"tool_choice,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
	Stream      bool      `json:"stream,omitempty"`

	// StreamOptions is set by Stream to ask for token usage on the final chunk. It
	// is a pointer because the field must be absent, not false, when not streaming:
	// some runtimes reject a request that carries it with stream unset.
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
}

// Usage is the token accounting the endpoint reports, when it reports any.
//
// Several local runtimes omit it, so it is advisory. A budget guard that refused to
// act unless usage arrived would disable itself on exactly the runtimes this project
// defaults to.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Config is what a Client needs. It is validated eagerly: a deployment that has
// mistyped LLM_BASE_URL should fail at startup with the bad value in the message,
// not on the first question an operator asks.
type Config struct {
	// BaseURL is the endpoint root, e.g. http://ollama:11434/v1. The version
	// segment is part of the value rather than appended here because runtimes
	// disagree about it — Ollama and vLLM use /v1, LM Studio serves both /v1 and
	// root, and some proxies mount the API under a path of their own.
	BaseURL string

	// APIKey is optional. Local runtimes do not check one; a hosted endpoint
	// requires it. It is never logged and never returned to a caller.
	APIKey string

	// Model is the default for requests that do not name one.
	Model string

	// ConnectTimeout bounds establishing the connection and receiving response
	// headers. Local inference is fast to accept a request and slow to answer it,
	// so this stays short while the total budget does not.
	ConnectTimeout time.Duration

	// FirstTokenTimeout bounds the wait for the first streamed token. It is
	// separate because a model loading itself into memory takes tens of seconds
	// and is normal, while a model that has begun and stalled is not.
	FirstTokenTimeout time.Duration

	// TotalTimeout bounds one whole generation. It is the only large number here,
	// and it is what stops a wedged runtime from holding a browser connection
	// open indefinitely.
	TotalTimeout time.Duration
}

// Defaults for a deployment that configures nothing beyond a base URL.
const (
	defaultConnectTimeout   = 10 * time.Second
	defaultFirstTokenBudget = 120 * time.Second
	defaultTotalTimeout     = 5 * time.Minute
)

// Validate rejects a configuration that would start and then be wrong.
func (c Config) Validate() error {
	if strings.TrimSpace(c.BaseURL) == "" {
		return errors.New("an LLM base URL is required")
	}
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(c.BaseURL), "/"))
	if err != nil {
		return fmt.Errorf("LLM_BASE_URL is not a valid URL: %w", err)
	}
	if parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("LLM_BASE_URL must be an absolute http(s) URL including the "+
			"version segment, e.g. http://ollama:11434/v1; got %q", c.BaseURL)
	}
	if strings.TrimSpace(c.Model) == "" {
		return errors.New("an LLM model name is required")
	}
	return nil
}

// Client calls one chat-completions endpoint. It is safe for concurrent use: the
// underlying transport is an http.Client, and nothing else is mutable state.
type Client struct {
	base   string
	apiKey string
	model  string
	http   *http.Client
	// connect and first are kept because the transport is built from both: changing
	// either one after construction means rebuilding it, and holding only the client
	// would leave a setter that silently changed one budget and not the other.
	connect time.Duration
	first   time.Duration
	total   time.Duration
}

// Option adjusts a Client at construction.
type Option func(*Client)

// WithTimeouts overrides the three budgets. Zero values keep their default, so a
// caller can pass a Config's fields straight through without normalising them first.
func WithTimeouts(connect, firstToken, total time.Duration) Option {
	return func(c *Client) {
		if connect > 0 {
			c.connect = connect
		}
		if firstToken > 0 {
			c.first = firstToken
		}
		if total > 0 {
			c.total = total
		}
		c.http = newHTTPClient(c.connect, c.first)
	}
}

// newHTTPClient expresses the connect and first-response budgets on the transport
// rather than as http.Client.Timeout, and that distinction is the whole point of having
// three budgets at all.
//
// Client.Timeout bounds the entire exchange including the response body, so putting the
// connect value there would cut a CPU generation off at ten seconds while this package
// promises a five-minute total.
//
// ResponseHeaderTimeout takes the first-token budget, not the connect one, and that is
// the part that was wrong before it was run against a local model. A hosted endpoint
// sends headers in well under a second, so a ten-second ceiling looked generous. A
// runtime loading a 7B model onto a CPU takes the better part of a minute to produce its
// first byte, headers included, so the transport killed the request at ten seconds —
// before the stream reader that enforces the 120-second first-token budget was ever
// reached, which made that budget unreachable and the failure indistinguishable from an
// endpoint that was down. Dial stays on the connect budget: that is the one that means
// "nothing is listening there".
func newHTTPClient(connect, first time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: connect, KeepAlive: 30 * time.Second}
	return &http.Client{
		Transport: &http.Transport{
			DialContext: dialer.DialContext,
			// How long to wait for the model to start answering, not how long to
			// wait for a socket. See above.
			ResponseHeaderTimeout: first,
			// A streaming response is one long-lived body; the pool must not impose
			// its own ceiling on how long a connection may live.
			IdleConnTimeout: 90 * time.Second,
			MaxIdleConns:    8,
		},
	}
}

// New builds a client. A bad URL is rejected here rather than on the first
// completion, when the cause is much harder to see.
func New(cfg Config, opts ...Option) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	c := &Client{
		base:    strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"),
		apiKey:  strings.TrimSpace(cfg.APIKey),
		model:   strings.TrimSpace(cfg.Model),
		connect: defaultConnectTimeout,
		first:   defaultFirstTokenBudget,
		total:   defaultTotalTimeout,
	}
	// The config's own budgets win over the defaults, and WithTimeouts still wins
	// over both, so a caller has exactly one way to override and it is the last one
	// applied.
	if cfg.ConnectTimeout > 0 {
		c.connect = cfg.ConnectTimeout
	}
	if cfg.FirstTokenTimeout > 0 {
		c.first = cfg.FirstTokenTimeout
	}
	if cfg.TotalTimeout > 0 {
		c.total = cfg.TotalTimeout
	}
	// Built after the budgets are settled, because the transport is derived from two
	// of them: building it first is how the first-token budget ended up not applying
	// to the one transport setting that decides whether a slow model gets answered.
	c.http = newHTTPClient(c.connect, c.first)
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// Model reports the configured default, so a status endpoint can say which model is
// answering without reaching into unexported fields.
func (c *Client) Model() string { return c.model }

// Endpoint reports the base URL. Reported to the UI on purpose: the whole argument
// for local inference is that telemetry does not leave the network, and that claim is
// only reassuring if the page shows where the traffic goes.
func (c *Client) Endpoint() string { return c.base }

// --- Errors ------------------------------------------------------------------

// AuthError means the endpoint rejected the credential. No retry and no rephrasing
// fixes it, so the assistant reports it as a deployment problem.
type AuthError struct{ Detail string }

func (e *AuthError) Error() string {
	if e.Detail == "" {
		return "the model endpoint rejected this server's API key"
	}
	return e.Detail
}

// APIError is any other rejection, carrying the endpoint's own message. Provider
// error text is usually specific enough to act on, and replacing it with "the model
// failed" throws that away.
type APIError struct {
	Status int
	Detail string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("the model endpoint returned %d: %s", e.Status, e.Detail)
}

// RateLimitError is a 429 kept distinct because the right response is to back off or
// tell the operator to, not to retry immediately inside the loop and burn the budget
// on a model that has already said no.
type RateLimitError struct{ Detail string }

func (e *RateLimitError) Error() string {
	if e.Detail == "" {
		return "the model endpoint is rate limiting this server"
	}
	return e.Detail
}

// Response is an assembled completion.
type Response struct {
	Model        string
	Content      string
	ToolCalls    []ToolCall
	FinishReason string
	Usage        Usage
}
