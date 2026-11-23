package tools

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// eventKinds is every kind the network server records, restated here so the tool
// description can name them: an agent that does not know the vocabulary cannot
// filter on it, and guessing produces a 400 it then has to parse.
//
// It is a restatement, so it can drift. The test in traffic_test.go reads
// ns/internal/engine/events.go as text and fails if the two disagree, which is the
// same guard the vocabulary package uses — the compiler cannot see across a module
// boundary, and neither can a typo in a description.
var eventKinds = []string{
	"join_request", "join_accept", "join_reject",
	"uplink", "downlink", "uplink_drop", "mac", "error",
}

type listUplinksIn struct {
	DevEUI string `json:"dev_eui,omitempty" jsonschema:"restrict to one device by its 16-hex-digit DevEUI; omit for every device"`
	Limit  int    `json:"limit,omitempty" jsonschema:"maximum number of uplinks to return, 1 to 500; omit for the network server's default of 50"`
}

type listEventsIn struct {
	DevEUI string `json:"dev_eui,omitempty" jsonschema:"restrict to one device by its 16-hex-digit DevEUI; omit for every device, which is the only way to see frames the network could not attribute to any session"`
	Kind   string `json:"kind,omitempty" jsonschema:"restrict to one event kind"`
	Limit  int    `json:"limit,omitempty" jsonschema:"maximum number of events to return, 1 to 500; omit for the network server's default of 50"`
}

func addListUplinks(s *mcp.Server, d *Deps) string {
	const name = "list_uplinks"
	mcp.AddTool(s, &mcp.Tool{
		Name:  name,
		Title: "List uplinks",
		Description: "Recent uplinks with the payload formatter's reading of them. Each row " +
			"carries received_at, f_port, the frame as base64 and the raw PHY payload as hex, " +
			"the gateway that heard it, frequency, data rate, RSSI and SNR, whether it was a " +
			"retransmission, and either decoded readings or decode_error. has_codec is what " +
			"distinguishes 'this device has no formatter' from 'the formatter rejected this " +
			"frame' — a formatter that has quietly stopped working looks exactly like a fleet " +
			"with no formatters, so do not read an absent decoded field as no data.",
		Annotations: readOnly("List uplinks"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listUplinksIn) (*mcp.CallToolResult, any, error) {
		devEUI, err := cleanOptionalDevEUI(in.DevEUI)
		if err != nil {
			return nil, nil, err
		}
		limit, err := cleanLimit(in.Limit)
		if err != nil {
			return nil, nil, err
		}
		raw, err := d.NS.ListUplinks(ctx, devEUI, limit)
		return d.read(name, devEUI, raw, err)
	})
	return name
}

func addListEvents(s *mcp.Server, d *Deps) string {
	const name = "list_events"
	mcp.AddTool(s, &mcp.Tool{
		Name:  name,
		Title: "List events",
		Description: "The recorded trace: joins, uplinks, downlinks, MAC exchanges, and every " +
			"frame the network refused together with the reason it refused. This is the only " +
			"place that explains how a device reached its current state, because a session " +
			"read shows only where it ended up. kind must be one of " +
			strings.Join(eventKinds, ", ") +
			"; anything else is refused with that list. A frame the server could not " +
			"attribute to a session appears only when dev_eui is omitted, and that absence is " +
			"the finding. Always pair a reason you do not recognise with " +
			"explain_event_reason rather than guessing at it.",
		Annotations: readOnly("List events"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listEventsIn) (*mcp.CallToolResult, any, error) {
		devEUI, err := cleanOptionalDevEUI(in.DevEUI)
		if err != nil {
			return nil, nil, err
		}
		limit, err := cleanLimit(in.Limit)
		if err != nil {
			return nil, nil, err
		}
		// The kind is deliberately not validated here. The network server owns the
		// vocabulary and its rejection lists every valid value, which is a better
		// answer than one this server could assemble from a copy that might be stale.
		raw, err := d.NS.ListEvents(ctx, devEUI, strings.ToLower(strings.TrimSpace(in.Kind)), limit)
		return d.read(name, devEUI, raw, err)
	})
	return name
}
