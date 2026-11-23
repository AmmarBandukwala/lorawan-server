package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/fieldnode/lorawan-server/api/internal/llm"
	"github.com/fieldnode/lorawan-server/api/internal/mcpclient"
)

// fakeNetwork records every call so a test can assert what reached the network, which
// is the only way to prove a write was refused before it was attempted rather than
// merely reported as a failure afterwards.
type fakeNetwork struct {
	tools    []mcpclient.Tool
	results  map[string]string
	failures map[string]string

	calls   []string
	callLog []call
}

type call struct {
	name string
	args string
}

func (f *fakeNetwork) Tools(context.Context) ([]mcpclient.Tool, error) {
	return f.tools, nil
}

func (f *fakeNetwork) Call(_ context.Context, name string, args json.RawMessage) (string, error) {
	f.calls = append(f.calls, name)
	f.callLog = append(f.callLog, call{name: name, args: string(args)})
	if reason, bad := f.failures[name]; bad {
		return "", fmt.Errorf("%s", reason)
	}
	if result, ok := f.results[name]; ok {
		return result, nil
	}
	return `{}`, nil
}

// standardTools is a listener like the real one: mostly reads, with the four writes the
// operate profile would add.
func standardTools() []mcpclient.Tool {
	read := func(name string) mcpclient.Tool {
		return mcpclient.Tool{
			Name:        name,
			Description: "read " + name,
			ReadOnly:    true,
			Schema:      json.RawMessage(`{"type":"object"}`),
		}
	}
	write := func(name string) mcpclient.Tool {
		return mcpclient.Tool{
			Name:        name,
			Description: "write " + name,
			ReadOnly:    false,
			Schema:      json.RawMessage(`{"type":"object"}`),
		}
	}
	return []mcpclient.Tool{
		read("network_status"), read("list_devices"), read("get_device"),
		read("get_device_session"), read("list_uplinks"), read("list_events"),
		read("explain_event_reason"), read("diagnose_device"),
		write("enqueue_downlink"), write("cancel_downlink"),
		write("request_device_status"), write("patch_device"),
	}
}

// scriptedModel returns a canned sequence of responses, one per round, and records the
// requests so the replay order can be asserted.
type scriptedModel struct {
	responses []*llm.Response
	err       error

	requests []llm.Request
}

func (m *scriptedModel) Stream(_ context.Context, req llm.Request, onContent func(string) error) (*llm.Response, error) {
	m.requests = append(m.requests, req)
	if m.err != nil {
		return nil, m.err
	}
	index := len(m.requests) - 1
	if index >= len(m.responses) {
		// Running past the script is itself a failure worth surfacing: it means the
		// loop asked again when it should have stopped, and a test that only checked
		// the final output would miss it.
		return nil, fmt.Errorf("script exhausted after %d rounds", index)
	}
	response := m.responses[index]
	if response.Content != "" && onContent != nil {
		if err := onContent(response.Content); err != nil {
			return nil, err
		}
	}
	return response, nil
}

// toolCall builds a model response asking for one tool.
func toolCall(id, name, args string) *llm.Response {
	return &llm.Response{
		FinishReason: "tool_calls",
		ToolCalls: []llm.ToolCall{{
			ID:       id,
			Type:     "function",
			Function: llm.FunctionCall{Name: name, Arguments: args},
		}},
	}
}

func textResponse(text string) *llm.Response {
	return &llm.Response{Content: text, FinishReason: "stop", Model: "llama3.1"}
}

// collect runs Ask and returns every emitted event.
func collect(t *testing.T, svc *Service, transcript []llm.Message) ([]Event, error) {
	t.Helper()
	var events []Event
	err := svc.Ask(context.Background(), transcript, func(e Event) error {
		events = append(events, e)
		return nil
	})
	return events, err
}

// newTestService takes the Model interface rather than *scriptedModel so a test can
// supply a model with different behaviour — one that never settles, for instance —
// without reimplementing this wiring.
func newTestService(t *testing.T, net *fakeNetwork, model Model) *Service {
	t.Helper()
	svc, err := New(Config{
		Network: net, Model: model,
		Vocabulary: "f_cnt_replay: the counter is at or below the highest accepted",
		ModelID:    "llama3.1", Endpoint: "http://ollama:11434/v1",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return svc
}

func question(text string) []llm.Message {
	return []llm.Message{{Role: llm.RoleUser, Content: text}}
}
