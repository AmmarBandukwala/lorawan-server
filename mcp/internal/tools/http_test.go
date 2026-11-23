package tools

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fieldnode/lorawan-server/mcp/internal/httpserver"
	"github.com/fieldnode/lorawan-server/mcp/internal/nsclient"
)

const listenerToken = "a-deployment-token-that-is-long-enough"

// bearerTransport injects the listener's token into every request a client makes.
//
// This is the shape a host has to reach: the SDK's streamable client takes an
// *http.Client rather than a token, so authenticating means a RoundTripper. It is
// written out here because it is the part of running over HTTP that is not obvious,
// and the README shows the same three lines.
type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (b bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Cloned rather than mutated: a RoundTripper must not modify the request it is
	// handed, and the SDK reuses these across retries.
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(clone)
}

// serveTools starts the HTTP listener in front of the same server stdio would have
// served, backed by fake, and returns a connected client session.
func serveTools(t *testing.T, fake *fakeNS, profile Profile) *mcp.ClientSession {
	t.Helper()

	upstream := httptest.NewServer(fake)
	t.Cleanup(upstream.Close)

	client, err := nsclient.New(upstream.URL, testToken)
	if err != nil {
		t.Fatalf("nsclient.New: %v", err)
	}
	server, _, err := New(&Deps{NS: client}, profile, "test",
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("tools.New: %v", err)
	}

	handler, err := httpserver.NewHandler(server, httpserver.Config{
		BindAddr:  ":0",
		AuthToken: listenerToken,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("httpserver.NewHandler: %v", err)
	}
	listener := httptest.NewServer(handler)
	t.Cleanup(listener.Close)

	transport := &mcp.StreamableClientTransport{
		Endpoint: listener.URL + httpserver.Endpoint,
		HTTPClient: &http.Client{
			Transport: bearerTransport{token: listenerToken, base: http.DefaultTransport},
		},
		// The listener is stateless, so there is no channel for it to initiate on
		// and nothing here would use one: sampling, roots and server-initiated
		// logging are all deprecated.
		DisableStandaloneSSE: true,
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil).
		Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatalf("connect over HTTP: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// TestAHostWithoutTheTokenCannotGetIn is the failure a deployment actually hits: the
// transport connects, the first request is refused, and the error has to say so
// rather than hang or report an empty tool list.
func TestAHostWithoutTheTokenCannotGetIn(t *testing.T) {
	fake := healthyFake(t)
	upstream := httptest.NewServer(fake)
	t.Cleanup(upstream.Close)

	ns, err := nsclient.New(upstream.URL, testToken)
	if err != nil {
		t.Fatalf("nsclient.New: %v", err)
	}
	server, _, err := New(&Deps{NS: ns}, ProfileRead, "test",
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("tools.New: %v", err)
	}
	handler, err := httpserver.NewHandler(server, httpserver.Config{
		BindAddr: ":0", AuthToken: listenerToken,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	listener := httptest.NewServer(handler)
	t.Cleanup(listener.Close)

	// No HTTPClient, so no token is ever sent.
	transport := &mcp.StreamableClientTransport{
		Endpoint:             listener.URL + httpserver.Endpoint,
		DisableStandaloneSSE: true,
	}
	_, err = mcp.NewClient(&mcp.Implementation{Name: "unauthenticated", Version: "0"}, nil).
		Connect(context.Background(), transport, nil)
	if err == nil {
		t.Fatal("an unauthenticated host completed a session")
	}
	if !strings.Contains(err.Error(), "Unauthorized") {
		t.Errorf("the failure should name the refusal, got %v", err)
	}
}

// TestTheWholeServerOverHTTP is the transport's reason to exist: the same tools, the
// same upstream and the same answers, reached over a socket instead of a pipe.
//
// It goes through the real client rather than calling handlers, because the thing
// worth knowing is whether a host can complete a session against a stateless
// listener — which is a property of the SDK's client and this server together, and
// not something either package can assert alone.
func TestTheWholeServerOverHTTP(t *testing.T) {
	fake := healthyFake(t)
	cs := serveTools(t, fake, ProfileRead)

	if res := cs.InitializeResult(); res == nil || !strings.Contains(res.Instructions, "live LoRaWAN network") {
		t.Errorf("the orientation did not survive the transport: %+v", res)
	}

	names := advertisedNames(t, cs)
	if len(names) != len(wantTools) {
		t.Errorf("over HTTP the server advertises %d tools, over stdio %d",
			len(names), len(wantTools))
	}

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "get_device",
		Arguments: map[string]any{"dev_eui": testEUI},
	})
	if err != nil {
		t.Fatalf("CallTool over HTTP: %v", err)
	}
	if res.IsError {
		t.Fatalf("get_device failed over HTTP: %v", res.Content)
	}
	if fake.requests["/api/v1/devices/"+testEUI] != 1 {
		t.Errorf("the network server saw %d requests, want 1",
			fake.requests["/api/v1/devices/"+testEUI])
	}
}

// TestWritesAreAbsentOverHTTPToo: the profile is a property of the process, not of
// the transport. A host must not gain a write by choosing a different way in.
func TestWritesAreAbsentOverHTTPToo(t *testing.T) {
	readOnly := advertisedNames(t, serveTools(t, healthyFake(t), ProfileRead))
	for _, name := range WriteTools(ProfileOperate) {
		if strings.Contains(strings.Join(readOnly, ","), name) {
			t.Errorf("%s is reachable over HTTP under the read profile", name)
		}
	}

	operate := advertisedNames(t, serveTools(t, healthyFake(t), ProfileOperate))
	for _, name := range WriteTools(ProfileOperate) {
		if !strings.Contains(strings.Join(operate, ","), name) {
			t.Errorf("%s is not reachable over HTTP under the operate profile", name)
		}
	}
}

// TestADefaultHostWorksAgainstTheStatelessListener is the interop question the
// transport hinges on, and it was not obvious enough to assume either way.
//
// The SDK's client opens a standalone SSE stream by default so the server can
// initiate. A stateless listener has no session to initiate on and answers that GET
// with 405. Whether that is fatal is a property of the client and this server
// together, and it decides whether a host needs to be configured for us: a host that
// cannot connect without a special flag is a server that ships with an instruction
// manual, and one that tolerates the 405 needs no instruction at all.
func TestADefaultHostWorksAgainstTheStatelessListener(t *testing.T) {
	fake := healthyFake(t)
	upstream := httptest.NewServer(fake)
	t.Cleanup(upstream.Close)

	ns, err := nsclient.New(upstream.URL, testToken)
	if err != nil {
		t.Fatalf("nsclient.New: %v", err)
	}
	server, _, err := New(&Deps{NS: ns}, ProfileRead, "test",
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("tools.New: %v", err)
	}
	handler, err := httpserver.NewHandler(server, httpserver.Config{
		BindAddr: ":0", AuthToken: listenerToken,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	listener := httptest.NewServer(handler)
	t.Cleanup(listener.Close)

	// Everything left at its default except the credential: no
	// DisableStandaloneSSE, so the client opens the SSE stream and meets the 405.
	transport := &mcp.StreamableClientTransport{
		Endpoint: listener.URL + httpserver.Endpoint,
		HTTPClient: &http.Client{
			Transport: bearerTransport{token: listenerToken, base: http.DefaultTransport},
		},
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "default-host", Version: "0"}, nil).
		Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatalf("a default-configured host could not connect to a stateless listener: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	got, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools after the 405: %v", err)
	}
	if len(got.Tools) == 0 {
		t.Error("the default host connected but was given no tools")
	}
}

// TestResourcesAndPromptsOverHTTP: a host that pins a resource needs it to be
// readable over the same transport it lists it on.
func TestResourcesAndPromptsOverHTTP(t *testing.T) {
	cs := serveTools(t, healthyFake(t), ProfileRead)

	listed, err := cs.ListResources(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListResources over HTTP: %v", err)
	}
	if len(listed.Resources) == 0 {
		t.Fatal("no resources over HTTP")
	}

	content, err := cs.ReadResource(context.Background(),
		&mcp.ReadResourceParams{URI: "fieldnode://status"})
	if err != nil {
		t.Fatalf("ReadResource over HTTP: %v", err)
	}
	if len(content.Contents) == 0 {
		t.Fatal("fieldnode://status returned nothing over HTTP")
	}
	text := content.Contents[0]
	if !strings.Contains(text.Text, "EU868") {
		t.Errorf("the status resource did not carry the upstream body: %s", text.Text)
	}
	if !strings.HasPrefix(text.URI, "fieldnode://") {
		t.Errorf("the resource came back under URI %q, not the fieldnode scheme", text.URI)
	}
}
