package httpserver

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const testToken = "0123456789abcdef0123456789abcdef"

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testServer(t *testing.T) *mcp.Server {
	t.Helper()
	return mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
}

func handler(t *testing.T, token string) http.Handler {
	t.Helper()
	h, err := NewHandler(testServer(t), Config{
		BindAddr: ":0", AuthToken: token, Logger: quietLogger(),
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return h
}

// TestListenerRefusesToStartWithoutItsOwnToken is the whole point of this package.
// A stdio process is guarded by who can exec it; a network listener is guarded by
// nothing else, so "no token" cannot be a mode that runs.
func TestListenerRefusesToStartWithoutItsOwnToken(t *testing.T) {
	cases := map[string]Config{
		"no token":        {BindAddr: ":8092"},
		"a blank token":   {BindAddr: ":8092", AuthToken: "   "},
		"a short token":   {BindAddr: ":8092", AuthToken: "abc123"},
		"no bind address": {BindAddr: "", AuthToken: testToken},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewHandler(testServer(t), cfg); err == nil {
				t.Errorf("%s: NewHandler accepted it", name)
			}
		})
	}
}

// TestNoServerMeansNoHandler: a listener with nothing behind it would answer every
// authenticated request with an empty tool list, which reads as an idle network
// rather than as a misconfiguration.
func TestNoServerMeansNoHandler(t *testing.T) {
	if _, err := NewHandler(nil, Config{BindAddr: ":8092", AuthToken: testToken}); err == nil {
		t.Error("NewHandler accepted a nil MCP server")
	}
}

func TestUnauthenticatedRequestsAreRejected(t *testing.T) {
	h := handler(t, testToken)

	cases := map[string]struct {
		header  string
		present bool
	}{
		"no Authorization header at all": {},
		"the wrong scheme":               {header: "Basic " + testToken},
		"the token with no scheme":       {header: testToken},
		"Bearer with nothing after it":   {header: "Bearer "},
		"a near miss on the token":       {header: "Bearer " + testToken[:len(testToken)-1] + "0"},
		"the right token, wrong case":    {header: "bearer " + strings.ToUpper(testToken)},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, Endpoint, strings.NewReader("{}"))
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				body, _ := io.ReadAll(rec.Body)
				t.Fatalf("status = %d, want 401 (body: %s)", rec.Code, body)
			}
			if got := rec.Header().Get("WWW-Authenticate"); !strings.HasPrefix(got, "Bearer") {
				t.Errorf("WWW-Authenticate = %q, want a Bearer challenge", got)
			}
			// The rejection must not hint at what was expected.
			if strings.Contains(rec.Body.String(), testToken) {
				t.Error("the rejection body leaked the expected token")
			}
		})
	}
}

// TestBearerTokenParsing covers the header grammar directly, because going through
// the middleware cannot tell "the scheme was rejected" from "the token did not match"
// — both are a 401, which is correct for a caller and useless for a test.
//
// The scheme is matched case-insensitively because RFC 7235 defines it that way: a
// client that sends "bearer" is not offering a wrong credential, and refusing it
// would produce a 401 that reads like a bad token.
func TestBearerTokenParsing(t *testing.T) {
	cases := []struct {
		header string
		want   string
		ok     bool
	}{
		{"Bearer " + testToken, testToken, true},
		{"bearer " + testToken, testToken, true},
		{"BEARER " + testToken, testToken, true},
		{"Bearer  " + testToken + "  ", testToken, true},
		{"", "", false},
		{"   ", "", false},
		{testToken, "", false},
		{"Basic " + testToken, "", false},
		{"Token " + testToken, "", false},
		{"Bearer", "", false},
		{"Bearer ", "", false},
		{"Bearer  ", "", false},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, Endpoint, nil)
		if tc.header != "" {
			req.Header.Set("Authorization", tc.header)
		}
		got, ok := bearerToken(req)
		if ok != tc.ok || got != tc.want {
			t.Errorf("bearerToken(%q) = %q, %v; want %q, %v", tc.header, got, ok, tc.want, tc.ok)
		}
	}
}

// TestAValidTokenReachesTheMCPHandler is the positive half: a correct credential must
// get through to the transport rather than being answered by the middleware.
//
// The assertion is that the response is not a 401. Anything the MCP handler itself
// says back is its business — an empty JSON-RPC body is a protocol error, not an
// authentication one, and confusing the two is how an auth test ends up passing for
// the wrong reason.
func TestAValidTokenReachesTheMCPHandler(t *testing.T) {
	h := handler(t, testToken)
	req := httptest.NewRequest(http.MethodPost, Endpoint, strings.NewReader(`{}`))
	req.Header.Set("Authorization", "bearer "+testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("a correct token was refused: %s", rec.Body.String())
	}
}

func TestLivenessNeedsNoTokenAndSaysNothing(t *testing.T) {
	h := handler(t, testToken)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if len(body) != 1 || body["status"] != "ok" {
		t.Errorf("the liveness body should say only that the process is up, got %v", body)
	}
	for _, leak := range []string{"fieldnode", "eu868", "8091", "dev_eui", "gateways"} {
		if strings.Contains(strings.ToLower(rec.Body.String()), leak) {
			t.Errorf("the unauthenticated liveness endpoint mentions %q", leak)
		}
	}
}

// TestUnknownPathNamesTheRealEndpoint: the frequent mistake is a client pointed at
// root, and a bare 404 sends the operator to the logs.
func TestUnknownPathNamesTheRealEndpoint(t *testing.T) {
	h := handler(t, testToken)
	for _, path := range []string{"/", "/sse", "/api/v1/devices"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), Endpoint) {
			t.Errorf("%s: the 404 should name %s, got %s", path, Endpoint, rec.Body.String())
		}
	}
}
