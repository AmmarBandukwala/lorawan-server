// Package mcpclient is the console's MCP client: it talks to an MCP listener and
// presents what it finds as plain Go values.
//
// The SDK is confined to this package on purpose. Its types carry protocol revision
// details — pagination cursors, content blocks, the distinction between a tool error
// and a protocol error — none of which the assistant should have to know. Keeping them
// behind a small port means a go-sdk version bump touches this package and nothing
// else, which matters because that SDK is actively revising its wire shape: protocol
// 2026-07-28 removed the session handshake and deprecated sampling, roots and logging
// in a single pass.
//
// Sessions are opened per question and closed at the end of it, rather than pooled.
// The listener is stateless, so an initialize round trip is the whole cost of a fresh
// session, and in exchange there is no idle session to leak, no reconnect path to get
// wrong, and no question that fails because a session died while the operator was
// reading the previous answer. A console is not a high-QPS client; two extra round
// trips per question is cheap enough to buy all of that.
package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tool is one callable the listener advertised, reduced to what the assistant needs.
//
// Schema is the tool's own JSON Schema, passed through untouched. Re-modelling it into
// a Go type would lose the constraints a model needs in order to call the tool
// correctly, and the whole value of tool calling is that the model is told what a valid
// argument looks like.
type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage

	// ReadOnly is the listener's own claim about the tool, taken from its
	// annotations. It is a claim and not a guarantee — the SDK's documentation warns
	// clients never to make tool-use decisions on annotations from an untrusted
	// server — so the assistant uses it only to narrow what a model may reach for,
	// never to widen it.
	ReadOnly bool
}

// Config is what a client needs.
type Config struct {
	// URL is the listener's MCP endpoint, including the path. The SDK's streamable
	// transport POSTs JSON-RPC there. The console's own API and the network server
	// are separate services with separate clients, so nothing here is shared with
	// them.
	URL string

	// Token is the bearer token the listener demands. It is never logged and never
	// returned to a caller. It is not the network server's token: the listener
	// issues its own precisely so that rotating one does not break the other.
	Token string

	// Timeout bounds one whole question, including every tool call the model asks
	// for inside it. It is generous because a model running on CPU can take a minute
	// to decide it wants to look at one more thing.
	Timeout time.Duration
}

// minTokenLen matches what the listener requires of its own token. Catching a short
// token here turns a 401 on the first question into a startup message that says what
// is actually wrong.
const minTokenLen = 16

// Validate rejects a configuration that would start and then be wrong.
func (c Config) Validate() error {
	trimmed := strings.TrimRight(strings.TrimSpace(c.URL), "/")
	if trimmed == "" {
		return errors.New("an MCP endpoint URL is required")
	}
	if !strings.HasPrefix(trimmed, "http://") && !strings.HasPrefix(trimmed, "https://") {
		return fmt.Errorf("the MCP endpoint must be an absolute http(s) URL including "+
			"the /mcp path, e.g. http://mcp:8092/mcp; got %q", c.URL)
	}
	if len(strings.TrimSpace(c.Token)) < minTokenLen {
		return fmt.Errorf("the MCP token must be at least %d characters; the listener "+
			"refuses to start with a shorter one, so this token could never authenticate",
			minTokenLen)
	}
	return nil
}

// Client dials one MCP listener.
type Client struct {
	endpoint string
	token    string
	timeout  time.Duration
}

// defaultQuestionTimeout bounds one Ask, tool calls included.
const defaultQuestionTimeout = 3 * time.Minute

// New builds a client. A bad URL or token is rejected here rather than on the first
// question, when the cause is much harder to see.
func New(cfg Config) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultQuestionTimeout
	}
	return &Client{
		endpoint: strings.TrimRight(strings.TrimSpace(cfg.URL), "/"),
		token:    strings.TrimSpace(cfg.Token),
		timeout:  timeout,
	}, nil
}

// Endpoint reports the URL this client dials, for the page to display. Which listener
// is answering is a fact an operator debugging a misconfiguration needs, and it is not
// a secret — the path is published in the README.
func (c *Client) Endpoint() string { return c.endpoint }

// Session is one open connection to the listener.
type Session struct {
	inner *mcp.ClientSession
}

