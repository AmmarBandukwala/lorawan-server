package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/fieldnode/lorawan-server/api/internal/llm"
	"github.com/fieldnode/lorawan-server/api/internal/mcpclient"
)

// The budgets. A small local model can decide to look at one more thing forever, and
// on CPU an unbounded loop is not a slow answer — it is a hung request on a loaded
// machine, with no way for the operator to tell the two apart.

// loopingModel never stops asking. It exists because a scripted model that runs out of
// responses reports "script exhausted", which would pass a termination test for the
// wrong reason: the loop would have carried on had the script been longer.
type loopingModel struct {
	requests []llm.Request
}

func (m *loopingModel) Stream(_ context.Context, req llm.Request, _ func(string) error) (*llm.Response, error) {
	m.requests = append(m.requests, req)
	round := len(m.requests)
	return &llm.Response{
		FinishReason: "tool_calls",
		ToolCalls: []llm.ToolCall{{
			ID:       fmt.Sprintf("call_%d", round),
			Type:     "function",
			Function: llm.FunctionCall{Name: "list_events", Arguments: `{}`},
		}},
	}, nil
}

// TestRoundLimitStopsALoopThatNeverSettles: the loop must end on its own terms and say
// so, rather than running until the operator closes the tab.
func TestRoundLimitStopsALoopThatNeverSettles(t *testing.T) {
	net := &fakeNetwork{tools: standardTools(), results: map[string]string{}}
	model := &loopingModel{}

	events, err := collect(t, newTestService(t, net, model), question("why is it slow?"))
	if err == nil {
		t.Fatalf("the loop terminated after %d rounds without an answer; it should fail",
			len(model.requests))
	}
	if len(model.requests) != maxRounds {
		t.Errorf("the model was consulted %d times, want the cap of %d",
			len(model.requests), maxRounds)
	}
	if !strings.Contains(err.Error(), "without reaching an answer") {
		t.Errorf("error = %q, want it to say the loop ran out rather than that something broke", err)
	}
	// The failure must reach the page as an event too: a returned error alone leaves
	// the browser showing a half-typed answer with no explanation.
	var sawError bool
	for _, event := range events {
		if event.Type == EventError {
			sawError = true
		}
	}
	if !sawError {
		t.Error("the budget failure was not emitted as an error event")
	}
}

// TestCallBudgetIsRefusedBeforeTheCallsHappen: a model can ask for thirty lookups in a
// single response. Refusing after spending the budget would be a limit in name only —
// the network would have absorbed all thirty.
func TestCallBudgetIsRefusedBeforeTheCallsHappen(t *testing.T) {
	net := &fakeNetwork{tools: standardTools(), results: map[string]string{}}

	var calls []llm.ToolCall
	for i := range maxCalls + 3 {
		calls = append(calls, llm.ToolCall{
			ID:       fmt.Sprintf("call_%d", i),
			Type:     "function",
			Function: llm.FunctionCall{Name: "get_device", Arguments: `{"dev_eui":"0011223344556677"}`},
		})
	}
	model := &scriptedModel{responses: []*llm.Response{{FinishReason: "tool_calls", ToolCalls: calls}}}

	_, err := collect(t, newTestService(t, net, model), question("check every device"))
	if err == nil {
		t.Fatal("a response asking for more than the budget should be refused")
	}
	if len(net.calls) != 0 {
		t.Errorf("%d lookups ran (%v); the budget must be checked before any of them",
			len(net.calls), net.calls)
	}
	// The message has to say what to do differently, because "too many lookups" alone
	// reads as a bug in the assistant.
	if !strings.Contains(err.Error(), "without finishing") || !strings.Contains(err.Error(), "one device") {
		t.Errorf("error = %q, want it to name the budget and suggest narrowing the question", err)
	}
}

