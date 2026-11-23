package tools

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/fieldnode/lorawan-server/mcp/internal/audit"
	"github.com/fieldnode/lorawan-server/mcp/internal/nsclient"
)

// capture is a concurrency-safe JSON-lines sink. The SDK serves tool calls on their
// own goroutines, so a plain bytes.Buffer here would race and report a failure that
// has nothing to do with the code under test.
type capture struct {
	buf   bytes.Buffer
	mu    sync.Mutex
	lines []map[string]any
}

func (c *capture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.buf.Write(p)
	return len(p), nil
}

// parse happens once per assertion rather than per line written, so the lock guards
// the writer and the reader sees a consistent snapshot.
func (c *capture) find(tool string) []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = nil
	for _, line := range strings.Split(strings.TrimRight(c.buf.String(), "\n"), "\n") {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(line), &parsed); err == nil && parsed["tool"] == tool {
			c.lines = append(c.lines, parsed)
		}
	}
	return c.lines
}

func (c *capture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	trimmed := strings.TrimRight(c.buf.String(), "\n")
	if trimmed == "" {
		return 0
	}
	return len(strings.Split(trimmed, "\n"))
}

func (c *capture) dump() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

// TestAuditNeverRecordsTheTokenOrThePayload. A downlink payload is application data
// and the log line outlives the conversation it came from; a bearer token in a log
// file is a credential on disk.
func TestAuditNeverRecordsTheTokenOrThePayload(t *testing.T) {
	deps, sink := auditDeps(t, healthyFake(t))
	cs := newServerWithDeps(t, deps, ProfileOperate)

	_, _ = callText(t, cs, "enqueue_downlink", map[string]any{
		"dev_eui": testEUI, "data": "DEADBEEF", "f_port": 10, "reason": "a stated purpose",
	})

	line := sink.dump()
	for _, forbidden := range []string{testToken, "DEADBEEF", "deadbeef"} {
		if strings.Contains(line, forbidden) {
			t.Errorf("the audit log contains %q: %s", forbidden, line)
		}
	}
	if !strings.Contains(line, "enqueue_downlink") {
		t.Errorf("nothing was audited at all: %q", line)
	}
}

// TestReadsAreNotAuditedAtInfo keeps the default log readable. A chatty read log
// buries the writes, which are the entries anyone actually searches for.
func TestReadsAreNotAuditedAtInfo(t *testing.T) {
	fake := healthyFake(t)
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	client, err := nsclient.New(srv.URL, testToken)
	if err != nil {
		t.Fatalf("nsclient.New: %v", err)
	}
	sink := &capture{}
	deps := &Deps{
		NS: client,
		Audit: audit.New(slog.New(slog.NewJSONHandler(sink,
			&slog.HandlerOptions{Level: slog.LevelInfo}))),
	}
	cs := newServerWithDeps(t, deps, ProfileRead)

	_, _ = callText(t, cs, "get_device", map[string]any{"dev_eui": testEUI})
	_, _ = callText(t, cs, "network_status", map[string]any{})

	if got := sink.count(); got != 0 {
		t.Errorf("reads produced %d Info-level audit line(s):\n%s", got, sink.dump())
	}
}

// TestReadsAreAuditedAtDebug: the level is what makes read auditing affordable, but if
// nothing emitted them at all the claim would be decoration. "What did it look at
// before it said that" has to be answerable when someone turns the level down.
func TestReadsAreAuditedAtDebug(t *testing.T) {
	deps, sink := auditDeps(t, healthyFake(t)) // auditDeps captures at Debug
	cs := newServerWithDeps(t, deps, ProfileRead)

	_, _ = callText(t, cs, "get_device", map[string]any{"dev_eui": testEUI})

	entries := sink.find("get_device")
	if len(entries) != 1 {
		t.Fatalf("a read recorded %d entries at Debug, want 1:\n%s", len(entries), sink.dump())
	}
	if entries[0]["dev_eui"] != testEUI {
		t.Errorf("dev_eui = %v, want the device that was read", entries[0]["dev_eui"])
	}
	if entries[0]["outcome"] != "ok" {
		t.Errorf("outcome = %v", entries[0]["outcome"])
	}
}

