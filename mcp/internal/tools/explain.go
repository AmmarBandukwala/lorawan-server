package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fieldnode/lorawan-server/mcp/internal/vocabulary"
)

type explainIn struct {
	Reason string `json:"reason" jsonschema:"the machine-readable reason recorded on an event, e.g. f_cnt_gap_too_large"`
}

func addExplainEventReason(s *mcp.Server, d *Deps) string {
	const name = "explain_event_reason"
	mcp.AddTool(s, &mcp.Tool{
		Name:  name,
		Title: "Explain an event reason",
		Description: "What a recorded event reason means and what usually causes it, from the " +
			"same documentation the network server's own vocabulary file carries. Nothing " +
			"here is a guess: an unrecognised reason is reported as unrecognised, with the " +
			"full list, rather than answered with something plausible. Call this before " +
			"repeating a reason to an operator in your own words — these strings have precise " +
			"meanings, and several of them (duplicate_frame, adr_keepalive, duty_cycle_denied) " +
			"describe things that are working correctly rather than failing.",
		Annotations: readOnly("Explain an event reason"),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in explainIn) (*mcp.CallToolResult, any, error) {
		reason := strings.ToLower(strings.TrimSpace(in.Reason))
		if reason == "" {
			return nil, nil, fmt.Errorf("reason is required; it is the reason field on an " +
				"event from list_events")
		}
		entry, ok := vocabulary.Lookup(reason)
		if !ok {
			return nil, nil, fmt.Errorf("%q is not a reason this network server records. "+
				"Known reasons: %s", reason, strings.Join(vocabulary.Reasons(), ", "))
		}
		// The cause is labelled as a likelihood rather than a conclusion, because that
		// is what it is. The event's own detail field is the evidence, and an agent
		// that reads this as a diagnosis has skipped the part where it looks.
		return text(fmt.Sprintf(
			"reason: %s\nmeans: %s\nusually caused by: %s\n\n"+
				"This is a likelihood, not a conclusion — the event's own detail field is "+
				"the evidence.\nstart reading at: %s",
			reason, entry.Summary, entry.Cause, entry.Where)), nil, nil
	})
	return name
}
