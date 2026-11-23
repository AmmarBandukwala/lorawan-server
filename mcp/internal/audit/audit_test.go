package audit

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func newCapture() (*Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return New(slog.New(slog.NewJSONHandler(buf, nil))), buf
}

func TestNilLoggerIsSilent(t *testing.T) {
	var l *Logger
	l.Write(Entry{Tool: "patch_device", Outcome: "ok"})
	l.Read(Entry{Tool: "get_device", Outcome: "ok"})
}

func TestNewWithNoOutputDisablesAuditing(t *testing.T) {
	if got := New(nil); got != nil {
		t.Errorf("New(nil) = %v, want a logger that does nothing", got)
	}
}

func TestWriteRecordsTheFieldsThatMakeItUseful(t *testing.T) {
	logger, buf := newCapture()
	logger.Write(Entry{
		Tool: "enqueue_downlink", DevEUI: "0011223344556677", ArgsDigest: "abcdef123456",
		Reason: "close the valve", Outcome: "ok", Status: 202, Duration: 12 * time.Millisecond,
	})

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("the entry is not JSON: %v (%s)", err, buf)
	}
	for key, want := range map[string]any{
		"tool": "enqueue_downlink", "dev_eui": "0011223344556677",
		"args": "abcdef123456", "reason": "close the valve",
		"outcome": "ok", "status": float64(202), "duration_ms": float64(12),
	} {
		if got[key] != want {
			t.Errorf("%s = %v, want %v", key, got[key], want)
		}
	}
}

// TestReasonCannotForgeLogLines. The reason is free text from a model. A newline in it
// must not produce a second entry, and a crafted value must not add attributes — a log
// that can be appended to by the thing being audited is not an audit log.
func TestReasonCannotForgeLogLines(t *testing.T) {
	logger, buf := newCapture()
	logger.Write(Entry{
		Tool:   "patch_device",
		Reason: "rename\n{\"level\":\"INFO\",\"msg\":\"forged entry\",\"tool\":\"delete_device\"}",
	})

	out := buf.String()
	if lines := strings.Split(strings.TrimRight(out, "\n"), "\n"); len(lines) != 1 {
		t.Fatalf("the reason produced %d log lines, want 1:\n%s", len(lines), out)
	}
	if strings.Contains(out, "delete_device") && strings.Contains(out, `"tool":"delete_device"`) {
		t.Errorf("the reason injected an attribute:\n%s", out)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("the entry is not JSON: %v", err)
	}
	if !strings.Contains(got["reason"].(string), "forged entry") {
		t.Errorf("the reason was dropped rather than flattened: %v", got["reason"])
	}
	if strings.Contains(got["reason"].(string), "\n") {
		t.Errorf("the reason still contains a newline: %q", got["reason"])
	}
}

// TestReasonIsBounded so one call cannot pad the log past usefulness.
func TestReasonIsBounded(t *testing.T) {
	logger, buf := newCapture()
	logger.Write(Entry{Tool: "patch_device", Reason: strings.Repeat("x", 5000)})

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if n := len(got["reason"].(string)); n > 210 {
		t.Errorf("reason is %d characters, want it truncated near 200", n)
	}
}

// TestReadsAreDebugAndWritesAreInfo: the default level must show writes and hide reads,
// or the entries anyone searches for are buried under inventory polling.
func TestReadsAreDebugAndWritesAreInfo(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := New(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))

	logger.Read(Entry{Tool: "get_device", Outcome: "ok"})
	if buf.Len() != 0 {
		t.Errorf("a read was logged at Info: %s", buf)
	}

	logger.Write(Entry{Tool: "patch_device", Outcome: "ok"})
	if buf.Len() == 0 {
		t.Error("a write was not logged at Info")
	}
}

func TestOutcomeDefaultsToNoneRatherThanAnEmptyField(t *testing.T) {
	logger, buf := newCapture()
	logger.Write(Entry{Tool: "get_device"})

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["outcome"] != "none" {
		t.Errorf("outcome = %v, want none", got["outcome"])
	}
	// Absent optional fields are omitted, not emitted empty, so a reader can tell
	// "no device was named" from a device named "".
	if _, present := got["dev_eui"]; present {
		t.Errorf("dev_eui was emitted for a call that named no device: %v", got)
	}
	if _, present := got["status"]; present {
		t.Errorf("status was emitted for a call that never reached the server: %v", got)
	}
}

// TestDigestIsStableAndArgumentSpecific: two identical calls must correlate, and two
// different ones must not, or the digest is decoration.
func TestDigestIsStableAndArgumentSpecific(t *testing.T) {
	a := Digest(map[string]any{"dev_eui": "0011223344556677", "enabled": false})
	b := Digest(map[string]any{"dev_eui": "0011223344556677", "enabled": false})
	c := Digest(map[string]any{"dev_eui": "0011223344556677", "enabled": true})

	if a != b {
		t.Errorf("the same arguments hashed differently: %s vs %s", a, b)
	}
	if a == c {
		t.Errorf("different arguments hashed the same: %s", a)
	}
	if len(a) != 12 {
		t.Errorf("digest length = %d, want 12", len(a))
	}
}

// TestDigestDoesNotLeakTheArguments: it is a correlation tag, not a copy.
func TestDigestDoesNotLeakTheArguments(t *testing.T) {
	got := Digest(map[string]any{"data": "DEADBEEFCAFE"})
	if strings.Contains(strings.ToUpper(got), "DEAD") {
		t.Errorf("the digest appears to contain the payload: %s", got)
	}
}

// TestDigestSurvivesAnUnmarshalableValue rather than panicking, because a digest is
// never worth losing a write over.
func TestDigestSurvivesAnUnmarshalableValue(t *testing.T) {
	got := Digest(func() {})
	if len(got) != 12 {
		t.Errorf("digest = %q, want a 12-character tag", got)
	}
}
