package nsclient

import (
	"context"
	"encoding/json"
	"net/http"
)

// Integration destinations are a relay, for the same reason device models are:
// the network server owns the connection, the credential and the delivery
// counters, so it is the only component that can say whether a destination
// works. This client therefore forwards requests and returns answers verbatim.
//
// One thing does get decided here, and it is worth stating because it is the
// reason the methods are not a single generic proxy: the credential. It is
// accepted on write and never returned by the network server, so nothing in this
// path may cache, log or re-emit a response body on the assumption that it is
// inert configuration.

// ListIntegrations returns every destination, without credentials, plus the live
// connection state the network server reports.
func (c *Client) ListIntegrations(ctx context.Context) (json.RawMessage, error) {
	return c.do(ctx, http.MethodGet, nsIntegrationRoute, nil)
}

// CreateIntegration adds a destination. The id travels in the body, matching the
// network server's shape, so a new field it learns to accept needs no change
// here.
func (c *Client) CreateIntegration(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	return c.do(ctx, http.MethodPost, nsIntegrationRoute, body)
}

// Integration returns one destination.
func (c *Client) Integration(ctx context.Context, id string) (json.RawMessage, error) {
	return c.do(ctx, http.MethodGet, integrationPath(id), nil)
}

// UpdateIntegration replaces a destination's settings.
func (c *Client) UpdateIntegration(ctx context.Context, id string, body json.RawMessage) (json.RawMessage, error) {
	return c.do(ctx, http.MethodPut, integrationPath(id), body)
}

// DeleteIntegration removes a destination and stops its delivery immediately.
func (c *Client) DeleteIntegration(ctx context.Context, id string) (json.RawMessage, error) {
	return c.do(ctx, http.MethodDelete, integrationPath(id), nil)
}

// TestIntegration sends one probe event to a stored destination. A failing probe
// is a 200 with ok:false at the network server — the request succeeded, the
// destination did not — so it must be relayed rather than turned into an error.
//
// The caller gives this call a longer context than the usual control timeout:
// the probe dials a broker or an HTTPS endpoint across the internet, and
// reporting a distant but working broker as broken is worse than waiting.
func (c *Client) TestIntegration(ctx context.Context, id string) (json.RawMessage, error) {
	return c.do(ctx, http.MethodPost, integrationPath(id)+"/test", nil)
}

func integrationPath(id string) string {
	return nsIntegrationRoute + "/" + id
}
