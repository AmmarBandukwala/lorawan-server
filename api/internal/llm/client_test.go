package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// respondWith builds a client whose endpoint answers with a fixed status, content type
// and body. Most of what this package does is read, so most tests need the transport
// and nothing else.
func respondWith(t *testing.T, status int, contentType, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
		}
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)

	client, err := New(Config{BaseURL: srv.URL + "/v1", Model: "test-model"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

func jsonServer(t *testing.T, body string) *Client {
	t.Helper()
	return respondWith(t, http.StatusOK, "application/json", body)
}

func TestConfigRejectsAnUnusableEndpoint(t *testing.T) {
	for name, cfg := range map[string]Config{
		"no base URL":       {Model: "m"},
		"a relative URL":    {BaseURL: "/v1", Model: "m"},
		"no scheme":         {BaseURL: "ollama:11434/v1", Model: "m"},
		"a non-http scheme": {BaseURL: "grpc://ollama:11434", Model: "m"},
		"no model":          {BaseURL: "http://ollama:11434/v1"},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: New accepted it", name)
		}
	}
	// A trailing slash is cosmetic and must not be a startup failure, but the endpoint
	// must come back normalised so what the UI displays is what is actually used.
	client, err := New(Config{BaseURL: "http://ollama:11434/v1/", Model: "m"})
	if err != nil {
		t.Fatalf("a trailing slash should be tolerated: %v", err)
	}
	if got := client.Endpoint(); got != "http://ollama:11434/v1" {
		t.Errorf("Endpoint() = %q, want it trimmed", got)
	}
}

// TestRequestShape pins what goes over the wire. A wrong path is a 404 that reads as
// "this endpoint does not exist", and a missing auth header on a hosted provider reads
// as a bad key.
func TestRequestShape(t *testing.T) {
	var gotPath, gotAuth, gotAccept, gotMethod string
	var gotBody Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotAccept, gotMethod = r.URL.Path, r.Header.Get("Authorization"),
			r.Header.Get("Accept"), r.Method
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"model":"m","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(srv.Close)

	client, err := New(Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test", Model: "default-model"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.Complete(context.Background(), Request{}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if gotPath != "/v1/chat/completions" {
		t.Errorf("path = %q, want /v1/chat/completions", gotPath)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q for a non-streaming request", gotAccept)
	}
	// The default model applies when the request names none, so a caller with no
	// opinion gets the configured one rather than an empty string, which the endpoint
	// rejects with a message about a model called "".
	if gotBody.Model != "default-model" {
		t.Errorf("model = %q, want the configured default", gotBody.Model)
	}
	if gotBody.Stream {
		t.Error("Complete must not set stream")
	}
	// stream_options must be absent, not false: several runtimes reject the field when
	// it arrives on a non-streaming request.
	raw, err := json.Marshal(gotBody)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "stream_options") {
		t.Errorf("stream_options must be omitted when not streaming: %s", raw)
	}
}

func TestCompleteAssemblesTheFirstChoice(t *testing.T) {
	client := jsonServer(t, `{"model":"llama3.1","choices":[{"message":{"content":" 42 devices ",`+
		`"tool_calls":[{"id":"call_1","type":"function","function":{"name":"list_devices",`+
		`"arguments":"{}"}}]},"finish_reason":"tool_calls"}],`+
		`"usage":{"prompt_tokens":11,"completion_tokens":5,"total_tokens":16}}`)

	got, err := client.Complete(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got.Content != " 42 devices " || got.Model != "llama3.1" || got.FinishReason != "tool_calls" {
		t.Errorf("assembled = %+v", got)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].Function.Name != "list_devices" {
		t.Fatalf("tool calls = %+v", got.ToolCalls)
	}
	if got.Usage.TotalTokens != 16 {
		t.Errorf("usage = %+v", got.Usage)
	}
}

// TestSuccessWithNoChoicesIsAnError: a proxy or cache answering with an empty choices
// array would otherwise read as "the model had nothing to say".
func TestSuccessWithNoChoicesIsAnError(t *testing.T) {
	_, err := jsonServer(t, `{"model":"m","choices":[]}`).Complete(context.Background(), Request{})
	if err == nil || !strings.Contains(err.Error(), "no choices") {
		t.Errorf("err = %v, want it to say the endpoint returned no choices", err)
	}
}

func TestErrorStatusesAreTypedAndCarryTheProvidersWords(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		check  func(error) bool
		want   string
	}{
		{"401 nested", http.StatusUnauthorized,
			`{"error":{"message":"Incorrect API key provided","type":"invalid_request_error"}}`,
			func(err error) bool { var e *AuthError; return errors.As(err, &e) },
			"Incorrect API key provided"},
		{"429 is distinct from 500", http.StatusTooManyRequests,
			`{"error":{"message":"rate limit exceeded"}}`,
			func(err error) bool { var e *RateLimitError; return errors.As(err, &e) },
			"rate limit exceeded"},
		{"500 keeps its status", http.StatusInternalServerError,
			`{"error":{"message":"model is not loaded"}}`,
			func(err error) bool {
				var e *APIError
				return errors.As(err, &e) && e.Status == 500
			}, "model is not loaded"},
		// Ollama's flat shape. Without this branch an operator sees "no detail
		// supplied" for a message that was right there in the body.
		{"flat error string", http.StatusNotFound, `{"error":"model not found"}`,
			func(err error) bool { var e *APIError; return errors.As(err, &e) },
			"model not found"},
		{"html from a proxy", http.StatusBadGateway, `<html>502 Bad Gateway</html>`,
			func(err error) bool { var e *APIError; return errors.As(err, &e) },
			"502 Bad Gateway"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := respondWith(t, tc.status, "application/json", tc.body).
				Complete(context.Background(), Request{})
			if err == nil {
				t.Fatal("want an error, got nil")
			}
			if !tc.check(err) {
				t.Errorf("wrong type: %T (%v)", err, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

// TestUnreachableEndpointNamesTheAddress: whether this points at a local runtime or a
// hosted API is the question an operator is really asking when the assistant goes
// quiet, and the answer must be in the message.
func TestUnreachableEndpointNamesTheAddress(t *testing.T) {
	client, err := New(Config{BaseURL: "http://127.0.0.1:1/v1", Model: "m"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.Complete(context.Background(), Request{})
	if err == nil {
		t.Fatal("want an error, got nil")
	}
	if !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Errorf("err = %q, want it to name the endpoint", err)
	}
	// Once, not twice via Go's url.Error wrapper.
	if strings.Contains(err.Error(), `Post "`) || strings.Contains(err.Error(), "Post \"http") {
		t.Errorf("err = %q, want Go's request wrapper unwrapped away", err)
	}
	// The dial target may repeat the host, which is informative; the wrapper is what
	// would make the message read as two failures instead of one.
	if strings.Count(err.Error(), "http://127.0.0.1:1/v1") != 1 {
		t.Errorf("err = %q, want the endpoint named once", err)
	}
}
