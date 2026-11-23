package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// networkStatusIn has no fields. The health snapshot is a whole-network read, and
// there is nothing meaningful to filter it by.
type networkStatusIn struct{}

func addNetworkStatus(s *mcp.Server, d *Deps) string {
	const name = "network_status"
	mcp.AddTool(s, &mcp.Tool{
		Name:  name,
		Title: "Network status",
		Description: "The network server's own health snapshot: the band and NetID it is " +
			"running, process uptime (a rising uptime that resets is a restart), every " +
			"gateway it has heard from with per-gateway RX/TX counters and the last " +
			"transmission error the concentrator reported, which gateway transports are " +
			"listening on and their ports, the event-delivery counters, and duty-cycle " +
			"usage per sub-band with a denial count. Read this first when the question is " +
			"about the network rather than one device. A gateway counts as online because " +
			"it recently sent PULL_DATA: that is presence, not health.",
		Annotations: readOnly("Network status"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ networkStatusIn) (*mcp.CallToolResult, any, error) {
		raw, err := d.NS.Status(ctx)
		return d.read(name, "", raw, err)
	})
	return name
}
