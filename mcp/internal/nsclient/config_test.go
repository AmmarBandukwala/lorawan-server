package nsclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestNewRejectsUnusableConfiguration(t *testing.T) {
	const token = "0123456789abcdef0123456789"
	cases := map[string][2]string{
		"a relative URL":        {"/api/v1", token},
		"a host with no scheme": {"127.0.0.1:8091", token},
		"an empty token":        {"http://127.0.0.1:8091", ""},
		"an empty URL":          {"", token},
	}
	for name, args := range cases {
		if _, err := New(args[0], args[1]); err == nil {
			t.Errorf("%s: New accepted it", name)
		}
	}
	if _, err := New("http://127.0.0.1:8091/", token); err != nil {
		t.Errorf("a trailing slash should be tolerated: %v", err)
	}
}

// TestWithStatusURLIgnoresEmpty lets main pass an unset environment variable
// straight through instead of branching on it at the call site.
func TestWithStatusURLIgnoresEmpty(t *testing.T) {
	c, err := New("http://127.0.0.1:8091", "0123456789abcdef0123456789", WithStatusURL("  "))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.statusURL != "http://127.0.0.1:8091" {
		t.Errorf("statusURL = %q, want it to fall back to the API base", c.statusURL)
	}
}

// TestUnreachableServerSaysWhere: "cannot reach the network server" plus the
// address is the one diagnosis an agent cannot derive for itself.
func TestUnreachableServerSaysWhere(t *testing.T) {
	c, err := New("http://127.0.0.1:1", "0123456789abcdef0123456789")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = c.ListDevices(context.Background())
	if err == nil {
		t.Fatal("want an error, got nil")
	}
	if !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Errorf("error = %q, want it to name the address it tried", err)
	}
}

func TestContextCancellationReachesTheRequest(t *testing.T) {
	c := newRecorderClient(t, &recorder{body: `{}`})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.ListDevices(ctx); err == nil {
		t.Error("a cancelled context must not report success")
	}
}

// TestRawMessageSurvivesMarshaling keeps the passthrough honest: what the client
// returns has to be embeddable in a response body unchanged.
func TestRawMessageSurvivesMarshaling(t *testing.T) {
	const body = `{"events":[]}`
	c := newRecorderClient(t, &recorder{body: body})
	raw, err := c.ListEvents(context.Background(), "", "", 0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	out, err := json.Marshal(map[string]json.RawMessage{"result": raw})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if want := fmt.Sprintf(`{"result":%s}`, body); string(out) != want {
		t.Errorf("marshal = %s, want %s", out, want)
	}
}
