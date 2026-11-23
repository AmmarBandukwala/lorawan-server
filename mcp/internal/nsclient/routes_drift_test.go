package nsclient

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestRoutesStillMatchTheNetworkServersMux is the guard the rest of this file cannot
// be. TestEveryReadHitsTheRouteTheNetworkServerServes asserts the client sends what its
// own constants say — so when the network server renames a route, both the client and
// that test move together and stay green while production 404s.
//
// This one reads the network server's mux as text and fails if a path or verb this
// client uses is no longer registered there. ns and mcp are separate Go modules with no
// shared package, so a rename is invisible to the compiler; the only thing that can
// catch it is a test that looks across the boundary.
//
// It is the same approach as the vocabulary drift test in internal/vocabulary: the
// compiler cannot see prose or another module, and this repository has already been
// bitten by a contract that drifted quietly.
func TestRoutesStillMatchTheNetworkServersMux(t *testing.T) {
	root := repoRoot(t)
	source := filepath.Join(root, "ns", "cmd", "ns", "api.go")
	text, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("reading the network server's mux at %s: %v", source, err)
	}

	registered := parseMux(t, string(text))

	// Every (method, path) pair this client can issue, built from the same constants
	// the production code calls. A rename on either side breaks the match.
	cases := []struct{ method, path string }{
		{http.MethodGet, devicesRoute},
		{http.MethodGet, devicePath("0011223344556677")},
		{http.MethodGet, devicePath("0011223344556677") + "/session"},
		{http.MethodPatch, devicePath("0011223344556677")},
		{http.MethodPost, devicePath("0011223344556677") + downlinkRoute},
		{http.MethodDelete, devicePath("0011223344556677") + downlinkRoute},
		{http.MethodPost, devicePath("0011223344556677") + devStatusRoute},
		{http.MethodGet, uplinksRoute},
		{http.MethodGet, eventsRoute},
		{http.MethodGet, modelsRoute},
		{http.MethodGet, modelsRoute + "/sensecap-c01"},
		{http.MethodGet, integrationsRoute},
		{http.MethodGet, integrationsRoute + "/ops"},
	}

	for _, tc := range cases {
		// The mux registers {dev_eui} and {model_id} where this client substitutes a
		// value, so compare against the pattern the ns actually declares.
		pattern := tc.path
		for _, seg := range []string{"0011223344556677", "sensecap-c01", "ops"} {
			pattern = strings.Replace(pattern, "/"+seg, "/{"+paramName(seg)+"}", 1)
		}
		key := tc.method + " " + pattern
		if _, ok := registered[key]; !ok {
			t.Errorf("the network server does not register %q.\n"+
				"This client would send %s %s and receive a 404 that reads like a missing "+
				"device. Either the ns renamed the route, or this client added a call the "+
				"ns does not serve.", key, tc.method, tc.path)
		}
	}
}

// TestStatusRouteIsServedByTheStatusServerNotTheAPI: the split is deliberate, and if it
// ever changed the client would be sending an unauthenticated request to an endpoint
// that started requiring one — a failure that looks like a bad token.
func TestStatusRouteIsServedByTheStatusServerNotTheAPI(t *testing.T) {
	root := repoRoot(t)
	apiSource := filepath.Join(root, "ns", "cmd", "ns", "api.go")
	text, err := os.ReadFile(apiSource)
	if err != nil {
		t.Fatalf("reading %s: %v", apiSource, err)
	}
	if strings.Contains(string(text), `"`+http.MethodGet+" "+statusRoute+`"`) {
		t.Errorf("the API server now serves %s. The client fetches it from the status URL "+
			"without a token, so it would start failing with what looks like an "+
			"authentication problem.", statusRoute)
	}
}

// paramName maps the sample values used above back to the mux's placeholder names.
func paramName(value string) string {
	switch value {
	case "0011223344556677":
		return "dev_eui"
	case "sensecap-c01":
		return "model_id"
	default:
		return "id"
	}
}

// muxPattern matches a route registration: mux.HandleFunc("GET /api/v1/events", ...).
var muxPattern = regexp.MustCompile(`HandleFunc\("([A-Z]+) (/[^"]*)"`)

func parseMux(t *testing.T, source string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, m := range muxPattern.FindAllStringSubmatch(source, -1) {
		out[m[1]+" "+m[2]] = m[0]
	}
	if len(out) == 0 {
		t.Fatal("no route registrations were parsed. The network server's mux no longer " +
			"looks like HandleFunc(\"METHOD /path\", ...), so this drift guard is reading " +
			"nothing and would pass against any change. Update the pattern.")
	}
	return out
}

// repoRoot walks up from the test's directory to the repository root, identified by a
// file that has to exist for the ns to build at all.
//
// It fails rather than skipping: a skipped drift guard is indistinguishable from a
// passing one, which is exactly the property that makes one worth writing.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "ns", "cmd", "ns", "main.go")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find the repository root above this test. The drift guard " +
				"needs ns/cmd/ns/api.go; if this module is being built on its own, the " +
				"guard cannot run and that should be visible rather than silent.")
		}
		dir = parent
	}
}
