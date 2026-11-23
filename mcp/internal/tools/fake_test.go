package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fieldnode/lorawan-server/mcp/internal/nsclient"
)

// fakeNS is a stand-in network server. Its bodies are the shapes the real handlers
// produce, taken from the view structs rather than invented: a digest test that
// passes against a made-up shape proves nothing about the digest.
type fakeNS struct {
	status    int
	responses map[string]string
	statuses  map[string]int
	requests  map[string]int
	queries   map[string][]string
	methods   map[string][]string
	bodies    map[string][]string
	// successCodes holds non-200 success statuses keyed by "METHOD /path", because the
	// network server's writes are not all 200 — queueing a frame is 202 — and a test
	// that asserts on the audited status has to be looking at the real one.
	successCodes map[string]int
}

func (f *fakeNS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.requests[r.URL.Path]++
	f.queries[r.URL.Path] = append(f.queries[r.URL.Path], r.URL.RawQuery)
	f.methods[r.URL.Path] = append(f.methods[r.URL.Path], r.Method)
	if body, err := io.ReadAll(r.Body); err == nil && len(body) > 0 {
		f.bodies[r.URL.Path] = append(f.bodies[r.URL.Path], string(body))
	}
	// An error status is served with the body configured for that path, because the
	// body is the thing under test: the network server's 409 and 404 sentences are
	// exactly what a tool result is supposed to carry back.
	if code := f.statuses[r.URL.Path]; code != 0 && code != http.StatusOK {
		w.WriteHeader(code)
		if body, ok := f.responses[r.URL.Path]; ok {
			fmt.Fprint(w, body)
			return
		}
		fmt.Fprint(w, `{"error":"simulated failure on `+r.URL.Path+`"}`)
		return
	}
	if f.status != 0 && f.status != http.StatusOK {
		w.WriteHeader(f.status)
		fmt.Fprint(w, `{"error":"simulated failure"}`)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	body, ok := f.responses[r.URL.Path]
	if !ok {
		// An unhandled path is a routing mistake in the digest. The honest answer is
		// a 404 exactly as the real server would return, so the digest reports a gap
		// and the test sees it, rather than passing on an empty body.
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":"no route for `+r.URL.Path+`"}`)
		return
	}
	// Non-200 successes are keyed by method and path, so a test asserting on the
	// audited status is looking at the code the real server sends.
	if code := f.successCodes[r.Method+" "+r.URL.Path]; code != 0 {
		w.WriteHeader(code)
	}
	fmt.Fprint(w, body)
}

const (
	testEUI   = "0011223344556677"
	testToken = "0123456789abcdef0123456789"

	bodyDevice = `{"dev_eui":"0011223344556677","name":"Basement sensor",` +
		`"group":"cold-chain","enabled":true,"activation":"otaa","model_id":"sensecap-c01"}`

	bodySession = `{"dev_addr":"26011234","region":"EU868","f_cnt_up":41,"data_rate":5,` +
		`"adr":true,"last_rssi":-97.5,"last_snr":-8.25,` +
		`"last_uplink_at":"2026-11-21T09:15:04Z","battery":255,"margin_db":null,` +
		`"pending_downlink":null}`

	bodyEvents = `{"count":3,"limit":8,"events":[` +
		`{"occurred_at":"2026-11-21T09:15:04Z","kind":"uplink","reason":"","detail":""},` +
		`{"occurred_at":"2026-11-21T09:14:51Z","kind":"uplink_drop",` +
		`"reason":"f_cnt_gap_too_large","detail":"f_cnt 41 is 9000 ahead of 41"},` +
		`{"occurred_at":"2026-11-21T08:02:00Z","kind":"join_accept","reason":"","detail":""}]}`

	bodyUplinks = `{"count":2,"limit":5,"uplinks":[` +
		`{"received_at":"2026-11-21T09:15:04Z","f_port":2,"rssi":-97.5,"snr":-8.25,"has_codec":true},` +
		`{"received_at":"2026-11-21T09:14:51Z","f_port":2,"rssi":-101,"snr":-11,` +
		`"has_codec":true,"decode_error":"expected at least 4 bytes, got 1"}]}`

	bodyStatus = `{"region":"EU868","net_id":"010000","uptime_seconds":3600,` +
		`"gateways_total":2,"gateways_online":1,` +
		`"transports":["semtech udp","basics station"],` +
		`"duty_cycle":{"enabled":true,"denials":{"eu868:subg_2":3}}}`

	// The network server's own 202 body, including the explanation field it adds
	// deliberately for a caller who expected a synchronous send.
	bodyQueued = `{"dev_eui":"0011223344556677","queued":true,"f_port":10,` +
		`"confirmed":false,"explanation":"the frame goes out in the device's next RX window"}`

	// The network server's 409 sentence for a device that already holds a frame.
	bodyConflict = `{"error":"the device already has a downlink waiting; the network server "` +
		`"keeps one per device so a queue cannot silently reorder application frames. "` +
		`DELETE the pending frame first"}`
)

func healthyFake(t *testing.T) *fakeNS {
	t.Helper()
	return &fakeNS{
		responses: map[string]string{
			"/api/v1/devices/" + testEUI:              bodyDevice,
			"/api/v1/devices/" + testEUI + "/session": bodySession,
			"/api/v1/events":                          bodyEvents,
			"/api/v1/uplinks":                         bodyUplinks,
			"/status":                                 bodyStatus,
			"/api/v1/devices":                         `[{"dev_eui":"0011223344556677"}]`,
			"/api/v1/models":                          `[{"model_id":"sensecap-c01"}]`,
			"/api/v1/models/sensecap-c01":             `{"model_id":"sensecap-c01","codec":"javascript"}`,
			"/api/v1/integrations":                    `[{"id":"ops","kind":"mqttls"}]`,
			"/api/v1/integrations/ops":                `{"id":"ops","kind":"mqttls"}`,

			// Write responses, taken from the network server's own view structs so a
			// test that asserts on them is asserting on the real contract.
			"/api/v1/devices/" + testEUI + "/downlink":  bodyQueued,
			"/api/v1/devices/" + testEUI + "/devstatus": `{"dev_eui":"0011223344556677","scheduled":true}`,
			"/api/v1/devices/AABBCCDDEEFF0011/downlink": `{"error":"no session for DevEUI AABBCCDDEEFF0011; the device must join first"}`,
		},
		// A device that is not provisioned must arrive as the 404 the network server
		// actually sends. Returning its body with a 200 let the digest render a
		// confident full report about a device that does not exist — precisely the
		// failure this harness exists to make impossible.
		statuses: map[string]int{
			"/api/v1/devices/AABBCCDDEEFF0011":          http.StatusNotFound,
			"/api/v1/devices/AABBCCDDEEFF0011/downlink": http.StatusNotFound,
		},
		requests: map[string]int{},
		queries:  map[string][]string{},
		methods:  map[string][]string{},
		bodies:   map[string][]string{},
		successCodes: map[string]int{
			// 202, not 200: the frame is accepted for the next window, not
			// transmitted. The distinction is the whole point of the tool's text.
			"POST /api/v1/devices/" + testEUI + "/downlink":  http.StatusAccepted,
			"POST /api/v1/devices/" + testEUI + "/devstatus": http.StatusAccepted,
		},
	}
}

// newTestServer wires an MCP client session to a server whose tools read from fake.
func newTestServer(t *testing.T, fake *fakeNS) *mcp.ClientSession {
	t.Helper()
	return newProfileServer(t, fake, ProfileRead)
}

// newOperateServer is the same with writes enabled, so a test can state which tier it
// is exercising rather than leaving a reader to infer it from the tool it calls.
func newOperateServer(t *testing.T, fake *fakeNS) *mcp.ClientSession {
	t.Helper()
	return newProfileServer(t, fake, ProfileOperate)
}

func newProfileServer(t *testing.T, fake *fakeNS, profile Profile) *mcp.ClientSession {
	t.Helper()

	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	client, err := nsclient.New(srv.URL, testToken)
	if err != nil {
		t.Fatalf("nsclient.New: %v", err)
	}
	return newServerWithDeps(t, &Deps{NS: client}, profile)
}

// newServerWithDeps connects a client session to a server built from a Deps the
// caller assembled, so a test can supply its own audit logger or a client pointed
// somewhere unusual without rebuilding this plumbing.
func newServerWithDeps(t *testing.T, deps *Deps, profile Profile) *mcp.ClientSession {
	t.Helper()

	server, _, err := New(deps, profile, "test", nil)
	if err != nil {
		t.Fatalf("tools.New: %v", err)
	}

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Run(context.Background(), serverTransport) }()

	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil).
		Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() {
		if err := cs.Close(); err != nil {
			t.Errorf("closing the client session: %v", err)
		}
		<-serverDone
	})

	return cs
}

// callText invokes a tool and returns its text content, failing the test if the
// call itself failed at the protocol level.
func callText(t *testing.T, cs *mcp.ClientSession, name string, args any) (string, *mcp.CallToolResult) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	var sb []byte
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb = append(sb, tc.Text...)
		}
	}
	return string(sb), res
}
