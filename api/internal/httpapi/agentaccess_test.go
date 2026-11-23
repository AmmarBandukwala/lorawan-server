package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubAccess stands in for a prober so the handler can be tested without a socket,
// and so a specific failure can be provoked rather than hoped for.
type stubAccess struct {
	endpoint string
	err      error
}

func (s stubAccess) Endpoint() string            { return s.endpoint }
func (s stubAccess) Reach(context.Context) error { return s.err }

// TestAgentAccessDistinguishesAbsentFromBroken is the reason the endpoint exists.
//
// Both states would fit in one boolean, and one boolean would send an operator the
// wrong way: "not configured" means go and start the service, "configured but not
// answering" means something is set and is broken. Collapsing them into a 503 would
// be worse still, because the frontend's generic API-failure path would point the
// operator at the console instead.
func TestAgentAccessDistinguishesAbsentFromBroken(t *testing.T) {
	t.Run("no listener configured", func(t *testing.T) {
		rec := call(t, New(&stubRepository{}, nil, nil), http.MethodGet, "/api/v1/mcp", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var view agentAccessView
		if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
			t.Fatalf("body: %v", err)
		}
		if view.Configured || view.Reachable {
			t.Errorf("an unconfigured deployment reported %+v", view)
		}
		// The remedy has to be in the message: an operator staring at a page that
		// says "no" with no next step opens a terminal and starts guessing.
		if !strings.Contains(view.Detail, "MCP_URL") {
			t.Errorf("detail %q does not name the variable to set", view.Detail)
		}
	})

	t.Run("configured and answering", func(t *testing.T) {
		rec := call(t, New(&stubRepository{}, nil, nil,
			WithAgentAccess(stubAccess{endpoint: "http://mcp:8092"})),
			http.MethodGet, "/api/v1/mcp", "")
		var view agentAccessView
		if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
			t.Fatalf("body: %v", err)
		}
		if !view.Configured || !view.Reachable {
			t.Errorf("a healthy listener reported %+v", view)
		}
		if view.Endpoint != "http://mcp:8092" {
			t.Errorf("endpoint = %q, want the address that was probed", view.Endpoint)
		}
		if view.Detail != "" {
			t.Errorf("a healthy check should carry no complaint, got %q", view.Detail)
		}
	})

	t.Run("configured and broken", func(t *testing.T) {
		rec := call(t, New(&stubRepository{}, nil, nil,
			WithAgentAccess(stubAccess{endpoint: "http://mcp:8092",
				err: errors.New("dial tcp: connection refused")})),
			http.MethodGet, "/api/v1/mcp", "")
		var view agentAccessView
		if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
			t.Fatalf("body: %v", err)
		}
		if !view.Configured || view.Reachable {
			t.Errorf("a refused probe reported %+v", view)
		}
		if !strings.Contains(view.Detail, "mcp:8092") {
			t.Errorf("detail %q does not name what was probed", view.Detail)
		}
	})
}

// TestAgentAccessIgnoresNilOption keeps the composition rule main relies on:
// WithAgentAccess(NewAgentAccess("")) must be a no-op rather than a Server that
// reports a listener at an empty address.
func TestAgentAccessIgnoresNilOption(t *testing.T) {
	if NewAgentAccess("") != nil {
		t.Error(`NewAgentAccess("") should be nil so the option is a no-op`)
	}
	if NewAgentAccess("   ") != nil {
		t.Error("a blank variable is the same as an unset one")
	}
	if s := New(&stubRepository{}, nil, nil, WithAgentAccess(nil)); s.agentAccess != nil {
		t.Error("a nil AgentAccess was installed and would probe an empty URL")
	}
	if got := NewAgentAccess("http://mcp:8092/").Endpoint(); got != "http://mcp:8092" {
		t.Errorf("trailing slash survived: %q", got)
	}
}

