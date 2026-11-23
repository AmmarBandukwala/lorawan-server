package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestOpenRequiresTheToken(t *testing.T) {
	endpoint, seen := fakeListener(t)

	// A wrong token must fail at Open rather than producing a session that cannot list
	// anything, which the console would then report as "this network has no tools".
	client, err := New(Config{URL: endpoint, Token: "wrong-token-value-123456"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	session, err := client.Open(context.Background())
	if err == nil {
		_ = session.Close()
		t.Fatal("a wrong token opened a session")
	}
	if *seen == 0 {
		t.Error("no request reached the listener, so nothing was really tested")
	}
}

func TestTokenReachesEveryRequest(t *testing.T) {
	endpoint, seen := fakeListener(t)
	session := mustOpen(t, endpoint, seen)
	defer session.Close()

	if _, err := session.Tools(context.Background()); err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if _, err := session.Call(context.Background(), "network_status", nil); err != nil {
		t.Fatalf("Call: %v", err)
	}
	// initialize, tools/list and tools/call at minimum. If the round-tripper only
	// covered the first request, the later ones would 401 and a test that ignored the
	// error would still pass.
	if *seen < 3 {
		t.Errorf("the listener saw %d requests, want at least 3", *seen)
	}
}

func TestToolsReportWhichOnesAreReadOnly(t *testing.T) {
	endpoint, seen := fakeListener(t)
	session := mustOpen(t, endpoint, seen)
	defer session.Close()

	tools, err := session.Tools(context.Background())
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if len(tools) != 3 {
		t.Fatalf("got %d tools, want 3: %v", len(tools), names(tools))
	}

	byName := map[string]Tool{}
	for _, tool := range tools {
		byName[tool.Name] = tool
	}
	// The read-only claim must survive the trip. The assistant filters on it, so a
	// write arriving marked read-only is a write an operator never approved.
	if !byName["network_status"].ReadOnly {
		t.Error("network_status lost its read-only annotation")
	}
	if byName["enqueue_downlink"].ReadOnly {
		t.Error("enqueue_downlink arrived read-only; the assistant would offer it as a read")
	}
	if byName["get_device"].Description == "" {
		t.Error("the description did not survive the trip")
	}

	// The schema must arrive as an object schema naming its property, or the model
	// invents argument names and every call fails for the wrong reason.
	var schema struct {
		Type       string                    `json:"type"`
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(byName["get_device"].Schema, &schema); err != nil {
		t.Fatalf("schema is not JSON: %s", byName["get_device"].Schema)
	}
	if schema.Type != "object" {
		t.Errorf("schema type = %q, want object", schema.Type)
	}
	if _, ok := schema.Properties["dev_eui"]; !ok {
		t.Errorf("schema lost its property: %s", byName["get_device"].Schema)
	}
}

func TestCallReturnsTheToolText(t *testing.T) {
	endpoint, seen := fakeListener(t)
	session := mustOpen(t, endpoint, seen)
	defer session.Close()

	got, err := session.Call(context.Background(), "network_status", nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !strings.Contains(got, `"region":"EU868"`) {
		t.Errorf("got %q", got)
	}
}

// TestToolRefusalIsTyped: the listener's refusal is the model's chance to correct
// itself, so it must arrive intact and distinguishable from a broken connection.
func TestToolRefusalIsTyped(t *testing.T) {
	endpoint, seen := fakeListener(t)
	session := mustOpen(t, endpoint, seen)
	defer session.Close()

	_, err := session.Call(context.Background(), "get_device",
		json.RawMessage(`{"dev_eui":"AABBCCDDEEFF0011"}`))
	if err == nil {
		t.Fatal("want the refusal, got nil")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("wrong type %T: %v", err, err)
	}
	if toolErr.Name != "get_device" {
		t.Errorf("Name = %q", toolErr.Name)
	}
	// Verbatim, not wrapped: the sentence is written for the model to read.
	if toolErr.Detail != "dev_eui AABBCCDDEEFF0011 is not provisioned" {
		t.Errorf("Detail = %q", toolErr.Detail)
	}
}

// TestCallWithNoArgumentsSendsAnEmptyObject: a nil argument list must not reach the
// listener as null, because the tools take an object and a strict validator would
// reject null outright.
func TestCallWithNoArgumentsSendsAnEmptyObject(t *testing.T) {
	endpoint, seen := fakeListener(t)
	session := mustOpen(t, endpoint, seen)
	defer session.Close()

	if _, err := session.Call(context.Background(), "network_status", nil); err != nil {
		t.Fatalf("Call with nil args: %v", err)
	}
}

func TestReadReturnsResourceText(t *testing.T) {
	endpoint, seen := fakeListener(t)
	session := mustOpen(t, endpoint, seen)
	defer session.Close()

	got, err := session.Read(context.Background(), "fieldnode://event-vocabulary")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !strings.Contains(got, "f_cnt_replay") {
		t.Errorf("got %q", got)
	}
}

func TestReadOfAnUnknownResourceFails(t *testing.T) {
	endpoint, seen := fakeListener(t)
	session := mustOpen(t, endpoint, seen)
	defer session.Close()

	if _, err := session.Read(context.Background(), "fieldnode://nope"); err == nil {
		t.Error("reading a resource that does not exist must not report success")
	}
}

func TestOpenAgainstADeadListenerSaysWhere(t *testing.T) {
	client, err := New(Config{URL: "http://127.0.0.1:1/mcp", Token: testToken})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.Open(context.Background()); err == nil {
		t.Fatal("want an error, got nil")
	} else if !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Errorf("err = %q, want it to name the endpoint it tried", err)
	}
}

func names(tools []Tool) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		out = append(out, tool.Name)
	}
	return out
}
