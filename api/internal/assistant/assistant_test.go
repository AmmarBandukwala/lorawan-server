package assistant

import (
	"strings"
	"testing"

	"github.com/fieldnode/lorawan-server/api/internal/llm"
)

// TestWriteToolsAreNeverOfferedToTheModel is the load-bearing test of this package.
//
// The filter is the access control. If a write tool reaches the tool list, everything
// else in this package is decoration: a model that can see enqueue_downlink will, given
// a confident enough nudge from a user message, call it.
func TestWriteToolsAreNeverOfferedToTheModel(t *testing.T) {
	net := &fakeNetwork{tools: standardTools(), results: map[string]string{}}
	model := &scriptedModel{responses: []*llm.Response{textResponse("all quiet")}}

	if _, err := collect(t, newTestService(t, net, model), question("status?")); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if len(model.requests) != 1 {
		t.Fatalf("got %d model requests, want 1", len(model.requests))
	}

	offered := map[string]bool{}
	for _, tool := range model.requests[0].Tools {
		offered[tool.Function.Name] = true
	}
	for _, write := range []string{"enqueue_downlink", "cancel_downlink",
		"request_device_status", "patch_device"} {
		if offered[write] {
			t.Errorf("%s was offered to the model", write)
		}
	}
	for _, read := range []string{"network_status", "list_devices", "diagnose_device",
		"explain_event_reason"} {
		if !offered[read] {
			t.Errorf("%s should have been offered", read)
		}
	}
	if len(offered) != 8 {
		t.Errorf("offered %d tools, want the 8 reads", len(offered))
	}
}

// TestHallucinatedWriteIsRefusedBeforeItReachesTheNetwork: a model inventing a
// plausible write name must not result in the listener being asked, because the
// listener authorises on the token and would run it.
func TestHallucinatedWriteIsRefusedBeforeItReachesTheNetwork(t *testing.T) {
	net := &fakeNetwork{tools: standardTools(), results: map[string]string{}}
	model := &scriptedModel{responses: []*llm.Response{
		toolCall("call_1", "send_downlink", `{"dev_eui":"0011223344556677"}`),
		textResponse("I cannot do that"),
	}}

	events, err := collect(t, newTestService(t, net, model), question("send a frame"))
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if len(net.calls) != 0 {
		t.Errorf("the network was asked to run %v; the refusal must happen first", net.calls)
	}

	// The refusal goes back to the model as a tool result, so the operator sees the
	// attempt and its failure but the answer continues.
	var sawCall, sawFailure bool
	for _, event := range events {
		if event.Type == EventToolCall && event.Tool == "send_downlink" {
			sawCall = true
		}
		if event.Type == EventToolResult && event.Tool == "send_downlink" && !event.OK {
			sawFailure = true
		}
	}
	if !sawCall {
		t.Error("the attempted call was not reported to the operator")
	}
	if !sawFailure {
		t.Error("the refusal was not reported as a failed lookup")
	}
	if len(model.requests) != 2 {
		t.Fatalf("the model was consulted %d times, want a second round after the refusal",
			len(model.requests))
	}
	// The refusal must say it cannot be done rather than that the tool was not found,
	// or the model will retry with a different name.
	instruction := lastToolResult(model.requests[1])
	if !strings.Contains(instruction, "only read") {
		t.Errorf("the model was told %q, want it told this assistant can only read", instruction)
	}
}

// TestAssistantMessageIsReplayedBeforeToolResults guards loop termination.
//
// The OpenAI shape is stateless: if the assistant turn carrying the tool calls is not
// replayed alongside the tool result, the model has no record of having asked and asks
// again, forever.
func TestAssistantMessageIsReplayedBeforeToolResults(t *testing.T) {
	net := &fakeNetwork{
		tools:   standardTools(),
		results: map[string]string{"network_status": `{"region":"EU868"}`},
	}
	model := &scriptedModel{responses: []*llm.Response{
		toolCall("call_1", "network_status", `{}`),
		textResponse("EU868, two gateways"),
	}}

	if _, err := collect(t, newTestService(t, net, model), question("status?")); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if len(model.requests) != 2 {
		t.Fatalf("got %d rounds, want 2", len(model.requests))
	}

	second := model.requests[1].Messages
	// system, user, assistant(with calls), tool
	if len(second) != 4 {
		t.Fatalf("second round carries %d messages: %v", len(second), roles(second))
	}
	if second[0].Role != llm.RoleSystem || second[1].Role != llm.RoleUser {
		t.Errorf("first two turns = %q,%q", second[0].Role, second[1].Role)
	}
	if second[2].Role != llm.RoleAssistant || len(second[2].ToolCalls) != 1 {
		t.Errorf("the assistant turn carrying the tool calls is missing: %+v", second[2])
	}
	if second[3].Role != llm.RoleTool || second[3].ToolCallID != "call_1" {
		t.Errorf("the tool result must name the call it answers: %+v", second[3])
	}
	if second[3].Content != `{"region":"EU868"}` {
		t.Errorf("tool result = %q", second[3].Content)
	}
}

// lastToolResult pulls the content of the most recent tool message out of a request.
func lastToolResult(req llm.Request) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == llm.RoleTool {
			return req.Messages[i].Content
		}
	}
	return ""
}

func roles(messages []llm.Message) []string {
	out := make([]string, 0, len(messages))
	for _, m := range messages {
		out = append(out, m.Role)
	}
	return out
}