// TestReachRejectsAPortCollision: a 200 from something that is not the MCP health
// body must not be reported as a healthy listener. Without this check a console
// pointed at the wrong port would be satisfied by any service that answers 200 —
// including the console API's own health path, which is the likeliest wrong port of
// all — and would tell the operator the listener is fine.
func TestReachRejectsAPortCollision(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
		ok     bool
	}{
		"the real health body":      {http.StatusOK, `{"status":"ok"}`, true},
		"200 with a different body": {http.StatusOK, `{"status":"healthy"}`, false},
		"200 with no body":          {http.StatusOK, ``, false},
		"200 with HTML":             {http.StatusOK, `<html>ok</html>`, false},
		"an auth wall":              {http.StatusUnauthorized, `{"error":"a bearer token is required"}`, false},
		"a wrong path":              {http.StatusNotFound, `{"error":"no endpoint"}`, false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer upstream.Close()

			err := NewAgentAccess(upstream.URL).Reach(context.Background())
			if tc.ok && err != nil {
				t.Errorf("a real health report was rejected: %v", err)
			}
			if !tc.ok && err == nil {
				t.Errorf("%s was accepted as a healthy MCP listener", name)
			}
		})
	}
}

// TestReachAsksTheHealthPath pins the path, because the listener serves nothing
// else without a credential and a wrong path here reads to the operator as "the MCP
// server is broken" when it is the console that is wrong.
func TestReachAsksTheHealthPath(t *testing.T) {
	var got string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer upstream.Close()

	if err := NewAgentAccess(upstream.URL).Reach(context.Background()); err != nil {
		t.Fatalf("Reach: %v", err)
	}
	if got != "/healthz" {
		t.Errorf("probed %q, want /healthz", got)
	}
}

// TestAgentAccessIsReadOnly: a page about a door must not itself be a way in.
func TestAgentAccessIsReadOnly(t *testing.T) {
	rec := call(t, New(&stubRepository{}, nil, nil), http.MethodPost, "/api/v1/mcp", `{}`)
	if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
		t.Errorf("POST /api/v1/mcp returned %d, want a refusal", rec.Code)
	}
}

// TestProbeNeverCarriesACredential. The console does not hold the MCP token, by
// design: the two secrets are separate so that leaking one does not open the other.
// This asserts the probe cannot acquire one even by accident, because the day
// someone "just passes the token through for convenience" the split is gone and
// nothing else would notice.
func TestProbeNeverCarriesACredential(t *testing.T) {
	var auth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer upstream.Close()

	if err := NewAgentAccess(upstream.URL).Reach(context.Background()); err != nil {
		t.Fatalf("Reach: %v", err)
	}
	if auth != "" {
		t.Errorf("the probe sent an Authorization header: %q", auth)
	}
}

// TestProbeFailuresSayDifferentThings guards the wording an operator acts on.
//
// Go renders a refused connection, an unresolvable name and a timeout as similar
// dial errors, and the console used to collapse the last two into a raw error dump.
// They call for opposite responses — one means the service never started, the other
// means the URL belongs to something else — so the sentences must differ, and each
// must name the address that was probed.
func TestProbeFailuresSayDifferentThings(t *testing.T) {
	const endpoint = "http://mcp:8092"

	cases := []struct {
		name string
		err  error
		want []string
	}{
		{
			name: "unresolvable name",
			err:  errors.New(`Get "http://mcp:8092/healthz": dial tcp: lookup mcp on 127.0.0.11:53: no such host`),
			want: []string{"nothing resolves", endpoint, "--profile mcp"},
		},
		{
			name: "refused connection",
			err:  errors.New(`Get "http://mcp:8092/healthz": dial tcp 127.0.0.1:8092: connect: connection refused`),
			want: []string{"nothing is listening", endpoint},
		},
		{
			name: "timeout",
			err:  context.DeadlineExceeded,
			want: []string{"no answer within", endpoint, "another service"},
		},
		{
			name: "anything else keeps the address and the cause",
			err:  errors.New("tls: bad certificate"),
			want: []string{endpoint, "bad certificate"},
		},
	}

	seen := map[string]string{}
	for _, tc := range cases {
		got := describeProbeFailure(tc.err, endpoint)
		for _, want := range tc.want {
			if !strings.Contains(got, want) {
				t.Errorf("%s: message %q does not mention %q", tc.name, got, want)
			}
		}
		seen[tc.name] = got
	}

	// The point of the whole function: the two most common mistakes must not read
	// the same, or the operator cannot tell which container to look at.
	if seen["unresolvable name"] == seen["refused connection"] {
		t.Error("an unresolvable host and a refused port produce the same advice")
	}
}
