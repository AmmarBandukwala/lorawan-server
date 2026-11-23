// Package nsclient reads the Fieldnode network server's application API.
//
// Two decisions here are inherited from the console's client in
// api/internal/nsclient, and both exist because of bugs that client already had
// to fix:
//
//   - Reads return json.RawMessage, not decoded structs. The network server's own
//     JSON is the contract. Re-modelling it here would turn a field rename into a
//     tool result that quietly goes blank, which is the worst possible failure for
//     an agent: it would report "no data rate" about a device that has one.
//   - Route prefixes are named once, in one place, and pinned by a test against
//     the path a request actually carries. ns and mcp are separate modules with no
//     shared package, so a wrong route is not a compile error and not a test
//     failure either — it is a 404 the caller misreads as a missing device.
package nsclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Route prefixes. These must match the mux in ns/cmd/ns/api.go verbatim.
const (
	devicesRoute      = "/api/v1/devices"
	uplinksRoute      = "/api/v1/uplinks"
	eventsRoute       = "/api/v1/events"
	modelsRoute       = "/api/v1/models"
	integrationsRoute = "/api/v1/integrations"

	// statusRoute is served by the status server, not the API server, which is
	// why it is fetched from a different base URL and without a token.
	statusRoute = "/status"
)

const (
	// requestTimeout bounds one read. The network server is a long-running process
	// on the same host or network; a read taking longer than this is not going to
	// come back usefully, and an agent waiting on it is stuck.
	requestTimeout = 10 * time.Second

	// maxResponseBytes caps a read. The device list is an unbounded query on the ns
	// side, so the cap is what stops a large fleet from being pulled into this
	// process — and then into a model's context — on one tool call. Handing it back
	// truncated would be worse than refusing: truncated JSON is not JSON.
	maxResponseBytes = 4 << 20
)

// NotFoundError means the network server says the thing does not exist. It is a
// distinct type so a tool can say "provision it first" rather than "the request
// failed": for a device that is a different conversation, and the ns's own message
// names the DevEUI.
type NotFoundError struct{ Detail string }

func (e *NotFoundError) Error() string { return e.Detail }

// AuthError means the token was rejected. Nothing an agent does can fix that, so
// tools report it as a deployment problem rather than a retryable failure.
type AuthError struct{ Detail string }

func (e *AuthError) Error() string {
	if e.Detail == "" {
		return "the network server rejected this server's token (NS_API_TOKEN)"
	}
	return e.Detail
}

// APIError is any other rejection, carrying the network server's sentence. That
// API returns a human-readable reason rather than a code, and the reason is usually
// the most useful thing the agent will see in the whole exchange.
type APIError struct {
	Status int
	Detail string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("network server returned %d: %s", e.Status, e.Detail)
}

// Client reads the network server.
type Client struct {
	apiURL    string
	statusURL string
	token     string
	http      *http.Client
}

// Option adjusts a Client at construction.
type Option func(*Client)

// WithStatusURL points the health read at a different base URL. An empty value is
// ignored so a caller can pass an unset environment variable and keep the default.
func WithStatusURL(raw string) Option {
	return func(c *Client) {
		if trimmed := strings.TrimRight(strings.TrimSpace(raw), "/"); trimmed != "" {
			c.statusURL = trimmed
		}
	}
}

// WithHTTPClient replaces the transport, so a test can point a client at an
// httptest server.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

// New builds a client. A bad URL is rejected here rather than on the first tool
// call, when the cause is much harder to see.
func New(apiURL, token string, opts ...Option) (*Client, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(apiURL), "/")
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("nsclient: invalid API URL: %w", err)
	}
	if parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("nsclient: API URL must be an absolute http(s) URL, got %q", apiURL)
	}
	if token == "" {
		return nil, errors.New("nsclient: an API token is required")
	}
	c := &Client{
		apiURL: trimmed,
		token:  token,
		http:   &http.Client{Timeout: requestTimeout},
	}
	c.statusURL = c.apiURL
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// --- Reads -------------------------------------------------------------------

// Status returns the network server's own health: gateways it has heard from,
// transports, duty-cycle occupancy, integration dispatcher counters.
//
// It is unauthenticated and served on another port, so it goes to statusURL with no
// Authorization header. That is the network server's design — /status is read-only
// and carries no per-device data — not an oversight to work around.
func (c *Client) Status(ctx context.Context) (json.RawMessage, error) {
	return c.do(ctx, http.MethodGet, c.statusURL, statusRoute, nil)
}

// ListDevices returns the whole inventory. The network server does not page it, so
// a large fleet is a large response; maxResponseBytes is what makes that a refusal
// rather than an incident.
func (c *Client) ListDevices(ctx context.Context) (json.RawMessage, error) {
	return c.get(ctx, devicesRoute, nil)
}

