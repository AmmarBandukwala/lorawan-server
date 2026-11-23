package tools

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// devEUIReasonIn is the shape shared by the two writes that take nothing but a device
// and a stated purpose.
type devEUIReasonIn struct {
	DevEUI string `json:"dev_eui" jsonschema:"the device's 16-hex-digit DevEUI"`
	Reason string `json:"reason" jsonschema:"why this is being done; shown to whoever approves the call and recorded in the audit log"`
}

func addCancelDownlink(s *mcp.Server, d *Deps) string {
	const name = "cancel_downlink"
	mcp.AddTool(s, &mcp.Tool{
		Name:  name,
		Title: "Cancel a pending downlink",
		Description: "Discard the frame waiting in one device's pending slot. This is " +
			"destructive in the way that matters: the application wanted that frame sent, and " +
			"after this call it will not be, with nothing queued behind it. The network server " +
			"keeps one pending frame per device, so cancelling is also the only way to replace " +
			"a queued frame — queueing a second is refused rather than appended. It does not " +
			"touch a frame already transmitted, and it is safe to call when nothing is waiting.",
		Annotations: writeAnnotations("Cancel a pending downlink", true, true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in devEUIReasonIn) (*mcp.CallToolResult, any, error) {
		devEUI, err := cleanDevEUI(in.DevEUI)
		if err != nil {
			return nil, nil, err
		}
		reason, err := requireReason(in.Reason)
		if err != nil {
			return nil, nil, err
		}
		return d.write(ctx, name, in, devEUI, reason, func() (int, json.RawMessage, error) {
			return d.NS.CancelPendingDownlink(ctx, devEUI)
		})
	})
	return name
}

func addRequestDeviceStatus(s *mcp.Server, d *Deps) string {
	const name = "request_device_status"
	mcp.AddTool(s, &mcp.Tool{
		Name:  name,
		Title: "Request a device battery report",
		Description: "Queue a DevStatusReq so a device reports its battery level and link " +
			"margin in its next window. Despite reading like a query this is a write: it " +
			"occupies the device's single pending slot, so it is refused with a 409 when an " +
			"application frame is already queued, and it delays that frame if you cancel first. " +
			"Nothing arrives immediately — the answer comes back with the device's next uplink, " +
			"which may be hours away, and then appears as battery and margin_db on " +
			"get_device_session. A battery of 255 means the device cannot report a level.",
		Annotations: writeAnnotations("Request a device battery report", false, false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in devEUIReasonIn) (*mcp.CallToolResult, any, error) {
		devEUI, err := cleanDevEUI(in.DevEUI)
		if err != nil {
			return nil, nil, err
		}
		reason, err := requireReason(in.Reason)
		if err != nil {
			return nil, nil, err
		}
		return d.write(ctx, name, in, devEUI, reason, func() (int, json.RawMessage, error) {
			return d.NS.RequestDeviceStatus(ctx, devEUI)
		})
	})
	return name
}
