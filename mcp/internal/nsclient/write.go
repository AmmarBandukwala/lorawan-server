package nsclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// Write routes appended to the device path. Same rule as the read routes: named once
// and pinned by a test against the path a request actually carries, because the two
// modules share no package and a wrong route is a 404, not a compile error.
const (
	downlinkRoute  = "/downlink"
	devStatusRoute = "/devstatus"
)

// DownlinkRequest is the body of POST /api/v1/devices/{dev_eui}/downlink.
//
// Data is hex, not base64, because these payloads get copied out of datasheets and
// serial traces, which are written in hex. The network server says so and enforces it.
type DownlinkRequest struct {
	Data      string `json:"data"`
	FPort     int    `json:"f_port"`
	Confirmed bool   `json:"confirmed,omitempty"`
}

// DevicePatch is the body of PATCH /api/v1/devices/{dev_eui}.
//
// Every field is a pointer, and that is the whole point: the network server applies
// only the fields present in the body, so a value here means "change this" and a nil
// means "leave it alone". A plain bool would send enabled:false on every call that
// meant to rename a device, silently switching it off.
type DevicePatch struct {
	Name           *string
	Group          *string
	ModelID        *string
	Enabled        *bool
	DisableADR     *bool
	SupportsClassC *bool
	MaxDataRate    *int
	MinDataRate    *int
	MaxTxPower     *int
	Attributes     *map[string]string

	// LocationJSON is kept raw because the network server tells three cases apart and
	// a typed field would collapse the first two: `location` absent means leave the
	// position alone, `location: null` means clear it, an object means set it.
	LocationJSON json.RawMessage
}

// body assembles the PATCH document, omitting every field that was not supplied.
func (p DevicePatch) body() ([]byte, error) {
	out := map[string]any{}
	if p.Name != nil {
		out["name"] = *p.Name
	}
	if p.Group != nil {
		out["group"] = *p.Group
	}
	if p.ModelID != nil {
		out["model_id"] = *p.ModelID
	}
	if p.Enabled != nil {
		out["enabled"] = *p.Enabled
	}
	if p.DisableADR != nil {
		out["disable_adr"] = *p.DisableADR
	}
	if p.SupportsClassC != nil {
		out["supports_class_c"] = *p.SupportsClassC
	}
	if p.MaxDataRate != nil {
		out["max_data_rate"] = *p.MaxDataRate
	}
	if p.MinDataRate != nil {
		out["min_data_rate"] = *p.MinDataRate
	}
	if p.MaxTxPower != nil {
		out["max_tx_power"] = *p.MaxTxPower
	}
	if p.Attributes != nil {
		// Replaces the whole map server-side, so an empty map means "no attributes" —
		// a different operation from omitting the field, and the reason this is a
		// pointer to a map rather than a map.
		out["attributes"] = *p.Attributes
	}
	if len(p.LocationJSON) > 0 {
		out["location"] = p.LocationJSON
	}
	return json.Marshal(out)
}

// Empty reports whether the patch would change nothing. An empty body is harmless to
// the network server and useless to the caller, so the tool refuses it rather than
// reporting success for an operation that did nothing.
func (p DevicePatch) Empty() bool {
	return p.Name == nil && p.Group == nil && p.ModelID == nil && p.Enabled == nil &&
		p.DisableADR == nil && p.SupportsClassC == nil && p.MaxDataRate == nil &&
		p.MinDataRate == nil && p.MaxTxPower == nil && p.Attributes == nil &&
		len(p.LocationJSON) == 0
}

// --- Writes ------------------------------------------------------------------

// EnqueueDownlink queues a frame for the device's next RX window.
//
// It returns the upstream status so the caller can tell 202 from 200: the network
// server answers 202 deliberately, because the frame is accepted for a future window
// rather than transmitted, and an agent that reads 202 as failure will retry into a
// 409.
func (c *Client) EnqueueDownlink(ctx context.Context, devEUI string, in DownlinkRequest) (int, json.RawMessage, error) {
	return c.send(ctx, http.MethodPost, devicePath(devEUI)+downlinkRoute, nil, in)
}

// CancelPendingDownlink drops the frame waiting for the device's next window.
func (c *Client) CancelPendingDownlink(ctx context.Context, devEUI string) (int, json.RawMessage, error) {
	return c.send(ctx, http.MethodDelete, devicePath(devEUI)+downlinkRoute, nil, nil)
}

// RequestDeviceStatus queues a DevStatusReq. It occupies the device's single pending
// slot, so it conflicts with a queued application frame exactly as enqueueing one does
// — which surprises anyone who read "ask for a battery level" as a read.
func (c *Client) RequestDeviceStatus(ctx context.Context, devEUI string) (int, json.RawMessage, error) {
	return c.send(ctx, http.MethodPost, devicePath(devEUI)+devStatusRoute, nil, nil)
}

// PatchDevice applies a partial update and returns the network server's own view of
// the device afterwards, which is how the caller learns what actually changed rather
// than assuming the arguments were applied as sent.
func (c *Client) PatchDevice(ctx context.Context, devEUI string, p DevicePatch) (int, json.RawMessage, error) {
	body, err := p.body()
	if err != nil {
		return 0, nil, fmt.Errorf("nsclient: encode the device patch: %w", err)
	}
	return c.send(ctx, http.MethodPatch, devicePath(devEUI), nil, json.RawMessage(body))
}
