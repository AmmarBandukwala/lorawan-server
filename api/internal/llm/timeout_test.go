package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A slow first byte is the ordinary case for a local model, not a fault: a runtime
// loading a 7B model onto a CPU spends tens of seconds on prompt processing before it
// produces a single byte, headers included.
//
// ResponseHeaderTimeout was wired to the connect budget, so the transport gave up after
// ten seconds and the 120-second first-token budget — which is enforced while reading
// the body — never got to run. Every question to a cold local model failed with
// "timeout awaiting response headers", which reads exactly like an endpoint that is
// down. This was found by pointing the console at a real container, not by a hosted
// endpoint, because a hosted one answers in well under a second.
func TestSlowFirstByteIsToleratedUpToTheFirstTokenBudget(t *testing.T) {
	const delay = 1200 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(delay)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, event(contentChunk("late but complete")))
	}))
	t.Cleanup(srv.Close)

	// connect sits far below the delay on purpose. Reaching the endpoint is instant;
	// answering is what takes time. If the transport still uses the connect budget for
	// response headers, this request fails and the test fails with it.
	client, err := New(Config{
		BaseURL:           srv.URL + "/v1",
		Model:             "slow-local-model",
		ConnectTimeout:    100 * time.Millisecond,
		FirstTokenTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var streamed strings.Builder
	got, err := client.Stream(context.Background(), Request{},
		func(fragment string) error { streamed.WriteString(fragment); return nil })
	if err != nil {
		t.Fatalf("a model that took %v to answer was treated as unreachable: %v", delay, err)
	}
	if got.Content != "late but complete" {
		t.Errorf("assembled = %q", got.Content)
	}
	if streamed.String() != "late but complete" {
		t.Errorf("callback saw %q", streamed.String())
	}
}

// The counterpart, and the one that matters more: raising the header timeout must not
// quietly remove the protection. A budget shorter than the delay still has to fail, or
// the previous test would pass just as well with every timeout deleted.
func TestFirstTokenBudgetStillBoundsASilentEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, event(contentChunk("too late")))
	}))
	t.Cleanup(srv.Close)

	client, err := New(Config{
		BaseURL:           srv.URL + "/v1",
		Model:             "silent-model",
		ConnectTimeout:    100 * time.Millisecond,
		FirstTokenTimeout: 250 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	start := time.Now()
	_, err = client.Stream(context.Background(), Request{}, nil)
	if err == nil {
		t.Fatal("an endpoint silent past the first-token budget reported success")
	}
	// It must fail on the budget, not by hanging: the point of the budget is a bound.
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("gave up after %v, well past the 250ms budget", elapsed)
	}
}
