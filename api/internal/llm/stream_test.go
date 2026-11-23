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
	"time"
)

// sse builds a client whose endpoint streams body with the right content type.
func sse(t *testing.T, body string) *Client {
	t.Helper()
	return respondWith(t, http.StatusOK, "text/event-stream", body)
}

// event renders one SSE frame the way a runtime sends it.
func event(payload string) string { return "data: " + payload + "\n\n" }

// contentChunk builds a delta carrying a text fragment. It deliberately omits the
// model field: only the first chunk need carry it, and repeating a different value on
// every frame would make the assembled model depend on which chunk won.
func contentChunk(text string) string {
	return fmt.Sprintf(`{"choices":[{"delta":{"content":%q}}]}`, text)
}

// deltaFrame wraps tool-call deltas in the envelope a runtime sends them in.
func deltaFrame(deltasJSON string) string {
	return event(`{"choices":[{"delta":{"tool_calls":` + deltasJSON + `}}]}`)
}

// jsonString quotes a value for embedding in a JSON payload. These tests nest JSON
// inside JSON inside a Go literal; hand-escaping that produces a payload which
// silently fails to parse, and a frame that fails to parse is invisible in the
// assembled result — which is exactly how a broken assembler hides.
func jsonString(t *testing.T, v string) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

func TestStreamAssemblesContentFragmentsInOrder(t *testing.T) {
	body := event(`{"model":"llama3.1","choices":[{"delta":{"role":"assistant"}}]}`) +
		event(contentChunk("The gateway ")) +
		event(contentChunk("has never sent ")) +
		event(contentChunk("PULL_DATA.")) +
		event(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`) +
		event(`{"usage":{"prompt_tokens":9,"completion_tokens":12,"total_tokens":21}}`) +
		"data: [DONE]\n\n"

	var streamed strings.Builder
	got, err := sse(t, body).Stream(context.Background(), Request{},
		func(fragment string) error { streamed.WriteString(fragment); return nil })
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	want := "The gateway has never sent PULL_DATA."
	if got.Content != want {
		t.Errorf("assembled = %q, want %q", got.Content, want)
	}
	// The callback must see the same text the return value carries, or the browser
	// shows one answer and the transcript stores another.
	if streamed.String() != want {
		t.Errorf("streamed = %q, want %q", streamed.String(), want)
	}
	if got.Model != "llama3.1" || got.FinishReason != "stop" {
		t.Errorf("model/finish = %q/%q", got.Model, got.FinishReason)
	}
	if got.Usage.TotalTokens != 21 {
		t.Errorf("usage = %+v, want it read from the trailing usage chunk", got.Usage)
	}
	if len(got.ToolCalls) != 0 {
		t.Errorf("unexpected tool calls: %+v", got.ToolCalls)
	}
}

// TestStreamReassemblesFragmentedToolCalls is the load-bearing test of this file.
//
// A runtime sends the name once and the arguments in pieces. Concatenating them into
// the wrong slot, or overwriting the name with the empty string that arrives alongside
// an argument fragment, produces a call that runs the wrong tool on a live network.
func TestStreamReassemblesFragmentedToolCalls(t *testing.T) {
	var body strings.Builder
	body.WriteString(deltaFrame(`[{"index":0,"id":"call_a","type":"function",` +
		`"function":{"name":"list_uplinks","arguments":""}}]`))
	// The arguments arrive split at arbitrary points, including inside a string
	// value. These are raw string literals, so each is exactly what it looks like.
	for _, fragment := range []string{`{`, `"dev_eui":"`, `0011223344556677`, `","limit":`, `5}`} {
		body.WriteString(deltaFrame(`[{"index":0,"function":{"arguments":` +
			jsonString(t, fragment) + `}}]`))
	}
	body.WriteString(event(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`))
	body.WriteString("data: [DONE]\n\n")

	got, err := sse(t, body.String()).Stream(context.Background(), Request{}, nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if len(got.ToolCalls) != 1 {
		t.Fatalf("got %d calls, want 1: %+v", len(got.ToolCalls), got.ToolCalls)
	}
	call := got.ToolCalls[0]
	if call.ID != "call_a" || call.Type != "function" {
		t.Errorf("id/type = %q/%q", call.ID, call.Type)
	}
	// The name survives the argument fragments that arrive with an empty name field.
	if call.Function.Name != "list_uplinks" {
		t.Errorf("name = %q, want it preserved through the argument fragments", call.Function.Name)
	}
	want := `{"dev_eui":"0011223344556677","limit":5}`
	if call.Function.Arguments != want {
		t.Errorf("arguments = %q, want %q", call.Function.Arguments, want)
	}
}

