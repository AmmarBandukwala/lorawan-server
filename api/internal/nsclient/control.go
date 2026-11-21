package nsclient

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
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

	// Attributes replaces the device's whole key/value map. A pointer so that
	// "leave alone" and "there are none now" stay different: an empty map is a
	// deliberate clearing, and a nil is silence.
	Attributes *map[string]string `json:"attributes,omitempty"`

	// Location is raw JSON for the same reason, with one complication: clearing a
	// position means sending an explicit null, and a typed field cannot tell a
	// null from an absent key. Marshalling json.RawMessage verbatim keeps the
	// three cases distinct all the way to the network server, which is where the
	// distinction is acted on.
	Location json.RawMessage `json:"location,omitempty"`

	// ModelID re-associates the device with a device model, which is what decides
	// how its uplinks are decoded. A pointer to string, so a nil means "leave
	// alone" and a pointer at "" means "clear it" — omitempty tests the pointer,
	// not the string, so clearing survives the trip.
	ModelID *string `json:"model_id,omitempty"`
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

// Device models: what a kind of sensor is and how its bytes are decoded.
//
// These are relayed rather than re-implemented. A model carries a JavaScript
// payload formatter, and the network server is the component that compiles it —
// so it is the only place that can say whether a script is acceptable, and it
// says so with the parser's own message. Re-validating here would either disagree
// with that verdict or duplicate a JavaScript parser, and both are worse than a
// relay.
//
// Bodies and responses are raw JSON for the same reason: the console's request is
// already the network server's request, and re-encoding it would lose exactly the
// detail that makes a rejection actionable.

// ListModels returns every model without its formatter script.
func (c *Client) ListModels(ctx context.Context) (json.RawMessage, error) {
	return c.do(ctx, http.MethodGet, nsModelRoute, nil)
}

// Model returns one model including its script.
func (c *Client) Model(ctx context.Context, modelID string) (json.RawMessage, error) {
	return c.do(ctx, http.MethodGet, modelPath(modelID), nil)
}

// CreateModel installs a new model. A slug that exists is a 409 from the network
// server, which is relayed rather than turned into an upsert: an operator who
// means to replace a formatter should do it deliberately.
func (c *Client) CreateModel(ctx context.Context, modelID string, body json.RawMessage) (json.RawMessage, error) {
	return c.do(ctx, http.MethodPost, modelPath(modelID), body)
}

// UpdateModel replaces a model, creating it when the slug is new.
func (c *Client) UpdateModel(ctx context.Context, modelID string, body json.RawMessage) (json.RawMessage, error) {
	return c.do(ctx, http.MethodPut, modelPath(modelID), body)
}

// DeleteModel removes a model. Devices that referenced it survive and simply stop
// being decoded, which is why the response counts them.
func (c *Client) DeleteModel(ctx context.Context, modelID string) (json.RawMessage, error) {
	return c.do(ctx, http.MethodDelete, modelPath(modelID), nil)
}

// TestModelDecode runs a formatter against bytes without storing anything. The
// answer is a verdict rather than a transport result, so a failing script still
// arrives as a 200 with ok:false and must be relayed untouched.
func (c *Client) TestModelDecode(ctx context.Context, modelID string, body json.RawMessage) (json.RawMessage, error) {
	return c.do(ctx, http.MethodPost, modelPath(modelID)+"/decode", body)
}

// Uplinks returns recent stored uplinks, decoded where the device's model has a
// formatter. devEUI may be empty for the whole network.
func (c *Client) Uplinks(ctx context.Context, devEUI string, limit int) (json.RawMessage, error) {
	query := url.Values{"limit": {strconv.Itoa(limit)}}
	if devEUI != "" {
		query.Set("dev_eui", devEUI)
	}
	return c.do(ctx, http.MethodGet, nsUplinkRoute+"?"+query.Encode(), nil)
}

// modelPath builds a model route. The slug is validated by the caller before it
// gets here, so no escaping is applied: a slug containing a slash would be a bug
// in this service, and escaping it would hide that bug behind a 404 from the
// network server.
func modelPath(modelID string) string {
	return nsModelRoute + "/" + modelID
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