// TestBudgetSpansRounds: capping per round would still allow maxRounds × maxCalls
// lookups, which on a network server that logs every request is a self-inflicted flood.
func TestBudgetSpansRounds(t *testing.T) {
	net := &fakeNetwork{tools: standardTools(), results: map[string]string{}}

	// Four rounds of four calls each is 16: over a budget of 12, and under any
	// per-round reading of it.
	var responses []*llm.Response
	id := 0
	for range 4 {
		var calls []llm.ToolCall
		for range 4 {
			id++
			calls = append(calls, llm.ToolCall{
				ID:       fmt.Sprintf("call_%d", id),
				Type:     "function",
				Function: llm.FunctionCall{Name: "network_status", Arguments: `{}`},
			})
		}
		responses = append(responses, &llm.Response{FinishReason: "tool_calls", ToolCalls: calls})
	}
	model := &scriptedModel{responses: responses}

	if _, err := collect(t, newTestService(t, net, model), question("everything please")); err == nil {
		t.Fatal("16 lookups should exceed a budget of 12")
	}
	if len(net.calls) > maxCalls {
		t.Errorf("%d lookups ran, over a budget of %d", len(net.calls), maxCalls)
	}
}

// TestTrimmedHistoryIsDisclosedToTheModel: a model that has silently lost its earlier
// turns answers as though the conversation began with the last message, and an operator
// cannot tell that from a model that simply did not understand the reference.
func TestTrimmedHistoryIsDisclosedToTheModel(t *testing.T) {
	net := &fakeNetwork{tools: standardTools(), results: map[string]string{}}
	model := &scriptedModel{responses: []*llm.Response{textResponse("noted")}}

	long := make([]llm.Message, 0, maxTranscript+6)
	for i := range maxTranscript + 6 {
		role := llm.RoleUser
		if i%2 == 1 {
			role = llm.RoleAssistant
		}
		long = append(long, llm.Message{Role: role, Content: fmt.Sprintf("turn %d", i)})
	}

	if _, err := collect(t, newTestService(t, net, model), long); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	sent := model.requests[0].Messages
	if len(sent) != maxTranscript+1 {
		t.Fatalf("the model received %d messages, want the system turn plus %d",
			len(sent), maxTranscript)
	}
	// The oldest surviving turn is the one that proves the front was dropped rather
	// than the back: keeping the wrong end would still fit the count.
	oldest := sent[1]
	if want := fmt.Sprintf("turn %d", len(long)-maxTranscript); oldest.Content != want {
		t.Errorf("oldest kept turn = %q, want %q", oldest.Content, want)
	}
	if !strings.Contains(sent[0].Content, "earliest turns are not included") {
		t.Error("history was dropped but the model was not told, so it will guess at what it lost")
	}
}

// TestUntrimmedHistoryIsNotDisclosedAsTrimmed is the other half: a note that always
// appears teaches the model to hedge about context it can still see.
func TestUntrimmedHistoryIsNotDisclosedAsTrimmed(t *testing.T) {
	net := &fakeNetwork{tools: standardTools(), results: map[string]string{}}
	model := &scriptedModel{responses: []*llm.Response{textResponse("fine")}}

	if _, err := collect(t, newTestService(t, net, model), []llm.Message{
		{Role: llm.RoleUser, Content: "first"},
		{Role: llm.RoleAssistant, Content: "second"},
	}); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if got := model.requests[0].Messages[0].Content; strings.Contains(got, "earliest turns are not included") {
		t.Error("the trimming note appeared for a conversation that was never trimmed")
	}
}

