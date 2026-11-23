// Package audit records what this server did, not what it read.
//
// The distinction is deliberate. A read-only tool call is recoverable and mostly
// uninteresting; a write changed a live network and cannot be undone by the caller.
// So writes are logged at Info on every call and reads at Debug, which keeps the
// default log readable without losing the record anyone needs at 2am.
//
// What is deliberately absent: the bearer token, and the arguments of a write beyond a
// digest. A downlink payload is application data an operator may not want in a log
// line, and a hash still lets you correlate two entries without storing the content.
// The one argument recorded verbatim is the caller's stated reason, because that is
// the sentence a human should be reading when they decide whether to approve the call.
package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Entry is one recorded call.
type Entry struct {
	// Tool is the tool name as advertised in tools/list.
	Tool string
	// DevEUI is the device the call named, when it named one. Recorded in full:
	// an identifier is not a secret, and "which device" is the first question asked.
	DevEUI string
	// ArgsDigest correlates two calls without storing their content. Twelve hex
	// characters is far past the collision point for one process's lifetime.
	ArgsDigest string
	// Reason is the caller's own stated purpose, for writes that ask for one.
	Reason string
	// Outcome is "ok" or a short error class, never a full error message: an
	// upstream's text can carry anything, including a connection string.
	Outcome string
	// Status is the upstream HTTP status, 0 when the call never reached it.
	Status int
	// Duration is how long the upstream took.
	Duration time.Duration
}

// Logger writes audit entries. A nil *Logger is valid and does nothing, so tests and
// any caller that does not care pay nothing and no call site needs a nil check.
type Logger struct {
	out *slog.Logger
}

// New returns a logger writing to out. A nil out disables auditing.
func New(out *slog.Logger) *Logger {
	if out == nil {
		return nil
	}
	return &Logger{out: out}
}

// Digest returns the correlation tag for a value. It is computed over the canonical
// JSON encoding, so two calls that differ only in argument order hash differently —
// which is correct, because for a patch, order of presence is the whole meaning.
func Digest(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		// An unmarshalable argument is a programming error, and a digest of the type
		// name is enough to still correlate the entries.
		raw = []byte(fmt.Sprintf("%T", v))
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])[:12]
}

// Write records a write. Always at Info: this is the record of something that changed.
func (l *Logger) Write(e Entry) {
	if l == nil {
		return
	}
	l.out.Info("mcp write", e.attrs()...)
}

// Read records a read, at Debug, so the default level stays readable.
func (l *Logger) Read(e Entry) {
	if l == nil {
		return
	}
	l.out.Debug("mcp read", e.attrs()...)
}

func (e Entry) attrs() []any {
	out := []any{
		"tool", e.Tool,
		"outcome", orNone(e.Outcome),
		"duration_ms", e.Duration.Milliseconds(),
	}
	if e.DevEUI != "" {
		out = append(out, "dev_eui", e.DevEUI)
	}
	if e.ArgsDigest != "" {
		out = append(out, "args", e.ArgsDigest)
	}
	if e.Status != 0 {
		out = append(out, "status", e.Status)
	}
	if r := oneLine(e.Reason); r != "" {
		out = append(out, "reason", r)
	}
	return out
}

// oneLine flattens a caller-supplied string. It is free text from a model, so it must
// not be able to forge extra log lines or pad the record past usefulness.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
