package mcpclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// testToken is long enough to satisfy the same floor the listener enforces.
const testToken = "0123456789abcdef0123456789abcdef"

// fakeListener serves a real MCP server over the same transport the console uses, so
// these tests exercise the protocol rather than a stand-in that agrees with itself.
//
// The listener requires a bearer token because that is the deployment: an endpoint that
// answered without one here would let a regression in the transport go unnoticed.
func fakeListener(t *testing.T) (string, *int) {
	t.Helper()

	server := mcp.NewServer(&mcp.Implementation{Name: "fake-network", Version: "1.0.0"}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "network_status",
		Description: "The network's health.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{
			&mcp.TextContent{Text: `{"region":"EU868","gateways_online":2}`},
		}}, nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_device",
		Description: "One device.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(_ context.Context, _ *mcp.CallToolRequest, in struct {
		DevEUI string `json:"dev_eui" jsonschema:"the device identifier"`
	}) (*mcp.CallToolResult, any, error) {
		if in.DevEUI != "0011223344556677" {
			// A refusal carried in the tool result rather than as a protocol error,
			// exactly as the real listener does, so what is under test is the
			// client's handling of it.
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{
					Text: "dev_eui " + in.DevEUI + " is not provisioned",
				}},
			}, nil, nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{
			Text: `{"dev_eui":"0011223344556677","enabled":true}`,
		}}}, nil, nil
	})

	// A write, so the read-only filter has something to reject.
	mcp.AddTool(server, &mcp.Tool{
		Name:        "enqueue_downlink",
		Description: "Queue a frame.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false},
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{
			&mcp.TextContent{Text: `{"queued":true}`},
		}}, nil, nil
	})

	server.AddResource(&mcp.Resource{
		URI:         "fieldnode://event-vocabulary",
		Name:        "event-vocabulary",
		MIMEType:    "text/plain",
		Description: "Every reason the network records.",
	}, func(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI:  "fieldnode://event-vocabulary",
			Text: "f_cnt_replay: the counter is at or below the highest accepted",
		}}}, nil
	})

	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true},
	)

	// Counted so a test can assert the token was actually presented, rather than
	// asserting that a request succeeded when the fake would have answered anyway.
	seen := new(int)
	guarded := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen++
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			http.Error(w, `{"error":"missing or wrong token"}`, http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	})

	srv := httptest.NewServer(guarded)
	t.Cleanup(srv.Close)
	return srv.URL + "/mcp", seen
}

// mustOpen dials a fake listener and fails the test if the session cannot be
// established. The returned session is closed by the test.
func mustOpen(t *testing.T, endpoint string, _ *int) *Session {
	t.Helper()
	client, err := New(Config{URL: endpoint, Token: testToken})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	session, err := client.Open(context.Background())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return session
}