// TestListenerWithNoReadToolsFailsBeforeTheModelIsAsked: a listener whose tools are all
// writes must not produce an answer built on nothing. Offering an empty tool list would
// leave the model free to answer from general LoRaWAN knowledge, which is the failure
// this whole package exists to prevent.
func TestListenerWithNoReadToolsFailsBeforeTheModelIsAsked(t *testing.T) {
	onlyWrites := &fakeNetwork{
		tools: []mcpclient.Tool{{Name: "enqueue_downlink", Description: "write", ReadOnly: false}},
	}
	model := &scriptedModel{responses: []*llm.Response{textResponse("all quiet")}}

	_, err := collect(t, newTestService(t, onlyWrites, model), question("status?"))
	if err == nil {
		t.Fatal("a listener with no read tools should be a failure, not an answer")
	}
	if len(model.requests) != 0 {
		t.Errorf("the model was consulted %d times with no tools available", len(model.requests))
	}
}

// TestMalformedArgumentsGoBackToTheModel: small models emit trailing commas, and the
// same model fixes it on the next attempt far more often than an operator would believe.
// Rejecting outright would end a good answer at the first syntax slip.
func TestMalformedArgumentsGoBackToTheModel(t *testing.T) {
	net := &fakeNetwork{tools: standardTools(), results: map[string]string{}}
	model := &scriptedModel{responses: []*llm.Response{
		toolCall("call_1", "get_device", `{"dev_eui":"0011223344556677",}`),
		textResponse("let me try that again"),
	}}

	if _, err := collect(t, newTestService(t, net, model), question("device details")); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if len(net.calls) != 0 {
		t.Errorf("malformed arguments reached the network: %v", net.callLog)
	}
	if !strings.Contains(lastToolResult(model.requests[1]), "not valid JSON") {
		t.Errorf("the model was told %q, want it told the arguments were not valid JSON",
			lastToolResult(model.requests[1]))
	}
}

// TestToolFailureGoesBackToTheModelNotTheOperator: a device that does not exist is a
// normal thing for a lookup to return mid-answer, and the model corrects a mistyped
// DevEUI on its own if it is shown the failure.
func TestToolFailureGoesBackToTheModelNotTheOperator(t *testing.T) {
	net := &fakeNetwork{
		tools:    standardTools(),
		results:  map[string]string{},
		failures: map[string]string{"get_device": "dev_eui 0011223344556677 is not provisioned"},
	}
	model := &scriptedModel{responses: []*llm.Response{
		toolCall("call_1", "get_device", `{"dev_eui":"0011223344556677"}`),
		textResponse("that one is not provisioned"),
	}}

	events, err := collect(t, newTestService(t, net, model), question("device details"))
	if err != nil {
		t.Fatalf("Ask: %v; a failed lookup must not end the answer", err)
	}
	var sawFailedResult bool
	for _, event := range events {
		if event.Type == EventToolResult && event.Tool == "get_device" {
			sawFailedResult = true
			if event.OK {
				t.Error("the result event claims success for a lookup that failed")
			}
		}
	}
	if !sawFailedResult {
		t.Error("the failed lookup was not reported to the operator")
	}
	said := lastToolResult(model.requests[1])
	if !strings.Contains(said, "This lookup failed") || !strings.Contains(said, "not provisioned") {
		t.Errorf("the model was told %q, want the upstream sentence so it can correct itself", said)
	}
}

// TestEmitFailureAbortsTheAnswer: emit fails when the browser closes its connection.
// Carrying on would spend a full CPU generation on an answer nobody will read.
func TestEmitFailureAbortsTheAnswer(t *testing.T) {
	net := &fakeNetwork{tools: standardTools(), results: map[string]string{}}
	model := &scriptedModel{responses: []*llm.Response{
		toolCall("call_1", "network_status", `{}`),
		textResponse("carried on anyway"),
	}}
	svc := newTestService(t, net, model)

	gone := errors.New("connection closed")
	err := svc.Ask(context.Background(), question("status?"), func(e Event) error {
		if e.Type == EventStatus {
			return gone
		}
		return nil
	})
	if !errors.Is(err, gone) {
		t.Fatalf("Ask returned %v, want the emit error propagated", err)
	}
	if len(model.requests) != 0 {
		t.Errorf("the model was consulted %d times after the reader went away", len(model.requests))
	}
}
