package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// healthPath is the one path the MCP listener answers without a credential. It is
// deliberately the only thing the console can learn about that process: the listener
// exposes nothing else unauthenticated, and this service does not hold its token.
const healthPath = "/healthz"

// probeTimeout bounds the reachability check. The listener is on the same internal
// network, so an answer slower than this is not a slow server but a dead socket or a
// container that never started. The page would rather say "not answering" in two
// seconds than hang long enough for the browser to give up and blame the console.
const probeTimeout = 2 * time.Second

// AgentAccess is the console's view of the MCP listener.
//
// It is a port rather than an inline fetch so the handler can be tested without a
// socket, and so "there is no listener to ask" is a value (a nil AgentAccess) rather
// than a special case every caller has to remember.
type AgentAccess interface {
	// Endpoint is the base URL the console was configured with. It is reported to
	// the page so an operator debugging a misconfiguration can see what was probed
	// instead of guessing which environment variable was wrong.
	Endpoint() string

	// Reach reports whether the listener answered its health path.
	Reach(ctx context.Context) error
}

// mcpProber is the AgentAccess that asks a real listener.
type mcpProber struct {
	base   string
	client *http.Client
}

// NewAgentAccess builds a prober for a base URL. An empty URL yields nil, meaning
// this deployment has no MCP listener configured, which the page reports as a
// distinct state from "configured but not answering" — the operator's next action
// differs, so collapsing them would waste their time.
func NewAgentAccess(rawURL string) AgentAccess {
	trimmed := strings.TrimRight(strings.TrimSpace(rawURL), "/")
	if trimmed == "" {
		return nil
	}
	return &mcpProber{base: trimmed, client: &http.Client{Timeout: probeTimeout}}
}

func (p *mcpProber) Endpoint() string { return p.base }

func (p *mcpProber) Reach(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+healthPath, nil)
	if err != nil {
		return err
	}
	response, err := p.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	// Drained so the connection can be returned to the pool. The page re-reads on
	// a manual refresh, which is not frequent, but leaving a connection half-read
	// is free to avoid and annoying to debug.
	body, _ := io.ReadAll(io.LimitReader(response.Body, 256))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("the listener replied %s", response.Status)
	}
	// A 200 from something that is not the MCP server's health body means a port
	// collision or a proxy in the way. Naming what came back is the difference
	// between an operator finding the misconfigured URL in a minute and an
	// afternoon.
	var payload struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Status != "ok" {
		return fmt.Errorf("answered 200 but not with a health report (%s)",
			strings.TrimSpace(string(body)))
	}
	return nil
}

// agentAccessView is what the page is told about the MCP listener.
//
// Configured and reachable are separate fields because they are separate facts with
// separate remedies. "No listener is configured" means the deployment never started
// one and the operator should read the setup instructions; "configured but not
// answering" means something is set and is broken, and the endpoint field says what
// to go and look at.
type agentAccessView struct {
	Configured bool   `json:"configured"`
	Reachable  bool   `json:"reachable"`
	Endpoint   string `json:"endpoint"`
	Detail     string `json:"detail"`
	CheckedAt  string `json:"checked_at"`
}

// getAgentAccess serves GET /api/v1/mcp.
//
// It answers 200 even when the listener is down. The console API is healthy in that
// moment — it successfully established that something else is not — and a 503 here
// would land in the frontend's generic "API unavailable" path, sending an operator to
// debug the console while the MCP container is the thing that never started.
func (s *Server) getAgentAccess(w http.ResponseWriter, r *http.Request) {
	if s.agentAccess == nil {
		writeJSON(w, http.StatusOK, agentAccessView{
			Configured: false,
			Detail: "this console was not told where an MCP listener is; set MCP_URL " +
				"on the console API to have it check",
			CheckedAt: time.Now().UTC().Format(time.RFC3339),
		})
		return
	}

	view := agentAccessView{
		Configured: true,
		Endpoint:   s.agentAccess.Endpoint(),
		CheckedAt:  time.Now().UTC().Format(time.RFC3339),
	}

	ctx, cancel := context.WithTimeout(r.Context(), probeTimeout)
	defer cancel()
	if err := s.agentAccess.Reach(ctx); err != nil {
		view.Detail = describeProbeFailure(err, s.agentAccess.Endpoint())
	} else {
		view.Reachable = true
	}
	writeJSON(w, http.StatusOK, view)
}

// describeProbeFailure turns a transport error into something an operator acts on.
//
// A refused connection, an unresolvable name and a timeout look similar in Go's error
// text and mean different things here: refused means nothing is listening on that
// port, no-such-host means the address belongs to no service at all — usually a
// container that was never started, since compose DNS only answers for running
// services — and a timeout means something owns the address but is not this. Naming
// which is which is the difference between a one-minute fix and an afternoon.
func describeProbeFailure(err error, endpoint string) string {
	text := err.Error()
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Sprintf("no answer within %s from %s. Something owns that address "+
			"but is not answering a health check, so the URL is probably pointing at "+
			"another service", probeTimeout, endpoint)
	case strings.Contains(text, "no such host"):
		return fmt.Sprintf("nothing resolves the host in %s. On a compose deployment "+
			"that usually means the MCP service is not running: its name only answers "+
			"once the container exists. Start it with `docker compose --profile mcp up -d`",
			endpoint)
	case strings.Contains(text, "connection refused"):
		return fmt.Sprintf("nothing is listening at %s. The MCP service is not "+
			"running, or it is on a port other than the one the console was given", endpoint)
	default:
		return fmt.Sprintf("%s: %v", endpoint, err)
	}
}
