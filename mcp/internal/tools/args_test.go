package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestArgumentMistakesAreToolErrorsNotProtocolErrors: an error returned from a
// handler must arrive as a result with IsError set, because that is the only form
// the model can read and correct itself against. A JSON-RPC error would hide the
// reason from it entirely.
func TestArgumentMistakesAreToolErrorsNotProtocolErrors(t *testing.T) {
	cases := []struct {
		tool string
		args map[string]any
		want string
	}{
		{"get_device", map[string]any{"dev_eui": "00:11:22:33:44:55:66:77"},
			"not 16 hexadecimal characters"},
		{"get_device", map[string]any{"dev_eui": "xyz"}, "not 16 hexadecimal characters"},
		{"get_device", map[string]any{"dev_eui": ""}, "not 16 hexadecimal characters"},
		{"get_device_session", map[string]any{"dev_eui": "001122"},
			"not 16 hexadecimal characters"},
		{"diagnose_device", map[string]any{"dev_eui": "not-an-eui"},
			"not 16 hexadecimal characters"},
		{"list_uplinks", map[string]any{"limit": 9000}, "between 1 and 500"},
		{"list_uplinks", map[string]any{"limit": -1}, "between 1 and 500"},
		{"list_events", map[string]any{"dev_eui": "zz"}, "not 16 hexadecimal characters"},
		{"get_model", map[string]any{"model_id": ""}, "model_id is required"},
		{"get_integration", map[string]any{"id": ""}, "id is required"},
		{"explain_event_reason", map[string]any{"reason": ""}, "reason is required"},
		{"explain_event_reason", map[string]any{"reason": "made_up_reason"},
			"not a reason this network server records"},
	}

	cs := newTestServer(t, healthyFake(t))
	for _, tc := range cases {
		raw, _ := json.Marshal(tc.args)
		t.Run(tc.tool+" "+string(raw), func(t *testing.T) {
			body, res := callText(t, cs, tc.tool, tc.args)
			if !res.IsError {
				t.Fatalf("want a tool-level error, got a success: %s", body)
			}
			if !strings.Contains(body, tc.want) {
				t.Errorf("message = %q, want it to contain %q", body, tc.want)
			}
		})
	}
}

// TestLowercaseDevEUIIsAccepted because a DevEUI copied out of a JSON response is
// lowercase, and refusing it would be a needless round trip.
func TestLowercaseDevEUIIsAccepted(t *testing.T) {
	body, res := callText(t, newTestServer(t, healthyFake(t)),
		"get_device", map[string]any{"dev_eui": "0011223344556677"})
	if res.IsError {
		t.Fatalf("a lowercase DevEUI was refused: %s", body)
	}
}

// TestExplainEventReasonNamesTheKnownSet when it rejects, so a model that guessed a
// reason can pick the right one on the next turn instead of guessing again.
func TestExplainEventReasonNamesTheKnownSet(t *testing.T) {
	body, res := callText(t, newTestServer(t, healthyFake(t)),
		"explain_event_reason", map[string]any{"reason": "nope"})
	if !res.IsError {
		t.Fatalf("want a rejection, got: %s", body)
	}
	for _, want := range []string{"f_cnt_replay", "duty_cycle_denied", "join_mic_failed"} {
		if !strings.Contains(body, want) {
			t.Errorf("the rejection did not list %q, so the caller cannot correct itself: %s",
				want, body)
		}
	}
}

func TestExplainEventReasonExplainsWithoutInventing(t *testing.T) {
	body, res := callText(t, newTestServer(t, healthyFake(t)),
		"explain_event_reason", map[string]any{"reason": "f_cnt_gap_too_large"})
	if res.IsError {
		t.Fatalf("want an explanation, got an error: %s", body)
	}
	for _, want := range []string{"frame counter", "lost", "detail"} {
		if !strings.Contains(strings.ToLower(body), want) {
			t.Errorf("explanation %q does not mention %q", body, want)
		}
	}
	// The hedge is the point: this is a likelihood, and the event's own detail is the
	// evidence. An agent that reads a cause as a conclusion stops looking.
	if !strings.Contains(body, "likelihood") {
		t.Errorf("explanation should say it is a likelihood, not a conclusion: %s", body)
	}
}

// TestExplainEventReasonToleratesWhatAModelTypes: the value usually arrives copied
// from a JSON response, possibly uppercased or padded. Refusing that would produce a
// rejection that reads as "that reason does not exist" about a reason that does.
func TestExplainEventReasonToleratesWhatAModelTypes(t *testing.T) {
	for _, raw := range []string{"F_CNT_GAP_TOO_LARGE", "  f_cnt_gap_too_large  "} {
		body, res := callText(t, newTestServer(t, healthyFake(t)),
			"explain_event_reason", map[string]any{"reason": raw})
		if res.IsError {
			t.Errorf("reason %q was refused: %s", raw, body)
		}
	}
}