// TestNilAuditLoggerIsHarmless: auditing is optional, and a write must not panic when
// it is switched off.
func TestNilAuditLoggerIsHarmless(t *testing.T) {
	cs := newOperateServer(t, healthyFake(t)) // Deps.Audit is nil here
	_, res := callText(t, cs, "cancel_downlink", map[string]any{
		"dev_eui": testEUI, "reason": "the application changed its mind",
	})
	if res.IsError {
		t.Errorf("a write without an audit logger failed: %v", res)
	}
}

// auditDeps wires a Deps whose audit lines land in the capture.
func auditDeps(t *testing.T, fake *fakeNS) (*Deps, *capture) {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	client, err := nsclient.New(srv.URL, testToken)
	if err != nil {
		t.Fatalf("nsclient.New: %v", err)
	}
	sink := &capture{}
	return &Deps{
		NS: client,
		Audit: audit.New(slog.New(slog.NewJSONHandler(sink,
			&slog.HandlerOptions{Level: slog.LevelDebug}))),
	}, sink
}

// TestWritesAreAuditedOnSuccess: the record has to name the device, the outcome and
// the caller's stated reason, because that is what someone reads afterwards to work
// out who did this.
func TestWritesAreAuditedOnSuccess(t *testing.T) {
	deps, sink := auditDeps(t, healthyFake(t))
	cs := newServerWithDeps(t, deps, ProfileOperate)

	_, res := callText(t, cs, "enqueue_downlink", map[string]any{
		"dev_eui": testEUI, "data": "0102", "f_port": 10,
		"reason": "close the irrigation valve",
	})
	if res.IsError {
		t.Fatalf("the call failed: %v", res)
	}

	entries := sink.find("enqueue_downlink")
	if len(entries) != 1 {
		t.Fatalf("audit recorded %d entries, want exactly 1", len(entries))
	}
	got := entries[0]
	if got["dev_eui"] != testEUI {
		t.Errorf("dev_eui = %v", got["dev_eui"])
	}
	if got["outcome"] != "ok" {
		t.Errorf("outcome = %v, want ok", got["outcome"])
	}
	if got["reason"] != "close the irrigation valve" {
		t.Errorf("reason = %v, want the caller's own sentence", got["reason"])
	}
	if got["status"] != float64(http.StatusAccepted) {
		t.Errorf("status = %v, want 202", got["status"])
	}
	if got["args"] == "" || got["args"] == nil {
		t.Error("the argument digest is missing, so two calls cannot be correlated")
	}
}

// TestRefusedWritesAreAuditedToo: the question at 2am is usually "what did it try to
// do", not only "what did it manage".
func TestRefusedWritesAreAuditedToo(t *testing.T) {
	fake := healthyFake(t)
	path := "/api/v1/devices/" + testEUI + "/downlink"
	fake.statuses[path] = http.StatusConflict
	fake.responses[path] = bodyConflict

	deps, sink := auditDeps(t, fake)
	cs := newServerWithDeps(t, deps, ProfileOperate)

	_, res := callText(t, cs, "enqueue_downlink", map[string]any{
		"dev_eui": testEUI, "data": "0102", "f_port": 10, "reason": "a second attempt",
	})
	if !res.IsError {
		t.Fatal("a 409 should surface as a tool error")
	}

	entries := sink.find("enqueue_downlink")
	if len(entries) != 1 {
		t.Fatalf("audit recorded %d entries, want 1 even for a refusal", len(entries))
	}
	if entries[0]["outcome"] != "conflict" {
		t.Errorf("outcome = %v, want conflict", entries[0]["outcome"])
	}
	if entries[0]["status"] != float64(http.StatusConflict) {
		t.Errorf("status = %v, want 409", entries[0]["status"])
	}
}
