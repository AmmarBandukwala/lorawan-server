package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fieldnode/lorawan-server/mcp/internal/nsclient"
)

// The console's Agents page carries a table of these tools, with a read/write badge on
// each. That table lives in a TypeScript file this module cannot import: the console
// and the MCP server share no code, so a renamed tool is not a compile error and not a
// test failure in either language. It is a page that confidently tells an operator an
// agent can do something the server no longer offers — or, far worse, describes a
// write as a read.
//
// So the guard reads the file as text, the same way the vocabulary and route guards do.

// consoleConstantsPath is the file the Agents page builds its table from, relative to
// the repository root.
const consoleConstantsPath = "src/features/network/constants.ts"

func TestConsoleListsExactlyTheToolsThisServerRegisters(t *testing.T) {
	text := readConsoleConstants(t)
	advertised := registeredToolNames(t, ProfileOperate)
	claimed := consoleClaimedTools(t, text)

	var missing []string
	for _, name := range advertised {
		if !slices.Contains(claimed, name) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Errorf("%s does not list %d tool(s) this server registers: %s.\n"+
			"The Agents page is where an operator learns what an agent can do; a tool "+
			"absent from it is a capability nobody knows they have.",
			consoleConstantsPath, len(missing), strings.Join(missing, ", "))
	}

	var stale []string
	for _, name := range claimed {
		if !slices.Contains(advertised, name) {
			stale = append(stale, name)
		}
	}
	if len(stale) > 0 {
		t.Errorf("%s lists %d tool(s) this server does not register: %s.\n"+
			"The page would be advertising something an agent cannot call.",
			consoleConstantsPath, len(stale), strings.Join(stale, ", "))
	}
}

// TestConsoleMarksEveryWriteAsAWrite is the half that matters for safety.
//
// A write mislabelled as a read is not a cosmetic error: the page's entire purpose is
// letting an operator decide what to point an agent at, and a row reading "Read only"
// beside a tool that reconfigures a live device is the console telling them the
// opposite of the truth about the one action they were checking.
func TestConsoleMarksEveryWriteAsAWrite(t *testing.T) {
	text := readConsoleConstants(t)
	writes := WriteTools(ProfileOperate)
	if len(writes) == 0 {
		t.Fatal("no write tools in the operate profile, so this test would pass against " +
			"any change to the console file. That is not a guard.")
	}

	for _, name := range writes {
		line := consoleToolLine(t, text, name)
		if !strings.Contains(line, "writes: true") {
			t.Errorf("%s marks %s as not writing (%s).\nIt is a write tool in the "+
				"operate profile.", consoleConstantsPath, name, strings.TrimSpace(line))
		}
	}

	for _, name := range registeredToolNames(t, ProfileRead) {
		line := consoleToolLine(t, text, name)
		if !strings.Contains(line, "writes: false") {
			t.Errorf("%s does not mark the read tool %s with writes: false (%s).",
				consoleConstantsPath, name, strings.TrimSpace(line))
		}
	}
}

// TestConsoleNeverAllowedListIsStillTrue guards the "offered by no profile" list.
//
// Each entry names a capability in prose rather than a tool name, so this checks the
// tools that capability would need. If one ever gets registered, the page is telling
// operators something that has stopped being true, and the whole point of the list is
// that it can be trusted without going to look.
func TestConsoleNeverAllowedListIsStillTrue(t *testing.T) {
	advertised := registeredToolNames(t, ProfileOperate)

	// The names provisioning, integration writes and formatter installation would
	// need. They are absent by design; their appearance is the drift this catches.
	for _, forbidden := range []string{
		"create_device", "delete_device", "provision_device",
		"create_integration", "update_integration", "delete_integration",
		"create_model", "update_model", "delete_model", "save_model",
	} {
		if slices.Contains(advertised, forbidden) {
			t.Errorf("%q is now registered, but %s still lists its capability under "+
				"\"offered by no profile\". Either remove the tool or rewrite the page.",
				forbidden, consoleConstantsPath)
		}
	}
}

