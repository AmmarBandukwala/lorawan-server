package nsclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

// Every route this client uses, pinned.
//
// These tests exist because the paths here have to match the routes the network
// server registers in ns/cmd/ns/api.go, and nothing enforced that: the two live
// in separate Go modules, so a mismatch is not a compile error, and the handlers
// above this package are tested against a stub controller, so the URL was never
// the thing under test. What a wrong prefix produces is a 404 from the network
// server's default mux — reported to an operator as "page not found" on a screen
// that looks like it should have data. /api/v1/models and /api/v1/uplinks both
// shipped that way.

const (
	testDevEUI  = "0102030405060708"
	testModelID = "cold-chain-probe-v2"

	// testIntegrationID is a valid destination id under the network server's
	// slug rule, so a path built from it is one a real server would accept.
	testIntegrationID = "cloud-mq"
)

type call struct {
	name   string
	invoke func(*Client) error
	method string
	path   string
	query  string
}

// errWantRejected turns "this call must fail before it reaches the network" into
// the shape the table above expects: a non-nil error is the pass condition, and
// a nil one becomes a failure.
func errWantRejected(err error) error {
	if err == nil {
		return errors.New("expected the client to refuse the call, but it sent it")
	}
	return nil
}

func TestClientRoutes(t *testing.T) {
	devEUI, model := testDevEUI, testModelID

	var gotMethod, gotPath, gotQuery, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotQuery, gotAuth = r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	client, err := New(srv.URL, "test-token", WithStatusURL(srv.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	calls := clientCalls(devEUI, model)

	for _, c := range calls {
		gotMethod, gotPath, gotQuery = "", "", ""
		if err := c.invoke(client); err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		// An empty expected method means the call is not supposed to reach the
		// network at all, so there is nothing to compare.
		if c.method == "" {
			continue
		}
		if gotMethod != c.method || gotPath != c.path {
			t.Errorf("%s: %s %s\n want %s %s", c.name, gotMethod, gotPath, c.method, c.path)
		}
		if gotQuery != c.query {
			t.Errorf("%s: query = %q, want %q", c.name, gotQuery, c.query)
		}
		if gotAuth != "Bearer test-token" {
			t.Errorf("%s: Authorization = %q, want the bearer token", c.name, gotAuth)
		}
	}
}

// TestRoutesShareTheAPIPrefix is the check that would have failed before the
// fix. Asserting the prefix separately from the table above means a new method
// added with a bare path cannot pass by also being absent from the table.
// TestRoutesShareTheAPIPrefix is the cheap half of the guard.
//
// TestClientPathsExistOnTheNetworkServer is the real contract check, but it
// needs the network server's source and skips when only this module is built.
// This one costs nothing and runs everywhere: it catches the specific mistake
// that shipped twice, a route constant that lost its /api/v1 prefix and so
// reached the network server's default mux instead of a handler — a 404 that
// reaches an operator as an empty screen.
func TestRoutesShareTheAPIPrefix(t *testing.T) {
	const prefix = "/api/v1/"
	for name, route := range map[string]string{
		"devices":      nsDeviceRoute,
		"models":       nsModelRoute,
		"uplinks":      nsUplinkRoute,
		"integrations": nsIntegrationRoute,
	} {
		if !strings.HasPrefix(route, prefix) {
			t.Errorf("%s route %q is not under the %s prefix the network server registers",
				name, route, prefix)
		}
	}
}

// TestClientPathsExistOnTheNetworkServer is the cross-module half of the
// contract.
//
// The console's client and the network server live in separate Go modules, so
// neither compiles against the other: a route that moved on one side is a plain
// 404 at runtime, which the console reports as "this device has no session yet"
// or an empty Models screen. TestClientRoutes pins what this client sends; this
// one reads the routes the network server actually registers and insists the two
// agree. It reads source rather than starting a process so it runs in `go test
// ./...` with no containers.
//
// If the network server ever moves its routes into another file, add that file
// to nsRouteFiles: a missing entry here fails loudly rather than silently
// skipping the check.
func TestClientPathsExistOnTheNetworkServer(t *testing.T) {
	patterns := loadNetworkServerRoutes(t)
	// Rather than a route count, which changes whenever a route is added, insist
	// that the two files really were read: one route from each of their route
	// spaces proves it, and a stale nsRouteFiles list fails here instead of
	// quietly checking nothing.
	if !anyRouteMatches(patterns, http.MethodGet, "/api/v1/models") {
		t.Fatal("no /api/v1/models route was read, so the API route file was not parsed")
	}
	if !anyRouteMatches(patterns, http.MethodGet, "/status") {
		t.Fatal("no /status route was read, so the status route file was not parsed")
	}

	// Run every call the client can make against a server that only records where
	// it was pointed, then check those real paths against the routes the network
	// server registers. Deriving them from the client is the point: a list written
	// by hand here would only restate the client's own assumption, and the bug this
	// test exists to catch was exactly that assumption being wrong.
	var sent []call
	recorder := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent = append(sent, call{method: r.Method, path: r.URL.Path})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer recorder.Close()

	client, err := New(recorder.URL, "test-token", WithStatusURL(recorder.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, c := range clientCalls(testDevEUI, testModelID) {
		if err := c.invoke(client); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if len(sent) < 15 {
		t.Fatalf("only %d requests reached the recorder; the client is not exercising its routes",
			len(sent))
	}

	for _, s := range sent {
		if !anyRouteMatches(patterns, s.method, s.path) {
			t.Errorf("the client sends %s %s but the network server registers no such route.\n"+
				"registered: %v", s.method, s.path, patterns)
		}
	}
}

// nsRouteFiles are the network server sources that register HTTP routes.
var nsRouteFiles = []string{
	"../../../ns/cmd/ns/api.go",
	"../../../ns/cmd/ns/status.go",
}

// routePattern is one registered method+path pair.
type routePattern struct {
	method   string
	segments []string
}

func loadNetworkServerRoutes(t *testing.T) []routePattern {
	t.Helper()
	var out []routePattern
	for _, rel := range nsRouteFiles {
		src, err := os.ReadFile(rel)
		if err != nil {
			// A build that mounts only this module cannot see the network
			// server's source. Skipping is right; failing would break
			// containerised builds of the API alone. Any checkout of the whole
			// repository runs the check for real.
			if os.IsNotExist(err) {
				t.Skipf("the network server source is not present (%s); "+
					"this check needs a full repository checkout", rel)
			}
			t.Fatalf("read %s: %v", rel, err)
		}
		// Go 1.22 mux patterns look like `mux.HandleFunc("GET /api/v1/x", ...)`.
		re := regexp.MustCompile(`HandleFunc\(\s*"(?:(\w+) )?([^"]+)"`)
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			out = append(out, routePattern{
				method:   strings.ToUpper(m[1]),
				segments: splitPath(m[2]),
			})
		}
	}
	return out
}

func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// anyRouteMatches reports whether some registered pattern serves method+path.
// A `{name}` segment matches any single concrete segment, which is how the mux
// itself behaves.
func anyRouteMatches(patterns []routePattern, method, path string) bool {
	got := splitPath(path)
	for _, p := range patterns {
		if p.method != method {
			continue
		}
		if len(p.segments) != len(got) {
			continue
		}
		ok := true
		for i, seg := range p.segments {
			if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
				continue
			}
			if seg != got[i] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// clientCalls is every request this client can make, with the path and query it is
// expected to carry. Both route tests read it: TestClientRoutes checks the paths
// against fixed expectations, and TestClientPathsExistOnTheNetworkServer feeds the
// same calls to the network server's registered routes. Sharing one table is what
// makes the second test a real cross-module check rather than a second copy of the
// same assumption.
func clientCalls(devEUI, model string) []call {
	return []call{
		{"CreateDevice", func(c *Client) error { _, e := c.CreateDevice(context.Background(), Device{}); return e },
			http.MethodPost, "/api/v1/devices", ""},
		{"PutDevice", func(c *Client) error {
			_, e := c.PutDevice(context.Background(), Device{DevEUI: devEUI})
			return e
		}, http.MethodPut, "/api/v1/devices/" + devEUI, ""},
		{"PutDevice without a DevEUI", func(c *Client) error {
			_, e := c.PutDevice(context.Background(), Device{})
			return errWantRejected(e)
		}, "", "", ""},
		{"Device", func(c *Client) error { _, e := c.Device(context.Background(), devEUI); return e },
			http.MethodGet, "/api/v1/devices/" + devEUI, ""},
		{"PatchDevice", func(c *Client) error { _, e := c.PatchDevice(context.Background(), devEUI, DevicePatch{}); return e },
			http.MethodPatch, "/api/v1/devices/" + devEUI, ""},
		{"DeleteDevice", func(c *Client) error { _, e := c.DeleteDevice(context.Background(), devEUI); return e },
			http.MethodDelete, "/api/v1/devices/" + devEUI, ""},
		{"Session", func(c *Client) error { _, e := c.Session(context.Background(), devEUI); return e },
			http.MethodGet, "/api/v1/devices/" + devEUI + "/session", ""},
		{"EnqueueDownlink", func(c *Client) error {
			_, e := c.EnqueueDownlink(context.Background(), devEUI, 10, "00", false)
			return e
		}, http.MethodPost, "/api/v1/devices/" + devEUI + "/downlink", ""},
		{"CancelDownlink", func(c *Client) error { _, e := c.CancelDownlink(context.Background(), devEUI); return e },
			http.MethodDelete, "/api/v1/devices/" + devEUI + "/downlink", ""},
		{"RequestDeviceStatus", func(c *Client) error {
			_, e := c.RequestDeviceStatus(context.Background(), devEUI)
			return e
		}, http.MethodPost, "/api/v1/devices/" + devEUI + "/devstatus", ""},

		{"ListModels", func(c *Client) error { _, e := c.ListModels(context.Background()); return e },
			http.MethodGet, "/api/v1/models", ""},
		{"Model", func(c *Client) error { _, e := c.Model(context.Background(), model); return e },
			http.MethodGet, "/api/v1/models/" + model, ""},
		{"CreateModel", func(c *Client) error {
			_, e := c.CreateModel(context.Background(), model, json.RawMessage(`{}`))
			return e
		}, http.MethodPost, "/api/v1/models/" + model, ""},
		{"UpdateModel", func(c *Client) error {
			_, e := c.UpdateModel(context.Background(), model, json.RawMessage(`{}`))
			return e
		}, http.MethodPut, "/api/v1/models/" + model, ""},
		{"DeleteModel", func(c *Client) error { _, e := c.DeleteModel(context.Background(), model); return e },
			http.MethodDelete, "/api/v1/models/" + model, ""},
		{"TestModelDecode", func(c *Client) error {
			_, e := c.TestModelDecode(context.Background(), model, json.RawMessage(`{}`))
			return e
		}, http.MethodPost, "/api/v1/models/" + model + "/decode", ""},

		{"Uplinks network-wide", func(c *Client) error { _, e := c.Uplinks(context.Background(), "", 20); return e },
			http.MethodGet, "/api/v1/uplinks", "limit=20"},
		{"Uplinks for one device", func(c *Client) error { _, e := c.Uplinks(context.Background(), devEUI, 5); return e },
			http.MethodGet, "/api/v1/uplinks", "dev_eui=" + devEUI + "&limit=5"},

		{"ListIntegrations", func(c *Client) error { _, e := c.ListIntegrations(context.Background()); return e },
			http.MethodGet, "/api/v1/integrations", ""},
		{"CreateIntegration", func(c *Client) error {
			_, e := c.CreateIntegration(context.Background(), json.RawMessage(`{}`))
			return e
		}, http.MethodPost, "/api/v1/integrations", ""},
		{"Integration", func(c *Client) error { _, e := c.Integration(context.Background(), testIntegrationID); return e },
			http.MethodGet, "/api/v1/integrations/" + testIntegrationID, ""},
		{"UpdateIntegration", func(c *Client) error {
			_, e := c.UpdateIntegration(context.Background(), testIntegrationID, json.RawMessage(`{}`))
			return e
		}, http.MethodPut, "/api/v1/integrations/" + testIntegrationID, ""},
		{"DeleteIntegration", func(c *Client) error { _, e := c.DeleteIntegration(context.Background(), testIntegrationID); return e },
			http.MethodDelete, "/api/v1/integrations/" + testIntegrationID, ""},
		{"TestIntegration", func(c *Client) error { _, e := c.TestIntegration(context.Background(), testIntegrationID); return e },
			http.MethodPost, "/api/v1/integrations/" + testIntegrationID + "/test", ""},

		// Health is read from the status port and is not under the API prefix,
		// because the network server serves it from a separate mux.
		{"Status", func(c *Client) error { _, e := c.Status(context.Background()); return e },
			http.MethodGet, "/status", ""},
	}
}
