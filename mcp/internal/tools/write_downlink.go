package tools

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fieldnode/lorawan-server/mcp/internal/nsclient"
)

// maxDownlinkBytes mirrors the network server's own cap. Refusing here rather than
// there means the caller gets one sentence naming the limit instead of an upstream 400
// to interpret, and does not pay a round trip to learn it.
const maxDownlinkBytes = 51

// hexPayload is the shape the network server expects for a downlink body: hex, not
// base64, because these payloads are copied out of datasheets and serial traces.
var hexPayload = regexp.MustCompile(`^[0-9A-Fa-f]+$`)

type enqueueDownlinkIn struct {
	DevEUI    string `json:"dev_eui" jsonschema:"the device's 16-hex-digit DevEUI"`
	Data      string `json:"data" jsonschema:"the application payload as a hex string, 1 to 51 bytes (2 to 102 hex characters), no separators"`
	FPort     int    `json:"f_port" jsonschema:"application port 1..223; 0 is reserved for MAC-only frames and is refused"`
	Confirmed bool   `json:"confirmed,omitempty" jsonschema:"ask the device to acknowledge the frame; a confirmed frame it never acknowledges is eventually discarded"`
	Reason    string `json:"reason" jsonschema:"why this frame is being sent. Shown to whoever approves the call and recorded in the audit log, so write the sentence you would want read"`
}

func addEnqueueDownlink(s *mcp.Server, d *Deps) string {
	const name = "enqueue_downlink"
	mcp.AddTool(s, &mcp.Tool{
		Name:  name,
		Title: "Queue a downlink",
		Description: "Queue an application frame for one device. It is NOT transmitted now: " +
			"the network server answers 202 because the frame waits in the device's single " +
			"pending slot and goes out in the next RX window, which for a class-A device that " +
			"reports hourly may be hours away. Do not retry after a 202 — the retry is refused " +
			"with a 409 saying a frame already waits, which is the correct outcome rather than " +
			"a failure. Class C is not implemented by this network server, so nothing can reach " +
			"a device outside its own windows. Payload is hex, at most 51 bytes; f_port must be " +
			"1..223. A device that has never joined has no session and will refuse this.",
		Annotations: writeAnnotations("Queue a downlink", false, false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in enqueueDownlinkIn) (*mcp.CallToolResult, any, error) {
		devEUI, err := cleanDevEUI(in.DevEUI)
		if err != nil {
			return nil, nil, err
		}
		reason, err := requireReason(in.Reason)
		if err != nil {
			return nil, nil, err
		}
		payload, err := cleanPayload(in.Data)
		if err != nil {
			return nil, nil, err
		}
		if in.FPort < 1 || in.FPort > 223 {
			return nil, nil, fmt.Errorf("f_port must be 1..223, got %d; 0 is reserved for "+
				"network MAC commands and cannot be written by hand", in.FPort)
		}
		req := nsclient.DownlinkRequest{Data: payload, FPort: in.FPort, Confirmed: in.Confirmed}
		return d.write(ctx, name, in, devEUI, reason, func() (int, json.RawMessage, error) {
			return d.NS.EnqueueDownlink(ctx, devEUI, req)
		})
	})
	return name
}

// cleanPayload normalises and validates a hex payload, returning the canonical
// upper-case form.
func cleanPayload(raw string) (string, error) {
	cleaned := strings.Join(strings.Fields(raw), "")
	cleaned = strings.TrimPrefix(cleaned, "0x")
	if cleaned == "" {
		return "", fmt.Errorf("data is required: a hex payload such as \"0102\". An empty " +
			"frame is not a thing this network transmits")
	}
	if len(cleaned)%2 != 0 {
		return "", fmt.Errorf("data %q is an odd number of hex characters; a byte is two", raw)
	}
	if !hexPayload.MatchString(cleaned) {
		return "", fmt.Errorf("data %q is not a hex string; write bare hex with no spaces, "+
			"commas or 0x prefix", raw)
	}
	decoded, err := hex.DecodeString(cleaned)
	if err != nil {
		return "", fmt.Errorf("data %q is not valid hex: %v", raw, err)
	}
	if len(decoded) > maxDownlinkBytes {
		return "", fmt.Errorf("data is %d bytes; the network server's limit is %d, and a "+
			"larger frame cannot be carried by any RX window at a legal data rate",
			len(decoded), maxDownlinkBytes)
	}
	return strings.ToUpper(cleaned), nil
}