// --- helpers -----------------------------------------------------------------

// registeredToolNames asks Register itself what it registered, rather than trusting a
// list maintained beside the registrations.
func registeredToolNames(t *testing.T, profile Profile) []string {
	t.Helper()
	// The client is never called: this asserts on registration, not on traffic. The
	// address is unroutable so an accidental request fails loudly rather than
	// resolving to something real.
	client, err := nsclient.New("http://127.0.0.1:1", "0123456789abcdef0123456789")
	if err != nil {
		t.Fatalf("nsclient.New: %v", err)
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "drift-guard", Version: "test"}, nil)
	names, err := Register(server, &Deps{NS: client}, profile)
	if err != nil {
		t.Fatalf("Register(%q): %v", profile, err)
	}
	if len(names) == 0 {
		t.Fatalf("Register(%q) registered nothing, so this guard would pass against "+
			"any change to the console file.", profile)
	}
	return names
}

func readConsoleConstants(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(consoleRepoRoot(t), consoleConstantsPath))
	if err != nil {
		t.Fatalf("read %s: %v", consoleConstantsPath, err)
	}
	text := string(raw)
	// Asserted before anything is parsed: a guard whose pattern matches nothing passes
	// against every future change, which is the property that makes it worth writing.
	if !strings.Contains(text, "AGENT_TOOLS") {
		t.Fatalf("%s no longer declares AGENT_TOOLS, so this guard is reading nothing. "+
			"Update it to wherever the Agents page gets its table now.", consoleConstantsPath)
	}
	return text
}

// consoleToolBlock returns the body of the AGENT_TOOLS array.
//
// Scoped rather than searched across the whole file because constants.ts holds other
// object literals, and a match in one of them would either invent a tool or hide a
// real one.
func consoleToolBlock(t *testing.T, text string) string {
	t.Helper()
	start := strings.Index(text, "AGENT_TOOLS")
	if start < 0 {
		t.Fatal("AGENT_TOOLS not found")
	}
	open := strings.Index(text[start:], "[")
	if open < 0 {
		t.Fatal("no opening bracket after AGENT_TOOLS")
	}
	body := text[start+open:]
	end := strings.Index(body, "\n]")
	if end < 0 {
		t.Fatal("no closing bracket for the AGENT_TOOLS array")
	}
	return body[:end]
}

var consoleToolName = regexp.MustCompile(`\{\s*name:\s*'([a-z0-9_]+)'`)

func consoleClaimedTools(t *testing.T, text string) []string {
	t.Helper()
	block := consoleToolBlock(t, text)
	matches := consoleToolName.FindAllStringSubmatch(block, -1)
	if len(matches) == 0 {
		t.Fatalf("no tool entries parsed out of AGENT_TOOLS. The array's shape changed, " +
			"and a guard that parses nothing passes against every future change.")
	}
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		names = append(names, m[1])
	}
	return names
}

// consoleToolLine returns the source line declaring one tool, so a caller can assert
// on the flags written beside it.
func consoleToolLine(t *testing.T, text, name string) string {
	t.Helper()
	for _, line := range strings.Split(consoleToolBlock(t, text), "\n") {
		if strings.Contains(line, "'"+name+"'") {
			return line
		}
	}
	t.Fatalf("%s has no AGENT_TOOLS entry for %q", consoleConstantsPath, name)
	return ""
}

// consoleRepoRoot walks up to the directory holding the console's source. It fails
// rather than skipping: a skipped drift guard is indistinguishable from a passing one,
// which is exactly the property that makes one worth writing.
func consoleRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, consoleConstantsPath)); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find the repository root above this test. The guard needs " +
				consoleConstantsPath + "; if this module is built on its own the guard " +
				"cannot run, and that should be visible rather than silent.")
		}
		dir = parent
	}
}