// GetDevice returns one device as the network server sees it. Keys are absent from
// that response by construction, so there is nothing here to redact.
func (c *Client) GetDevice(ctx context.Context, devEUI string) (json.RawMessage, error) {
	return c.get(ctx, devicePath(devEUI), nil)
}

// Session returns what the network currently believes about a device's radio
// parameters.
func (c *Client) Session(ctx context.Context, devEUI string) (json.RawMessage, error) {
	return c.get(ctx, devicePath(devEUI)+"/session", nil)
}

// ListUplinks returns recent uplinks with their decoded readings. An empty devEUI
// means every device, which is the network server's convention, not this client's.
func (c *Client) ListUplinks(ctx context.Context, devEUI string, limit int) (json.RawMessage, error) {
	return c.get(ctx, uplinksRoute, listQuery(devEUI, "", limit))
}

// ListEvents returns the recorded trace, optionally filtered to one device and one
// kind. The kind is validated by the network server, which answers an unrecognised
// one with a 400 listing the valid values — a better answer than an empty list.
func (c *Client) ListEvents(ctx context.Context, devEUI, kind string, limit int) (json.RawMessage, error) {
	return c.get(ctx, eventsRoute, listQuery(devEUI, kind, limit))
}

// ListModels returns the device models, without their formatter scripts.
func (c *Client) ListModels(ctx context.Context) (json.RawMessage, error) {
	return c.get(ctx, modelsRoute, nil)
}

// GetModel returns one model, including its formatter script. The ns returns the
// script on read deliberately: it came from a vendor datasheet, and an operator who
// cannot see what a fleet runs cannot audit it.
func (c *Client) GetModel(ctx context.Context, modelID string) (json.RawMessage, error) {
	return c.get(ctx, modelsRoute+"/"+url.PathEscape(modelID), nil)
}

// ListIntegrations returns the configured event destinations.
func (c *Client) ListIntegrations(ctx context.Context) (json.RawMessage, error) {
	return c.get(ctx, integrationsRoute, nil)
}

// GetIntegration returns one destination.
func (c *Client) GetIntegration(ctx context.Context, id string) (json.RawMessage, error) {
	return c.get(ctx, integrationsRoute+"/"+url.PathEscape(id), nil)
}

// --- Plumbing ----------------------------------------------------------------

func devicePath(devEUI string) string {
	return devicesRoute + "/" + url.PathEscape(devEUI)
}

// listQuery omits empty parameters rather than sending them. The network server
// treats an empty dev_eui as "all devices", and sending ?kind= explicitly makes the
// request look like it is asking for something specific when it is not.
func listQuery(devEUI, kind string, limit int) url.Values {
	q := url.Values{}
	if devEUI != "" {
		q.Set("dev_eui", devEUI)
	}
	if kind != "" {
		q.Set("kind", kind)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	return q
}

func (c *Client) get(ctx context.Context, path string, q url.Values) (json.RawMessage, error) {
	return c.do(ctx, http.MethodGet, c.apiURL, path, q)
}

func (c *Client) do(ctx context.Context, method, base, path string, q url.Values) (json.RawMessage, error) {
	target := base + path
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(nil))
	if err != nil {
		return nil, fmt.Errorf("nsclient: build request: %w", err)
	}
	// The status endpoint is unauthenticated by design, so the token travels only
	// to the API. Sending it anywhere else would be a leak waiting for a redirect.
	if base == c.apiURL {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		// The cause here is a connection failure, so the URL is worth carrying:
		// "cannot reach the network server" is the one diagnosis an agent cannot
		// produce for itself, and the address says whether this points at the
		// wrong host.
		return nil, fmt.Errorf("nsclient: cannot reach the network server at %s: %w", base, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("nsclient: read response from %s: %w", target, err)
	}
	if len(raw) > maxResponseBytes {
		return nil, fmt.Errorf(
			"nsclient: the response from %s is larger than %d bytes; narrow it with a "+
				"dev_eui filter or a smaller limit (handing it back truncated would not be "+
				"valid JSON, and handing it back whole would flood the conversation)",
			target, maxResponseBytes)
	}

	if resp.StatusCode >= 300 {
		return nil, statusError(resp.StatusCode, raw)
	}
	return json.RawMessage(raw), nil
}

// errorDetail pulls the network server's {"error": "..."} sentence out, falling
// back to the raw body so a proxy's HTML error page is still reported rather than
// swallowed.
func errorDetail(raw []byte) string {
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err == nil && body.Error != "" {
		return body.Error
	}
	text := strings.TrimSpace(string(raw))
	if len(text) > 200 {
		text = text[:200] + "…"
	}
	if text == "" {
		return "no detail supplied"
	}
	return text
}
