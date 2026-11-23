package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fieldnode/lorawan-server/mcp/internal/audit"
	"github.com/fieldnode/lorawan-server/mcp/internal/nsclient"
	"github.com/fieldnode/lorawan-server/mcp/internal/redact"
)

// write is the shared body of every write tool: call, audit, return.
//
// The audit line is written for both outcomes, because a refused write is exactly as
// interesting as an accepted one — the question at 2am is usually "what did it try to
// do", not only "what did it manage".
func (d *Deps) write(ctx context.Context, tool string, in any, devEUI, reason string,
	call func() (int, json.RawMessage, error),
) (*mcp.CallToolResult, any, error) {

	started := time.Now()
	status, raw, err := call()

	outcome := "ok"
	if err != nil {
		outcome = outcomeOf(err)
	}
	d.Audit.Write(audit.Entry{
		Tool:       tool,
		DevEUI:     devEUI,
		ArgsDigest: audit.Digest(in),
		Reason:     reason,
		Outcome:    outcome,
		Status:     status,
		Duration:   time.Since(started),
	})
	if err != nil {
		return nil, nil, err
	}
	return text(string(redact.Body(raw))), nil, nil
}

// outcomeOf is a short class, never the error text: an upstream's message can carry
// anything, including a connection string, and a log line is not where that belongs.
func outcomeOf(err error) string {
	if nsclient.ConflictError(err) {
		return "conflict"
	}
	var nf *nsclient.NotFoundError
	var au *nsclient.AuthError
	var api *nsclient.APIError
	switch {
	case errors.As(err, &nf):
		return "not_found"
	case errors.As(err, &au):
		return "unauthorized"
	case errors.As(err, &api):
		return fmt.Sprintf("upstream_%d", api.Status)
	default:
		return "unreachable"
	}
}

// writeAnnotations builds the annotation block for a write.
//
// OpenWorldHint is true here and false on every read: a write reaches a radio and a
// physical device, which is as open as this world gets.
//
// destructiveHint is fixed when the tool is registered, so it cannot describe one
// particular call. That rules out flagging patch_device only when enabled:false
// arrives; the tool *can* silence a device, so it is marked destructive always, which
// is what "may perform destructive updates" actually means.
func writeAnnotations(title string, destructive, idempotent bool) *mcp.ToolAnnotations {
	open := true
	return &mcp.ToolAnnotations{
		Title:           title,
		ReadOnlyHint:    false,
		DestructiveHint: ptr(destructive),
		IdempotentHint:  idempotent,
		OpenWorldHint:   &open,
	}
}

// requireReason insists a caller say what it is about to do.
//
// It is not access control — nothing in MCP is — but the sentence lands in the host's
// approval prompt, where a human reads it before deciding, and in the audit log after.
// A write that cannot be explained is a write that should not be attempted.
func requireReason(raw string) (string, error) {
	reason := strings.TrimSpace(raw)
	if reason == "" {
		return "", fmt.Errorf("reason is required: a human is being asked to approve this, " +
			"and they can only do that if you say why")
	}
	return reason, nil
}
