package resources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fieldnode/lorawan-server/mcp/internal/nsclient"
	"github.com/fieldnode/lorawan-server/mcp/internal/redact"
)

const (
	testToken = "0123456789abcdef0123456789"
	testEUI   = "0011223344556677"
)

// fakeNS answers with bodies an operator's network server would return, and records
// what was asked, so a test can prove both what came back and what was fetched.
type fakeNS struct {
	responses map[string]string
	statuses  map[string]int
	requests  map[string]int
}

func (f *fakeNS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.requests[r.URL.Path]++
	w.Header().Set("Content-Type", "application/json")
	if code := f.statuses[r.URL.Path]; code != 0 {
		w.WriteHeader(code)
	}
	if body, ok := f.responses[r.URL.Path]; ok {
		_, _ = w.Write([]byte(body))
		return
	}
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"error":"no route for ` + r.URL.Path + `"}`))
}

func healthyFake() *fakeNS {
	return &fakeNS{
		responses: map[string]string{
			"/status":                    `{"region":"EU868","gateways_total":2,"gateways_online":1}`,
			"/api/v1/devices":            `[{"dev_eui":"0011223344556677","name":"kitchen"}]`,
			"/api/v1/devices/" + testEUI: `{"dev_eui":"0011223344556677","name":"kitchen","enabled":true}`,
			"/api/v1/devices/" + testEUI + "/session": `{"dev_addr":"01020304","data_rate":5}`,
		},
		statuses: map[string]int{},
		requests: map[string]int{},
	}
}

// newSession connects a client to a server built the way main builds it.
func newSession(t *testing.T, fake *fakeNS) *mcp.ClientSession {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	client, err := nsclient.New(srv.URL, testToken)
	if err != nil {
		t.Fatalf("nsclient.New: %v", err)
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	Register(server, client)

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- server.Run(context.Background(), serverTransport) }()

	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil).
		Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() {
		_ = cs.Close()
		<-done
	})
	return cs
}

func read(t *testing.T, cs *mcp.ClientSession, uri string) (string, error) {
	t.Helper()
	got, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri})
	if err != nil {
		return "", err
	}
	if len(got.Contents) != 1 {
		t.Fatalf("%s returned %d contents, want 1", uri, len(got.Contents))
	}
	return got.Contents[0].Text, nil
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func TestEveryResourceIsAdvertised(t *testing.T) {
	cs := newSession(t, healthyFake())

	got, err := cs.ListResources(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	var uris []string
	for _, r := range got.Resources {
		uris = append(uris, r.URI)
	}
	for _, want := range []string{URIStatus, URIDevices, URIVocabulary} {
		if !strings.Contains(strings.Join(uris, ","), want) {
			t.Errorf("%s is not advertised; got %v", want, uris)
		}
	}

	templates, err := cs.ListResourceTemplates(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListResourceTemplates: %v", err)
	}
	var patterns []string
	for _, tmpl := range templates.ResourceTemplates {
		patterns = append(patterns, tmpl.URITemplate)
	}
	for _, want := range []string{URIDeviceTmpl, URISessionTmpl} {
		if !strings.Contains(strings.Join(patterns, ","), want) {
			t.Errorf("%s is not advertised; got %v", want, patterns)
		}
	}
}

// TestResourceReadsAreScrubbed is the load-bearing test of this package.
//
// The tool path scrubs credential-looking values before they reach a client. A resource
// path returning the same upstream bytes unscrubbed would be a second door into the same
// data with the guard left at the first one, and it would look fine in every test that
// only checks the happy path.
func TestResourceReadsAreScrubbed(t *testing.T) {
	fake := healthyFake()
	fake.responses["/status"] = `{"region":"EU868","app_key":"00112233445566778899aabbccddeeff",` +
		`"nested":{"token":"leakme"}}`

	body, err := read(t, newSession(t, fake), URIStatus)
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	for _, leak := range []string{"00112233445566778899aabbccddeeff", "leakme"} {
		if strings.Contains(body, leak) {
			t.Errorf("the resource path returned an unscrubbed value %q:\n%s", leak, body)
		}
	}
	if !strings.Contains(body, redact.Marker) {
		t.Errorf("no redaction marker, so a reader cannot tell this was scrubbed:\n%s", body)
	}
	// The innocent parts must survive; scrubbing must not become censoring.
	if !strings.Contains(body, "EU868") {
		t.Errorf("innocent content was lost:\n%s", body)
	}
}

func TestRegisterWithNoClientAddsNothing(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	if got := Register(server, nil); got != nil {
		t.Errorf("Register(nil) = %v, want nothing registered", got)
	}
}
