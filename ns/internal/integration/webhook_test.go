package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestWebhookPostsTheEvent(t *testing.T) {
	received := make(chan Event, 1)
	headers := make(chan http.Header, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		var ev Event
		if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
			t.Errorf("decode the posted event: %v", err)
		}
		headers <- r.Header.Clone()
		received <- ev
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	w, err := NewWebhook(WebhookConfig{URL: srv.URL, Token: "secret-token"})
	if err != nil {
		t.Fatalf("NewWebhook: %v", err)
	}
	ev := Event{Kind: KindUplink, DeviceEUI: "0011223344556677", FPort: 10, Data: "3q2+7w=="}
	if err := w.Publish(context.Background(), ev); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	got := <-received
	if got.Kind != ev.Kind || got.DeviceEUI != ev.DeviceEUI || got.FPort != 10 || got.Data != ev.Data {
		t.Errorf("received %+v, want %+v", got, ev)
	}
	h := <-headers
	if auth := h.Get("Authorization"); auth != "Bearer secret-token" {
		t.Errorf("Authorization = %q, want the bearer token", auth)
	}
	if ua := h.Get("User-Agent"); ua == "" {
		t.Error("a User-Agent helps the receiver's logs identify the sender")
	}
	if w.Name() == "" {
		t.Error("Name must identify the sink")
	}
}

// TestWebhookRetriesServerSideFailures covers the point of having retries: a
// receiver that is briefly unavailable must not cost the event.
func TestWebhookRetriesServerSideFailures(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	w, err := NewWebhook(WebhookConfig{URL: srv.URL, Attempts: 3, Backoff: time.Millisecond})
	if err != nil {
		t.Fatalf("NewWebhook: %v", err)
	}
	if err := w.Publish(context.Background(), Event{Kind: KindUplink}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if got := hits.Load(); got != 3 {
		t.Errorf("the endpoint was called %d times, want 3 (two failures then success)", got)
	}
}

// TestWebhookDoesNotRetryClientErrors: a 4xx means the receiver does not want
// this event. Retrying turns a misconfiguration into load on someone else's
// service, so one attempt is the whole story.
func TestWebhookDoesNotRetryClientErrors(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	w, err := NewWebhook(WebhookConfig{URL: srv.URL, Attempts: 5, Backoff: time.Millisecond})
	if err != nil {
		t.Fatalf("NewWebhook: %v", err)
	}
	err = w.Publish(context.Background(), Event{Kind: KindUplink})
	if err == nil {
		t.Fatal("a 404 must be reported as a failure")
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("the endpoint was called %d times, want 1 (4xx is not retried)", got)
	}
}

func TestWebhookGivesUpAfterTheAttemptBudget(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	w, err := NewWebhook(WebhookConfig{URL: srv.URL, Attempts: 2, Backoff: time.Millisecond})
	if err != nil {
		t.Fatalf("NewWebhook: %v", err)
	}
	if err := w.Publish(context.Background(), Event{Kind: KindUplink}); err == nil {
		t.Fatal("exhausting the attempts must be an error, so the dispatcher counts a failure")
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("the endpoint was called %d times, want 2", got)
	}
}

// TestWebhookCancelledByShutdown: a delivery in progress must not outlive the
// process, which is what lets Close return promptly with a dead endpoint.
func TestWebhookCancelledByShutdown(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-block
	}))
	// Unblocking must happen before srv.Close(): the server waits for handlers
	// still running, so releasing them afterwards would hang this test rather
	// than the code under test.
	defer func() { close(block); srv.Close() }()

	w, err := NewWebhook(WebhookConfig{URL: srv.URL, Timeout: 2 * time.Second, Attempts: 1})
	if err != nil {
		t.Fatalf("NewWebhook: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Publish(ctx, Event{Kind: KindUplink}) }()

	time.Sleep(20 * time.Millisecond) // let the request reach the handler
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a cancelled delivery must report an error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Publish ignored the cancelled context")
	}
}

func TestWebhookRejectsUnusableURLs(t *testing.T) {
	for _, raw := range []string{"", "not a url", "ftp://example.test/hook", "http://"} {
		if _, err := NewWebhook(WebhookConfig{URL: raw}); err == nil {
			t.Errorf("NewWebhook(%q) accepted a URL it cannot post to", raw)
		}
	}
}

// TestWebhookReadsAndClosesTheBody keeps connections reusable; asserting it
// through the server's close behaviour would be fragile, so the check is that a
// second publish reuses the same server without leaking a goroutine per call.
func TestWebhookReadsAndClosesTheBody(t *testing.T) {
	var body io.ReadCloser
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = r.Body
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	w, err := NewWebhook(WebhookConfig{URL: srv.URL, Attempts: 1})
	if err != nil {
		t.Fatalf("NewWebhook: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := w.Publish(context.Background(), Event{Kind: KindUplink}); err != nil {
			t.Fatalf("Publish %d: %v", i, err)
		}
	}
	if body == nil {
		t.Fatal("the handler never ran")
	}
}
