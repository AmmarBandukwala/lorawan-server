package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// eventKindConst extracts the kind constants from the network server's vocabulary
// file, which is the file that owns them.
var eventKindConst = regexp.MustCompile(`(?m)^\s*(Event)(\w+)\s*=\s*"([^"]+)"`)

// TestEveryRecordedKindIsNamedInTheToolDescription. eventKinds is a restatement of
// the network server's vocabulary, and a restatement can drift: the ns and this
// server are separate modules, so nothing but this test notices when a ninth kind is
// added and the description still lists eight.
//
// It matters because the description is the only place an agent learns the
// vocabulary. A kind it does not know about is a kind it will never filter on.
func TestEveryRecordedKindIsNamedInTheToolDescription(t *testing.T) {
	declared := declaredKinds(t)

	var missing []string
	for _, kind := range declared {
		if !slices.Contains(eventKinds, kind) {
			missing = append(missing, kind)
		}
	}
	if len(missing) > 0 {
		t.Errorf("the network server records %d kind(s) that list_events does not name: %s\n"+
			"add it to eventKinds in internal/tools/traffic.go so the tool description "+
			"tells a caller it exists", len(missing), strings.Join(missing, ", "))
	}

	var stale []string
	for _, kind := range eventKinds {
		if !slices.Contains(declared, kind) {
			stale = append(stale, kind)
		}
	}
	if len(stale) > 0 {
		t.Errorf("list_events names %d kind(s) the network server does not record: %s — "+
			"offering them invites a filter that always returns nothing",
			len(stale), strings.Join(stale, ", "))
	}
}

// TestKindListActuallyAppearsInTheDescription: the guard above would pass happily if
// eventKinds were correct but the description forgot to interpolate it.
func TestKindListActuallyAppearsInTheDescription(t *testing.T) {
	got, err := newTestServer(t, healthyFake(t)).ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var desc string
	for _, tool := range got.Tools {
		if tool.Name == "list_events" {
			desc = tool.Description
		}
	}
	if desc == "" {
		t.Fatal("list_events was not advertised")
	}
	for _, kind := range eventKinds {
		if !strings.Contains(desc, kind) {
			t.Errorf("the list_events description does not name %q: %s", kind, desc)
		}
	}
}

// TestEventKindsGuardIsNotVacuous, in the specific way this guard could fail: a
// pattern that matched nothing would make both comparisons above pass trivially.
func TestEventKindsGuardIsNotVacuous(t *testing.T) {
	declared := declaredKinds(t)
	if len(declared) < 8 {
		t.Errorf("only %d kinds found in the network server's vocabulary file; it records "+
			"at least eight, so a lower count means the read or the pattern broke", len(declared))
	}
	if !slices.Contains(declared, "uplink_drop") {
		t.Errorf("uplink_drop is not among the kinds found: %v", declared)
	}
}

func declaredKinds(t *testing.T) []string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		candidate := filepath.Join(dir, "ns", "internal", "engine", "events.go")
		if src, err := os.ReadFile(candidate); err == nil {
			var out []string
			for _, m := range eventKindConst.FindAllStringSubmatch(string(src), -1) {
				out = append(out, m[3])
			}
			if len(out) == 0 {
				t.Fatalf("no Event constants found in %s; the file or the pattern changed, "+
					"and a guard that matches nothing guards nothing", candidate)
			}
			return out
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find ns/internal/engine/events.go above the test's " +
				"working directory; if the network server moved, this guard is stale")
		}
		dir = parent
	}
}

// TestFiltersReachTheNetworkServerAsItsOwnQuery: the ns treats an absent dev_eui as
// "every device" and an absent limit as its own default, so this server must not
// invent either. Inventing a default here would put the same number in two modules
// and quietly override the ns's when one of them changed.
func TestFiltersReachTheNetworkServerAsItsOwnQuery(t *testing.T) {
	fake := healthyFake(t)
	cs := newTestServer(t, fake)

	callText(t, cs, "list_events", map[string]any{})
	callText(t, cs, "list_events", map[string]any{"limit": 3})
	callText(t, cs, "list_events", map[string]any{"dev_eui": testEUI, "kind": "uplink_drop"})

	got := fake.queries["/api/v1/events"]
	want := []string{
		"",        // nothing invented
		"limit=3", // only what was asked for
		"dev_eui=0011223344556677&kind=uplink_drop", // normalised, and no limit
	}
	if len(got) != len(want) {
		t.Fatalf("the events endpoint saw %d requests, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("request %d query = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestKindIsNormalisedBeforeItIsSent, because a reason or kind copied out of a JSON
// response may arrive uppercased, and the ns matches exactly.
func TestKindIsNormalisedBeforeItIsSent(t *testing.T) {
	fake := healthyFake(t)
	callText(t, newTestServer(t, fake), "list_events",
		map[string]any{"kind": "  UPLINK_DROP  "})

	got := fake.queries["/api/v1/events"]
	if len(got) != 1 || got[0] != "kind=uplink_drop" {
		t.Errorf("query = %v, want one request with kind=uplink_drop", got)
	}
}