// TestStreamKeepsParallelCallsApart covers two calls interleaved across chunks, which
// is where an index-blind implementation attributes arguments to the wrong name.
func TestStreamKeepsParallelCallsApart(t *testing.T) {
	var body strings.Builder
	body.WriteString(deltaFrame(`[{"index":0,"id":"call_a","type":"function",` +
		`"function":{"name":"get_device","arguments":""}},` +
		`{"index":1,"id":"call_b","type":"function",` +
		`"function":{"name":"get_device_session","arguments":""}}]`))
	// Deliberately out of order: the assembler must key on index, not on arrival, or
	// each call receives the other's arguments and the wrong tool runs.
	body.WriteString(deltaFrame(`[{"index":1,"function":{"arguments":` +
		jsonString(t, `{"dev_eui":"AABB"}`) + `}},` +
		`{"index":0,"function":{"arguments":` + jsonString(t, `{"dev_eui":"0011"}`) + `}}]`))
	body.WriteString(event(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`))
	body.WriteString("data: [DONE]\n\n")

	got, err := sse(t, body.String()).Stream(context.Background(), Request{}, nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if len(got.ToolCalls) != 2 {
		t.Fatalf("got %d calls, want 2: %+v", len(got.ToolCalls), got.ToolCalls)
	}
	// First-appearance order, not map order: a transcript must be reproducible.
	if got.ToolCalls[0].ID != "call_a" || got.ToolCalls[1].ID != "call_b" {
		t.Errorf("order = %q,%q want call_a,call_b", got.ToolCalls[0].ID, got.ToolCalls[1].ID)
	}
	// Each call's arguments belong to its own name.
	if got.ToolCalls[0].Function.Name != "get_device" ||
		got.ToolCalls[0].Function.Arguments != `{"dev_eui":"0011"}` {
		t.Errorf("call_a = %+v", got.ToolCalls[0])
	}
	if got.ToolCalls[1].Function.Name != "get_device_session" ||
		got.ToolCalls[1].Function.Arguments != `{"dev_eui":"AABB"}` {
		t.Errorf("call_b = %+v", got.ToolCalls[1])
	}
}

// TestStreamSplitsCallsThatReuseIndexZero: Ollama has emitted several tool calls all
// tagged index 0, distinguished only by id. Merging them yields one unexecutable call
// and an error that points at nothing.
func TestStreamSplitsCallsThatReuseIndexZero(t *testing.T) {
	body := event(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a",`+
		`"type":"function","function":{"name":"list_devices","arguments":"{}"}}]}}]}`) +
		event(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_b",`+
			`"type":"function","function":{"name":"network_status","arguments":"{}"}}]}}]}`) +
		event(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`) +
		"data: [DONE]\n\n"

	got, err := sse(t, body).Stream(context.Background(), Request{}, nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if len(got.ToolCalls) != 2 {
		t.Fatalf("got %d calls, want 2 — index 0 was merged: %+v", len(got.ToolCalls), got.ToolCalls)
	}
	if got.ToolCalls[0].Function.Name != "list_devices" ||
		got.ToolCalls[1].Function.Name != "network_status" {
		t.Errorf("names = %q,%q", got.ToolCalls[0].Function.Name, got.ToolCalls[1].Function.Name)
	}
	// Neither call may carry the other's arguments.
	for _, call := range got.ToolCalls {
		if call.Function.Arguments != "{}" {
			t.Errorf("%s carries %q, want only its own arguments", call.Function.Name,
				call.Function.Arguments)
		}
	}
}

func TestStreamToleratesCommentsAndUnparseableFrames(t *testing.T) {
	// A proxy keep-alive and a frame the package has no field for must not cost the
	// operator an answer the model already spent compute producing.
	body := ": keep-alive\n\n" +
		event(`{"choices":[{"delta":{"content":"half "}}]}`) +
		event("this is not json at all") +
		event(`{"choices":[{"delta":{"content":"an answer"}}]}`) +
		"data: [DONE]\n\n"

	got, err := sse(t, body).Stream(context.Background(), Request{}, nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if got.Content != "half an answer" {
		t.Errorf("content = %q, want the readable frames kept", got.Content)
	}
}

func TestStreamHandlesUnspacedDataPrefix(t *testing.T) {
	// The spec permits no space after the colon, and at least one gateway sends it
	// that way. Trimming only "data: " would drop every frame and return an empty
	// answer with no error at all.
	body := "data:{\"choices\":[{\"delta\":{\"content\":\"tight\"}}]}\n\ndata: [DONE]\n\n"
	got, err := sse(t, body).Stream(context.Background(), Request{}, nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if got.Content != "tight" {
		t.Errorf("content = %q, want %q", got.Content, "tight")
	}
}

func TestStreamRefusesANonEventStreamResponse(t *testing.T) {
	client := respondWith(t, http.StatusOK, "application/json",
		`{"choices":[{"message":{"content":"not streamed"}}]}`)
	_, err := client.Stream(context.Background(), Request{}, nil)
	if err == nil || !strings.Contains(err.Error(), "rather than an event stream") {
		t.Errorf("err = %v, want it to name the content type problem", err)
	}
}

// TestStreamConsumerErrorStopsTheRead: the handler's write fails when the browser
// closes, and that must end the generation rather than be swallowed.
func TestStreamConsumerErrorStopsTheRead(t *testing.T) {
	body := event(contentChunk("one ")) + event(contentChunk("two ")) + event(contentChunk("three")) +
		"data: [DONE]\n\n"
	sentinel := errors.New("the browser hung up")

	seen := 0
	_, err := sse(t, body).Stream(context.Background(), Request{}, func(string) error {
		seen++
		if seen == 2 {
			return sentinel
		}
		return nil
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the consumer's error propagated", err)
	}
	if seen != 2 {
		t.Errorf("the callback ran %d times, want it to stop at the first failure", seen)
	}
}

// TestFirstTokenBudgetFiresOnASilentStream: a runtime that accepts the request and
// then says nothing must not hold the connection for the whole total budget.
func TestFirstTokenBudgetFiresOnASilentStream(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if flusher, ok := w.(http.Flusher); ok {
			// A comment frame satisfies the HTTP response without counting as content,
			// which is exactly the case the budget exists for.
			fmt.Fprint(w, ": hi\n\n")
			flusher.Flush()
		}
		<-release
	}))
	t.Cleanup(srv.Close)
	defer close(release)

	client, err := New(Config{
		BaseURL: srv.URL + "/v1", Model: "m",
		FirstTokenTimeout: 150 * time.Millisecond,
		TotalTimeout:      5 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	started := time.Now()
	_, err = client.Stream(context.Background(), Request{}, nil)
	if err == nil {
		t.Fatal("a stream that never produces content must not report success")
	}
	if !strings.Contains(err.Error(), "sent nothing") {
		t.Errorf("err = %v, want it to say the model sent nothing", err)
	}
	// The point of the separate budget is that this returns in ~150ms rather than
	// waiting out the five-second total.
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Errorf("took %v; the first-token budget did not cut the wait short", elapsed)
	}
}

func TestStreamRespectsContextCancellation(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if flusher, ok := w.(http.Flusher); ok {
			fmt.Fprint(w, event(contentChunk("started")))
			flusher.Flush()
		}
		<-release
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	client, err := New(Config{BaseURL: srv.URL + "/v1", Model: "m", TotalTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := client.Stream(ctx, Request{}, nil)
		done <- err
	}()

	time.Sleep(150 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "cancelled") {
			t.Errorf("err = %v, want a cancellation the operator is not told about as a fault", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("cancelling the context did not end the stream")
	}
	close(release)
}

// TestStreamSendsTheStreamingHeadersAndFlags: a runtime that does not see stream:true
// answers with a whole body, and the reader would report an empty completion.
func TestStreamSendsTheStreamingHeadersAndFlags(t *testing.T) {
	var gotAccept string
	var gotStream bool
	var rawBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		raw, _ := readAll(r)
		rawBody = raw
		gotStream = strings.Contains(raw, `"stream":true`)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, event(contentChunk("ok"))+"data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)

	client, err := New(Config{BaseURL: srv.URL + "/v1", Model: "m"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.Stream(context.Background(), Request{}, nil); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if gotAccept != "text/event-stream" {
		t.Errorf("Accept = %q", gotAccept)
	}
	if !gotStream {
		t.Errorf("request body had no stream flag: %s", rawBody)
	}
	// Without this the trailing usage chunk never arrives and cost reporting is
	// silently always zero.
	if !strings.Contains(rawBody, "stream_options") {
		t.Errorf("request body had no stream_options: %s", rawBody)
	}
}
