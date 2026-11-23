package tools

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// advertisedNames returns the tool names a session sees, in the order it sees them.
func advertisedNames(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()
	got, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var names []string
	for _, tool := range got.Tools {
		names = append(names, tool.Name)
	}
	return names
}

// wantTools is the tool surface a model sees, as a set. It is the contract: a name
// in here is something an agent can do, and adding or removing one changes what a
// model can do without any of its own code changing.
//
// Order is not part of this list because the SDK sorts tools/list; that is asserted
// separately below, and it is a stronger guarantee than registration order would be.
var wantTools = []string{
	"diagnose_device",
	"explain_event_reason",
	"get_device",
	"get_device_session",
	"get_integration",
	"get_model",
	"list_devices",
	"list_events",
	"list_integrations",
	"list_models",
	"list_uplinks",
	"network_status",
}

func TestToolsListIsTheAdvertisedSet(t *testing.T) {
	names := advertisedNames(t, newTestServer(t, healthyFake(t)))
	if strings.Join(names, ",") != strings.Join(wantTools, ",") {
		t.Errorf("advertised tools =\n  %v\nwant\n  %v", names, wantTools)
	}
}

// TestToolsListIsSorted pins the determinism the spec asks of tools/list. A client
// caches the list and a model's prompt cache keys on it, so an order that varied
// between processes would quietly invalidate both. The SDK sorts by name, which holds
// regardless of the order Register happens to add tools in.
func TestToolsListIsSorted(t *testing.T) {
	names := advertisedNames(t, newTestServer(t, healthyFake(t)))
	if !slices.IsSorted(names) {
		t.Errorf("tools/list is not sorted by name: %v", names)
	}
}

// TestNoWriteToolIsAdvertised states the design position as a failing test rather
// than a comment. The ns's token authorises device, model and integration writes;
// none of them may be reachable from this build.
func TestNoWriteToolIsAdvertised(t *testing.T) {
	forbidden := []string{
		"create_device", "delete_device", "patch_device", "update_device",
		"enqueue_downlink", "cancel_downlink", "request_device_status",
		"create_model", "update_model", "delete_model", "test_model_decode",
		"create_integration", "update_integration", "delete_integration",
		"test_integration", "update_settings",
	}
	advertised := advertisedNames(t, newTestServer(t, healthyFake(t)))
	for _, name := range forbidden {
		if slices.Contains(advertised, name) {
			t.Errorf("%s is advertised; this build is read-only, and a write tool here "+
				"means a model can change a live network", name)
		}
	}
	// Belt and braces, so a write tool added under a name not listed above still
	// fails: nothing advertised may be named as though it writes.
	for _, name := range advertised {
		for _, verb := range []string{"create_", "delete_", "update_", "send_", "enqueue_", "patch_"} {
			if strings.HasPrefix(name, verb) {
				t.Errorf("%s looks like a write tool", name)
			}
		}
	}
}

// TestEveryToolIsMarkedReadOnly is the machine-checkable half of the read-only
// promise. The other half is which tools exist at all, and that is pinned above.
func TestEveryToolIsMarkedReadOnly(t *testing.T) {
	got, err := newTestServer(t, healthyFake(t)).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	for _, tool := range got.Tools {
		if tool.Annotations == nil {
			t.Errorf("%s: no annotations, so a client cannot tell what this tool does", tool.Name)
			continue
		}
		if !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s: ReadOnlyHint is false in a read-only build", tool.Name)
		}
		if tool.Annotations.Title == "" {
			t.Errorf("%s: no title", tool.Name)
		}
		if tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
			t.Errorf("%s: OpenWorldHint should be false; these tools reach one "+
				"operator-owned endpoint, not an open set of external entities", tool.Name)
		}
		// A thin description is a tool a model will misuse. The descriptions carry
		// what a field name cannot: that battery 255 means "cannot report", that
		// has_codec separates two very different absences.
		if len(tool.Description) < 120 {
			t.Errorf("%s: description is %d characters, too short to carry the semantics "+
				"a model needs to use it correctly", tool.Name, len(tool.Description))
		}
	}
}

// TestToolNamesAreStableAcrossStartup backs the determinism the spec asks of
// tools/list. A client caches the list and a model's prompt cache keys on it, so a
// registration order that varied would quietly invalidate both.
func TestToolNamesAreStableAcrossStartup(t *testing.T) {
	var first []string
	for run := range 5 {
		names := advertisedNames(t, newTestServer(t, healthyFake(t)))
		if run == 0 {
			first = names
			continue
		}
		if strings.Join(names, ",") != strings.Join(first, ",") {
			t.Fatalf("run %d advertised %v, run 0 advertised %v", run, names, first)
		}
	}
}
