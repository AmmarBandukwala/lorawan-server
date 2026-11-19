package nsclient

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
)

// Control-plane calls: queue a downlink, ask for a battery report, read a
// session, read the network's health.
//
// These live in their own file because they differ from provisioning in the way
// that matters: they return the network server's own view rather than a shape
// this package invents. The console renders what the network server says, so
// decoding into a local struct that happens to match would let the two drift
// apart without anything failing. json.RawMessage keeps the console a faithful
// mirror and makes a field rename on the ns visible in the UI rather than
// silently blanking a column.

// EnqueueDownlink queues an application frame for a device.
//
// The response is an acknowledgement, not a result: a class-A device listens for
// two seconds after it speaks, so the frame leaves in the next window the device
// opens and the caller cannot learn the outcome from this call.
func (c *Client) EnqueueDownlink(ctx context.Context, devEUI string, fPort int, dataHex string, confirmed bool) (json.RawMessage, error) {
	body := map[string]any{
		"data":      dataHex,
		"confirmed": confirmed,
	}
	// fPort is omitted rather than sent as 0 when unset: 0 means "MAC only" to
	// the network server, which is a different request, not a default.
	if fPort > 0 {
		body["f_port"] = fPort
	}
	return c.do(ctx, http.MethodPost, devicePath(devEUI)+"/downlink", body)
}

// CancelDownlink drops the frame waiting for the device's next window.
func (c *Client) CancelDownlink(ctx context.Context, devEUI string) (json.RawMessage, error) {
	return c.do(ctx, http.MethodDelete, devicePath(devEUI)+"/downlink", nil)
}

// RequestDeviceStatus schedules a DevStatusReq, which is how the dashboard gets
// a battery level: the device reports one only when asked.
func (c *Client) RequestDeviceStatus(ctx context.Context, devEUI string) (json.RawMessage, error) {
	return c.do(ctx, http.MethodPost, devicePath(devEUI)+"/devstatus", map[string]any{})
}

// Session returns the network's current belief about a device's radio
// parameters — data rate, counters, ADR state, what is queued.
func (c *Client) Session(ctx context.Context, devEUI string) (json.RawMessage, error) {
	return c.do(ctx, http.MethodGet, devicePath(devEUI)+"/session", nil)
}

// Status returns the network server's own health: gateways it has heard from,
// duty-cycle occupancy and the integration dispatcher's counters.
//
// It is fetched through the network server rather than read from ns_gateways
// because two of the three are process state, not table state: a gateway's
// "online" verdict depends on how recently it sent PULL_DATA, and the drop
// counters live in memory.
//
// It reads from statusURL because that endpoint is served separately from the
// token-protected API, usually on its own port.
func (c *Client) Status(ctx context.Context) (json.RawMessage, error) {
	return c.doAt(ctx, c.statusURL, http.MethodGet, "/status", nil)
}

// DevicePatch carries the fields an operator can change after provisioning.
//
// Pointers throughout, so "leave alone" and "set to false" are different requests.
// A plain bool would make a request that only renames a device silently re-enable
// ADR on it, which is the kind of change that shows up as a mysterious radio
// problem a week later.
//
// Credentials are absent by construction. The network server never returns them,
// so a console could not supply them on a replace even if it wanted to; this is
// the endpoint that lets an operator edit a device they cannot read back.
type DevicePatch struct {
	Name           *string `json:"name,omitempty"`
	Group          *string `json:"group,omitempty"`
	Enabled        *bool   `json:"enabled,omitempty"`
	DisableADR     *bool   `json:"disable_adr,omitempty"`
	SupportsClassC *bool   `json:"supports_class_c,omitempty"`
	MaxDataRate    *int    `json:"max_data_rate,omitempty"`
	MinDataRate    *int    `json:"min_data_rate,omitempty"`
	MaxTxPower     *int    `json:"max_tx_power,omitempty"`
}

// PatchDevice changes a device's operator-facing settings without touching its
// credentials, which stay inside the network server.
func (c *Client) PatchDevice(ctx context.Context, devEUI string, p DevicePatch) (json.RawMessage, error) {
	return c.do(ctx, http.MethodPatch, devicePath(devEUI), p)
}

// DeleteDevice revokes a device. The network server removes its session with it,
// which is what makes the device stop talking rather than merely disappear from
// the console.
func (c *Client) DeleteDevice(ctx context.Context, devEUI string) (json.RawMessage, error) {
	return c.do(ctx, http.MethodDelete, devicePath(devEUI), nil)
}

// Device reads one device's settings.
//
// This is what lets the console show the truth in an edit form. Without a read,
// a form would have to assume defaults, and saving it would push those assumed
// values over settings the operator never looked at.
func (c *Client) Device(ctx context.Context, devEUI string) (json.RawMessage, error) {
	return c.do(ctx, http.MethodGet, devicePath(devEUI), nil)
}

// do performs a request against the API base URL and returns the body verbatim.
func (c *Client) do(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	return c.doAt(ctx, c.baseURL, method, path, body)
}

// doAt is do against an explicit base, which is what lets the health read point
// at the status port without giving every call site a base parameter.
func (c *Client) doAt(ctx context.Context, base, method, path string, body any) (json.RawMessage, error) {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, base+path, jsonReader(payload))
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &TransportError{URL: base + path, Err: err}
	}
	defer resp.Body.Close()

	raw, err := readCapped(resp)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusConflict {
			return nil, &ConflictError{Detail: errorDetail(raw)}
		}
		return nil, &APIError{Status: resp.StatusCode, Detail: errorDetail(raw)}
	}
	return raw, nil
}

// TransportError means the network server could not be reached at all, which the
// console reports differently from a rejection: one is "the ns is down", the
// other is "the ns said no".
type TransportError struct {
	URL string
	Err error
}

func (e *TransportError) Error() string {
	return "nsclient: reach the network server at " + e.URL + ": " + e.Err.Error()
}

func (e *TransportError) Unwrap() error { return e.Err }

// jsonReader returns a body reader for a marshalled payload, or a nil interface
// so the request is sent without one. Returning a typed nil *bytes.Reader would
// produce a non-nil io.Reader holding nil, which http.NewRequest treats as "there
// is a body" and then trips over.
func jsonReader(payload []byte) io.Reader {
	if payload == nil {
		return nil
	}
	return bytes.NewReader(payload)
}

// maxResponseBytes bounds a response body. The network server's replies are
// small objects; a URL misconfigured to point at something chatty (a public
// API, an error page from a proxy) must not be able to exhaust this process.
const maxResponseBytes = 256 << 10

func readCapped(resp *http.Response) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, &TransportError{URL: resp.Request.URL.String(), Err: err}
	}
	return raw, nil
}