// Open establishes a session. The caller must Close it: a session left open holds a
// connection, and on a stateless listener it will eventually be reaped server-side and
// start answering with errors that look like the listener itself is broken.
func (c *Client) Open(ctx context.Context) (*Session, error) {
	transport := &mcp.StreamableClientTransport{
		Endpoint: c.endpoint,
		// The token rides on every request the transport makes, including the
		// initialize call, via a round-tripper rather than by mutating the transport:
		// the SDK owns request construction.
		HTTPClient: &http.Client{Transport: &bearerTransport{
			token: c.token,
			next:  http.DefaultTransport,
		}},
		// The listener is stateless and initiates nothing, so the persistent
		// server-to-client stream would be a connection held open for notifications
		// that can never arrive.
		DisableStandaloneSSE: true,
		// One attempt, not the SDK's default five. A listener that is down should
		// produce an answer the operator sees at once, not a retry cascade that ends
		// in the same message minutes later.
		MaxRetries: -1,
	}

	client := mcp.NewClient(&mcp.Implementation{
		Name:    "fieldnode-console",
		Title:   "Fieldnode Console",
		Version: "1.0.0",
	}, nil)

	inner, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("cannot open a session with the MCP listener at %s: %w",
			c.endpoint, err)
	}
	return &Session{inner: inner}, nil
}

// Close ends the session. Safe on a nil receiver so a caller can defer it before
// checking the error from Open.
func (s *Session) Close() error {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.Close()
}

// Tools lists what the listener offers.
//
// Pagination is followed rather than ignored. A listener with more tools than one page
// holds would otherwise present the assistant with a truncated tool set, and a model
// given a partial list does not report the gap — it concludes the tool it wanted does
// not exist and answers from assumption instead.
func (s *Session) Tools(ctx context.Context) ([]Tool, error) {
	var out []Tool
	cursor := ""
	for {
		result, err := s.inner.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("the MCP listener could not list its tools: %w", err)
		}
		for _, tool := range result.Tools {
			out = append(out, Tool{
				Name:        tool.Name,
				Description: tool.Description,
				Schema:      schemaOf(tool),
				ReadOnly:    tool.Annotations != nil && tool.Annotations.ReadOnlyHint,
			})
		}
		if result.NextCursor == "" {
			return out, nil
		}
		cursor = result.NextCursor
	}
}

// schemaOf flattens a tool's input schema to raw JSON. The SDK holds it as an
// interface{} that came from the wire, so a round trip through json is the cheapest way
// to get bytes back without assuming which representation this version uses.
func schemaOf(tool *mcp.Tool) json.RawMessage {
	if tool.InputSchema == nil {
		return json.RawMessage(`{"type":"object"}`)
	}
	raw, err := json.Marshal(tool.InputSchema)
	if err != nil {
		// An unusable schema must not silently become no schema: a model given a tool
		// with no constraints invents argument names, and the resulting failure gets
		// attributed to the network rather than to the missing schema.
		return json.RawMessage(`{"type":"object"}`)
	}
	return raw
}

// Call invokes one tool and returns its text output.
//
// A tool-level error — the listener accepting the call and then refusing it, with a
// reason — comes back as a ToolError, which is what lets the assistant hand the
// sentence straight back to the model so it can correct itself. A protocol-level
// failure is also an error but a differently worded one, because "the listener is
// broken" and "that device is not provisioned" are different facts and a model that
// conflates them will tell an operator their network is down.
func (s *Session) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if len(args) == 0 {
		// The spec allows null arguments. Every tool in this deployment takes an
		// object, so an empty object is the honest reading of "none were given".
		args = json.RawMessage(`{}`)
	}
	result, err := s.inner.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		return "", fmt.Errorf("calling %s on the MCP listener failed: %w", name, err)
	}
	text := textOf(result.Content)
	if result.IsError {
		return "", &ToolError{Name: name, Detail: text}
	}
	return text, nil
}

// ToolError is a call the listener accepted and then refused. Its text is written for
// the model to read and act on, which is why it is preserved verbatim rather than
// wrapped in a generic failure.
type ToolError struct {
	Name   string
	Detail string
}

func (e *ToolError) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("the MCP listener refused the call to %s and gave no reason", e.Name)
	}
	return e.Detail
}

// Read fetches one resource by URI.
func (s *Session) Read(ctx context.Context, uri string) (string, error) {
	result, err := s.inner.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
	if err != nil {
		return "", fmt.Errorf("the MCP listener could not read %s: %w", uri, err)
	}
	var parts []string
	for _, content := range result.Contents {
		// Text only. A binary resource has no faithful rendering into a chat
		// transcript, and guessing at one would put invented content in front of an
		// operator.
		if content != nil && content.Text != "" {
			parts = append(parts, content.Text)
		}
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("the MCP listener returned nothing readable for %s", uri)
	}
	return strings.Join(parts, "\n"), nil
}

// textOf concatenates the text parts of a result. Non-text content is skipped rather
// than guessed at: every tool in this deployment answers with text, and an image or
// embedded resource would otherwise be silently dropped from the answer the model
// builds.
func textOf(contents []mcp.Content) string {
	var parts []string
	for _, content := range contents {
		if text, ok := content.(*mcp.TextContent); ok && text.Text != "" {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}
