package tools

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fieldnode/lorawan-server/mcp/internal/prompts"
)

// snakeCaseTokens finds the identifiers prose refers to. Tool names, JSON fields,
// event kinds and reason strings all look alike in text, and this test decides which.
var snakeCaseTokens = regexp.MustCompile(`[a-z][a-z0-9]*(?:_[a-z0-9]+)+`)

// proseFieldNames are the non-tool identifiers the runbooks legitimately name: fields
// to read, kinds to filter on, reasons to expect.
//
// They are listed rather than derived, because the point is to notice when prose names
// something that no longer exists. If a tool is renamed, its old name would land here
// by accident and hide the break, so two other assertions guard it: an entry may not
// also be an advertised tool, and an entry may not sit unused.
var proseFieldNames = map[string]string{
	"dev_eui":           "the argument every runbook interpolates",
	"has_codec":         "uplink field: whether a formatter is configured",
	"decode_error":      "uplink field: why the formatter refused",
	"disable_adr":       "device field the patch tool writes",
	"duty_cycle":        "status field",
	"gateways_online":   "status field",
	"gateways_total":    "status field",
	"uptime_seconds":    "status field",
	"last_tx_ack_error": "gateway field: the concentrator's last transmission error",
	"pending_downlink":  "session field: the frame holding the device's one slot",

	"join_request":                  "event kind",
	"join_accept":                   "event kind",
	"join_reject":                   "event kind",
	"duty_cycle_denied":             "event reason",
	"downlink_send_failed":          "event reason",
	"downlink_too_large_for_window": "event reason",
	"no_rx_window_available":        "event reason",
	"uplink_mic_failed":             "event reason",
	"unknown_dev_addr":              "event reason",
	"f_cnt_replay":                  "event reason",
	"f_cnt_gap_too_large":           "event reason",
}

// TestEveryIdentifierInARunbookIsReal keeps the runbooks honest about the surface.
//
// A prompt that tells an agent to call a tool that does not exist is worse than no
// prompt: the agent spends a turn on a refusal, and if the name is close to a real
// one it may call that instead, which on this network means transmitting. The same
// applies to a field name that was renamed — the agent looks for a value that is not
// there and reports its absence as a fact about the device.
func TestEveryIdentifierInARunbookIsReal(t *testing.T) {
	cs := newOperateServer(t, healthyFake(t))
	advertised := advertisedNames(t, cs)

	for _, name := range advertised {
		if _, isField := proseFieldNames[name]; isField {
			t.Errorf("%s is both an advertised tool and listed as a prose field; the "+
				"allowlist may not absorb a tool, renamed or not", name)
		}
	}

	seen := map[string]bool{}
	for _, tc := range []struct {
		prompt string
		args   map[string]string
	}{
		{prompts.DiagnoseDevice, map[string]string{"dev_eui": testEUI}},
		{prompts.WhyNoDownlink, map[string]string{"dev_eui": testEUI}},
		{prompts.FleetHealth, nil},
	} {
		got, err := cs.GetPrompt(context.Background(),
			&mcp.GetPromptParams{Name: tc.prompt, Arguments: tc.args})
		if err != nil {
			t.Fatalf("GetPrompt(%s): %v", tc.prompt, err)
		}
		var text strings.Builder
		for _, msg := range got.Messages {
			if content, ok := msg.Content.(*mcp.TextContent); ok {
				text.WriteString(content.Text)
			}
		}

		for _, token := range snakeCaseTokens.FindAllString(text.String(), -1) {
			if slices.Contains(advertised, token) {
				continue
			}
			if _, ok := proseFieldNames[token]; ok {
				seen[token] = true
				continue
			}
			t.Errorf("%s names %q, which is neither an advertised tool nor a listed "+
				"field, kind or reason. Either the runbook is stale or the allowlist is "+
				"incomplete.", tc.prompt, token)
		}
	}

	for name := range proseFieldNames {
		if !seen[name] {
			t.Errorf("%s is listed as a prose field name but no runbook uses it; drop the "+
				"entry so the allowlist cannot quietly hide a renamed tool", name)
		}
	}
}
