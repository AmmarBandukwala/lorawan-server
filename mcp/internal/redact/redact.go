// Package redact guards what upstream bytes reach a model's context.
//
// Two separate problems, one package, because both are about the same act: deciding
// what leaves this process.
//
// The first is credential scrubbing. The network server never returns device keys —
// that is a property of its handlers, not of this client — so this is defence against
// what sits *between* the two processes: a proxy's error page echoing a request body, a
// test harness that dumps a store row, a future endpoint that leaks. Cheap to do and
// invisible when there is nothing to redact, which is the common case.
//
// The second is size. The device list is an unbounded query on the network server side;
// it has no paging. A 10,000-device fleet would otherwise be pulled into a single tool
// result and dominate the rest of the conversation. Truncating bytes would produce
// invalid JSON, so this truncates the *array* and says so loudly.
package redact

import (
	"encoding/json"
	"fmt"
	"strings"
)

// secretKeys are the JSON member names whose values must never reach a model.
//
// Matched case-insensitively and by substring, deliberately loosely: the point is not
// to identify a field precisely but to make it very hard for a key to pass through.
// Over-matching costs a redaction marker on something harmless; under-matching costs a
// credential in a transcript.
var secretKeys = []string{
	"app_key", "appkey", "nwk_s_key", "nwkskey", "app_s_key", "appskey",
	"session_key", "root_key", "token", "password", "secret",
}

// Marker replaces a value that looked like a credential.
const Marker = "[redacted]"

// Body scrubs credential-looking values from an upstream JSON body.
//
// Anything that is not valid JSON is returned unchanged: this is a guard on structured
// responses, not a general text filter, and mangling a proxy's HTML error page would
// destroy the only diagnostic information in it.
func Body(raw []byte) []byte {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
		return raw
	}
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return raw
	}
	scrubbed, changed := scrub(parsed)
	if !changed {
		return raw
	}
	out, err := json.Marshal(scrubbed)
	if err != nil {
		// Unlikely after a successful unmarshal, but a guard must not become the
		// thing that breaks the response.
		return raw
	}
	return out
}

func scrub(v any) (any, bool) {
	switch node := v.(type) {
	case map[string]any:
		changed := false
		for key, child := range node {
			if looksSecret(key) {
				if textOf(child) != "" {
					node[key] = Marker
					changed = true
				}
				continue
			}
			scrubbed, sub := scrub(child)
			node[key] = scrubbed
			changed = changed || sub
		}
		return node, changed
	case []any:
		changed := false
		for i, child := range node {
			scrubbed, sub := scrub(child)
			node[i] = scrubbed
			changed = changed || sub
		}
		return node, changed
	default:
		return v, false
	}
}

func looksSecret(key string) bool {
	lower := strings.ToLower(key)
	for _, s := range secretKeys {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

// MaxListItems bounds a list tool result. The device inventory is the read with no
// paging on the network server side, so this is the one place a large fleet can be
// stopped before it becomes the largest thing in the conversation.
const MaxListItems = 200

// CapList keeps the first n members of a JSON array and appends an explicit notice.
//
// The notice is a member of the array rather than prose outside it, so a caller
// iterating the result encounters it as data instead of something that looks like a
// device. Silently shortening a list is the failure being avoided: an agent that
// counted 200 devices would report a fleet of 200.
//
// A body that is not a JSON array is returned unchanged — the wrapped shapes
// ({"events": [...]}) are the network server's list endpoints with a limit of their
// own, and rewriting them here would invent a rule this server does not own.
func CapList(raw []byte, n int) []byte {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed[0] != '[' {
		return raw
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return raw
	}
	if len(items) <= n {
		return raw
	}

	kept := items[:n]
	notice, err := json.Marshal(map[string]any{
		"truncated": true,
		"shown":     n,
		"total":     len(items),
		"note": fmt.Sprintf("%d of %d items are shown. This server caps a list at %d to "+
			"protect the conversation; the network server has no paging. Count from total, "+
			"not from what is here, and narrow with a filter rather than assuming the rest "+
			"does not exist.", n, len(items), n),
	})
	if err != nil {
		return raw
	}
	out, err := json.Marshal(append(kept, notice))
	if err != nil {
		return raw
	}
	return out
}

// textOf treats an empty or null member as nothing to redact, so a response that
// legitimately carries "app_key": null is not rewritten and does not look altered.
func textOf(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		return fmt.Sprint(t)
	}
}
