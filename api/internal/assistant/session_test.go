package assistant

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/fieldnode/lorawan-server/api/internal/llm"
)

// The per-question session. An MCP connection belongs to a conversation, not to a
// process, so the console must not hold one open forever: a listener that restarts
// would then stay broken until the console restarted too.

func TestEachQuestionGetsItsOwnSessionAndReleasesIt(t *testing.T) {
	var opened, closed []int
	next := 0

	newService := func(t *testing.T, answer string) *Service {
		t.Helper()
		svc, err := New(Config{
			Dial: func(context.Context) (Network, func(), error) {
				next++
				id := next
				opened = append(opened, id)
				net := &fakeNetwork{tools: standardTools(), results: map[string]string{}}
				return net, func() { closed = append(closed, id) }, nil
			},
			Model:    &scriptedModel{responses: []*llm.Response{textResponse(answer)}},
			ModelID:  "llama3.1",
			Endpoint: "http://ollama:11434/v1",
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return svc
	}

	for i := range 3 {
		svc := newService(t, fmt.Sprintf("answer %d", i))
		if _, err := collect(t, svc, question("status?")); err != nil {
			t.Fatalf("Ask %d: %v", i, err)
		}
	}

	if len(opened) != 3 {
		t.Fatalf("opened %d sessions, want one per question: %v", len(opened), opened)
	}
	// Released, not merely counted: a release that never runs is how a long-lived
	// console slowly runs the listener out of connections, and a single-question test
	// would not show it at all.
	if len(closed) != 3 {
		t.Errorf("released %d sessions, want 3: %v", len(closed), closed)
	}
	for i := range opened {
		if opened[i] != closed[i] {
			t.Errorf("session %d was opened as %d and closed as %d", i, opened[i], closed[i])
		}
	}
}

// TestSessionIsReleasedWhenTheAnswerFails: the failure path is the one that leaks. A
// release only on the success path would strand a connection on every listener
// restart, which is exactly the situation the dialer exists to survive.
func TestSessionIsReleasedWhenTheAnswerFails(t *testing.T) {
	var closed int
	model := &scriptedModel{err: fmt.Errorf("the runtime is not answering")}

	svc, err := New(Config{
		Dial: func(context.Context) (Network, func(), error) {
			return &fakeNetwork{tools: standardTools(), results: map[string]string{}},
				func() { closed++ }, nil
		},
		Model: model,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := collect(t, svc, question("status?")); err == nil {
		t.Fatal("want the model failure to surface")
	}
	if closed != 1 {
		t.Errorf("released %d sessions after a failed answer, want 1", closed)
	}
}

// TestDialFailureNamesTheListener: nothing was opened, so nothing may be released —
// calling a nil release would panic on the first question after a listener stops,
// which is the moment an operator is watching hardest. The message also has to name
// the listener: a bare "connection refused" reads like a bug in the console.
func TestDialFailureNamesTheListenerAndReleasesNothing(t *testing.T) {
	var closed int
	svc, err := New(Config{
		Dial: func(context.Context) (Network, func(), error) {
			return nil, nil, fmt.Errorf("connection refused")
		},
		Model: &scriptedModel{responses: []*llm.Response{textResponse("should not get here")}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	events, err := collect(t, svc, question("status?"))
	if err == nil {
		t.Fatal("a listener that cannot be reached must fail the question")
	}
	if closed != 0 {
		t.Errorf("released %d sessions when none was opened", closed)
	}
	var said string
	for _, e := range events {
		if e.Type == EventError {
			said = e.Text
		}
	}
	if !strings.Contains(said, "MCP listener") {
		t.Errorf("the operator was told %q, want it to name the listener", said)
	}
	if !strings.Contains(said, "connection refused") {
		t.Errorf("the operator was told %q, want the underlying cause kept", said)
	}
}

// TestDialHappensOncePerQuestionNotOncePerLookup: opening a session per tool call
// would multiply the handshake cost by the number of lookups and, worse, let the
// model's second lookup hit a different listener state than its first.
func TestDialHappensOncePerQuestionNotOncePerLookup(t *testing.T) {
	var dials int
	model := &scriptedModel{responses: []*llm.Response{
		toolCall("call_1", "network_status", `{}`),
		toolCall("call_2", "list_devices", `{}`),
		textResponse("two lookups, one session"),
	}}

	svc, err := New(Config{
		Dial: func(context.Context) (Network, func(), error) {
			dials++
			return &fakeNetwork{tools: standardTools(), results: map[string]string{}},
				func() {}, nil
		},
		Model: model,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := collect(t, svc, question("status then devices?")); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if dials != 1 {
		t.Errorf("dialed %d times for one question with two lookups, want 1", dials)
	}
}

// TestNewRejectsNeitherNetworkNorDial: with both nil the service would answer every
// question by failing at the first lookup, which is a startup mistake, not a runtime
// state.
func TestNewRejectsNeitherNetworkNorDial(t *testing.T) {
	if _, err := New(Config{Model: &scriptedModel{}}); err == nil {
		t.Error("New accepted a service with nothing to read from")
	}
	if _, err := New(Config{Network: &fakeNetwork{}}); err == nil {
		t.Error("New accepted a service with no model")
	}
}
