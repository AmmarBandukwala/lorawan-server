// Package nsclient talks to the Fieldnode network server's application API.
//
// Provisioning goes over HTTP rather than writing ns_devices directly because
// the network server owns the rules about what a valid device is: which
// credentials are required together, that a DevAddr must not already be taken,
// that replacing a device must not reset its join-nonce state. Reimplementing
// those checks here would give the dashboard a second, subtly different opinion
// about what is legal — and the network server would still be the one that got
// it wrong at runtime.
package nsclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Device is what this client sends to create or replace a device. Credentials
// are write-only: the network server never returns them, and neither does this
// client keep them.
type Device struct {
	DevEUI  string `json:"dev_eui"`
	JoinEUI string `json:"join_eui"`
	Name    string `json:"name"`
	Group   string `json:"group,omitempty"`

	// OTAA. Provide AppKey for an OTAA device.
	AppKey string `json:"app_key,omitempty"`

	// ABP. All three are required together.
	DevAddr string `json:"dev_addr,omitempty"`
	NwkSKey string `json:"nwk_s_key,omitempty"`
	AppSKey string `json:"app_s_key,omitempty"`

	Enabled        *bool `json:"enabled,omitempty"`
	DisableADR     bool  `json:"disable_adr,omitempty"`
	SupportsClassC bool  `json:"supports_class_c,omitempty"`
}

// CreatedDevice is the subset of the network server's response this client
// needs. Keys are absent from that response by construction, so there is no
// field here that could accidentally carry one.
type CreatedDevice struct {
	DevEUI     string `json:"dev_eui"`
	JoinEUI    string `json:"join_eui"`
	Name       string `json:"name"`
	Group      string `json:"group"`
	Activation string `json:"activation"`
	Enabled    bool   `json:"enabled"`
}

// ConflictError means the DevEUI is already provisioned. It is a distinct type
// so the caller can answer 409 without string matching.
type ConflictError struct{ Detail string }

func (e *ConflictError) Error() string {
	if e.Detail == "" {
		return "device already provisioned"
	}
	return e.Detail
}

// APIError is any other rejection from the network server, carrying its message
// so an operator sees the reason rather than "request failed".
type APIError struct {
	Status int
	Detail string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("network server rejected the request (%d): %s", e.Status, e.Detail)
}

// Client is a network-server API client.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New builds a client. baseURL must be an absolute http(s) URL; anything else is
// rejected here rather than failing on the first request, when the cause is
// harder to see.
func New(baseURL, token string) (*Client, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("nsclient: invalid base URL: %w", err)
	}
	if parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("nsclient: base URL must be an absolute http(s) URL, got %q", baseURL)
	}
	if token == "" {
		return nil, fmt.Errorf("nsclient: an API token is required")
	}
	return &Client{
		baseURL: trimmed,
		token:   token,
		// The network server may be starting alongside this process, so a short
		// timeout beats waiting for a container that is not answering.
		http: &http.Client{Timeout: 10 * time.Second},
	}, nil
}

// CreateDevice provisions a device.
func (c *Client) CreateDevice(ctx context.Context, d Device) (CreatedDevice, error) {
	return c.send(ctx, http.MethodPost, "/api/v1/devices", d)
}

// PutDevice replaces an existing device.
func (c *Client) PutDevice(ctx context.Context, d Device) (CreatedDevice, error) {
	path := "/api/v1/devices/" + url.PathEscape(d.DevEUI)
	return c.send(ctx, http.MethodPut, path, d)
}

func (c *Client) send(ctx context.Context, method, path string, body Device) (CreatedDevice, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return CreatedDevice{}, fmt.Errorf("nsclient: encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return CreatedDevice{}, fmt.Errorf("nsclient: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return CreatedDevice{}, fmt.Errorf("nsclient: reach the network server at %s: %w", c.baseURL, err)
	}
	defer resp.Body.Close()

	// The network server's bodies are small JSON objects; reading at most 8 KiB
	// keeps a misconfigured URL that points at something chatty from exhausting
	// memory.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if err != nil {
		return CreatedDevice{}, fmt.Errorf("nsclient: read response: %w", err)
	}

	if resp.StatusCode >= 300 {
		detail := errorDetail(raw)
		if resp.StatusCode == http.StatusConflict {
			return CreatedDevice{}, &ConflictError{Detail: detail}
		}
		return CreatedDevice{}, &APIError{Status: resp.StatusCode, Detail: detail}
	}

	var out CreatedDevice
	if err := json.Unmarshal(raw, &out); err != nil {
		return CreatedDevice{}, fmt.Errorf("nsclient: decode response: %w", err)
	}
	return out, nil
}

// errorDetail pulls the network server's {"error": "..."} message out, falling
// back to the raw body so a proxy's HTML error page is still reported.
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
