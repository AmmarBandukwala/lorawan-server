package nsclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// recorder is a stand-in network server. It answers every path with a fixed body
// and remembers what was actually asked for, because the point of most of these
// tests is the request rather than the response.
type recorder struct {
	gotMethod string
	gotPath   string
	gotQuery  string
	gotAuth   string
	status    int
	body      string
	requests  int
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.requests++
	r.gotMethod, r.gotPath, r.gotQuery = req.Method, req.URL.Path, req.URL.RawQuery
	r.gotAuth = req.Header.Get("Authorization")
	if r.status != 0 {
		w.WriteHeader(r.status)
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(r.body))
}

// newRecorderClient wires a client to a stand-in API server. The status base URL is
// deliberately pointed at a second server so that a read accidentally routed to
// /status shows up as a request-count failure rather than passing.
func newRecorderClient(t *testing.T, api *recorder) *Client {
	t.Helper()
	apiSrv := httptest.NewServer(api)
	t.Cleanup(apiSrv.Close)

	statusSrv := httptest.NewServer(&recorder{body: `{"region":"EU868"}`})
	t.Cleanup(statusSrv.Close)

	c, err := New(apiSrv.URL, "0123456789abcdef0123456789", WithStatusURL(statusSrv.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// TestEveryReadHitsTheRouteTheNetworkServerServes is the reason the route prefixes
// are constants in one place. ns and mcp are separate modules with no shared
// package, so a wrong prefix is not a compile error and not a compile-time failure
// either: it is a 404 from the network server's default mux, which an agent would
// report as "that device does not exist".
func TestEveryReadHitsTheRouteTheNetworkServerServes(t *testing.T) {
	const eui = "0011223344556677"

	cases := []struct {
		name      string
		call      func(*Client) error
		wantPath  string
		wantQuery string
	}{
		{"ListDevices", func(c *Client) error { _, err := c.ListDevices(context.Background()); return err },
			"/api/v1/devices", ""},
		{"GetDevice", func(c *Client) error { _, err := c.GetDevice(context.Background(), eui); return err },
			"/api/v1/devices/0011223344556677", ""},
		{"Session", func(c *Client) error { _, err := c.Session(context.Background(), eui); return err },
			"/api/v1/devices/0011223344556677/session", ""},
		{"ListUplinks without filters", func(c *Client) error {
			_, err := c.ListUplinks(context.Background(), "", 0)
			return err
		}, "/api/v1/uplinks", ""},
		{"ListUplinks with filters", func(c *Client) error {
			_, err := c.ListUplinks(context.Background(), eui, 7)
			return err
		}, "/api/v1/uplinks", "dev_eui=0011223344556677&limit=7"},
		{"ListEvents without filters", func(c *Client) error {
			_, err := c.ListEvents(context.Background(), "", "", 0)
			return err
		}, "/api/v1/events", ""},
		{"ListEvents with filters", func(c *Client) error {
			_, err := c.ListEvents(context.Background(), eui, "uplink_drop", 20)
			return err
		}, "/api/v1/events", "dev_eui=0011223344556677&kind=uplink_drop&limit=20"},
		{"ListModels", func(c *Client) error { _, err := c.ListModels(context.Background()); return err },
			"/api/v1/models", ""},
		{"GetModel", func(c *Client) error { _, err := c.GetModel(context.Background(), "sensecap-c01"); return err },
			"/api/v1/models/sensecap-c01", ""},
		{"ListIntegrations", func(c *Client) error { _, err := c.ListIntegrations(context.Background()); return err },
			"/api/v1/integrations", ""},
		{"GetIntegration", func(c *Client) error { _, err := c.GetIntegration(context.Background(), "ops"); return err },
			"/api/v1/integrations/ops", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			up := &recorder{body: `{}`}
			c := newRecorderClient(t, up)
			if err := tc.call(c); err != nil {
				t.Fatalf("call: %v", err)
			}
			if up.gotMethod != http.MethodGet {
				t.Errorf("method = %s, want GET", up.gotMethod)
			}
			if up.gotPath != tc.wantPath {
				t.Errorf("path = %q, want %q", up.gotPath, tc.wantPath)
			}
			if up.gotQuery != tc.wantQuery {
				t.Errorf("query = %q, want %q", up.gotQuery, tc.wantQuery)
			}
			// Every API read carries the token. An omission here surfaces as a 401
			// that reads like a bad token rather than a missing header.
			if want := "Bearer 0123456789abcdef0123456789"; up.gotAuth != want {
				t.Errorf("Authorization = %q, want %q", up.gotAuth, want)
			}
		})
	}
}

// TestStatusGoesToTheStatusBaseWithoutAToken pins the split the network server
// makes on purpose: /status is unauthenticated and served on another port. Sending
// the bearer token there would be a leak waiting for a redirect.
func TestStatusGoesToTheStatusBaseWithoutAToken(t *testing.T) {
	api := &recorder{body: `{}`}
	statusSrv := httptest.NewServer(&recorder{body: `{"region":"EU868"}`})
	t.Cleanup(statusSrv.Close)

	c, err := New(mustServerURL(t, api), "0123456789abcdef0123456789",
		WithStatusURL(statusSrv.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	raw, err := c.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if string(raw) != `{"region":"EU868"}` {
		t.Errorf("body = %s", raw)
	}
	if api.requests != 0 {
		t.Errorf("the API server saw %d requests; /status must not go through it", api.requests)
	}
}

func mustServerURL(t *testing.T, h http.Handler) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}
