// Package httpserver serves the same MCP server that stdio serves, over HTTP.
//
// It exists for a host that cannot spawn a process — a remote agent, a browser
// client, an agent running in a container with no shell to exec. Everything else is
// unchanged: same tools, same profile, same upstream, same redaction. A transport is
// not a security boundary, and this package does not pretend to add one; it adds the
// one thing a network listener requires and stdio did not need, which is proving who
// is calling.
//
// Three decisions worth stating:
//
// The listener gets its own token (MCP_AUTH_TOKEN), deliberately not NS_API_TOKEN.
// Reusing it would mean one leaked string opens both the network API and this door,
// and rotating either would break the other. It also means access can be revoked
// here without touching the network server.
//
// The endpoint is stateless. Protocol revision 2026-07-28 moved away from a session
// handshake, and nothing here needs to call back into a client: no sampling, no
// roots, no server-initiated logging — all three deprecated. A stateless listener
// cannot leak an idle session, which is the failure mode a long-running
// network-facing MCP server most often dies of.
//
// There is no rate limit here. The honest reason is that a useful one needs to know
// who the callers are, and a single shared bearer token cannot distinguish two of
// them. The network server is what would suffer, so it is what has to set the limit;
// the README says what to put in front of this if it ever leaves the internal network.
package httpserver

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Endpoint is the path the MCP handler is mounted on. Clients that default to root
// are pointed at it by name rather than left to guess.
const Endpoint = "/mcp"

// HealthPath is the unauthenticated liveness path. It is exported because the binary
// probes itself with it — the runtime image is distroless and has no curl, so the
// probe and the handler must agree on the path by reference rather than by copy.
const HealthPath = "/healthz"

// minTokenLen is the floor the network server applies to its own token, and the same
// reasoning holds here: a short bearer token on a network is brute-forceable, and
// this one guards a process that can make devices transmit.
const minTokenLen = 16

// Config is what a listener needs.
type Config struct {
	// BindAddr is the address to listen on, e.g. ":8092". Binding anything other
	// than loopback publishes this endpoint to whatever network the process is on,
	// so it is stated rather than defaulted.
	BindAddr string

	// AuthToken is the bearer token every MCP request must present.
	AuthToken string

	// Logger receives admission decisions. Never the token.
	Logger *slog.Logger
}

// Validate rejects a configuration that would start and then be wrong.
func (c Config) Validate() error {
	if strings.TrimSpace(c.BindAddr) == "" {
		return fmt.Errorf("an HTTP address is required, e.g. :8092")
	}
	token := strings.TrimSpace(c.AuthToken)
	if token == "" {
		return fmt.Errorf("MCP_AUTH_TOKEN is required: this is a network listener, and " +
			"an unauthenticated one would hand the whole network to anything that can " +
			"reach the port")
	}
	if len(token) < minTokenLen {
		return fmt.Errorf("MCP_AUTH_TOKEN must be at least %d characters; generate one "+
			"with: openssl rand -hex 32", minTokenLen)
	}
	return nil
}

// NewHandler builds the HTTP surface: the MCP endpoint behind a bearer token, plus an
// unauthenticated liveness path.
func NewHandler(server *mcp.Server, cfg Config) (http.Handler, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if server == nil {
		return nil, fmt.Errorf("httpserver: an MCP server is required")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	streamable := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{
			// Stateless, for the reason in the package comment. The SDK notes
			// that in this mode GET and DELETE answer 405, which is correct:
			// there is no session to resume and nothing to delete.
			Stateless: true,
			Logger:    logger,
		},
	)

	mux := http.NewServeMux()
	// Liveness only, and deliberately unauthenticated: an orchestrator
	// health-checking this service should not need a secret, and should not learn
	// anything about the network from the answer. This says the process is up.
	// Whether the network server is reachable is a different question, answered by
	// that server's own /status.
	mux.HandleFunc(HealthPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.Handle(Endpoint, requireBearer(cfg.AuthToken, logger, streamable))
	// Anything else gets the path it asked for, because the common misconfiguration
	// is a client pointed at root. Naming the real path costs nothing and saves the
	// operator a debugging session; that this server answers to /mcp is not a secret.
	mux.HandleFunc("/", unknownPath)

	return mux, nil
}

func unknownPath(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": fmt.Sprintf("no endpoint at %s; the MCP endpoint is %s and requires a "+
			"bearer token", r.URL.Path, Endpoint),
	})
}

// requireBearer admits a request only on an exact bearer-token match.
//
// The comparison is constant-time. That is not the main defence — a 32-byte random
// token is — but a listener on a shared network should not hand out a timing oracle
// for free either.
//
// A rejection carries WWW-Authenticate so a client can tell "you sent nothing" from
// "you sent the wrong thing", and the body says nothing about what was expected.
func requireBearer(token string, logger *slog.Logger, next http.Handler) http.Handler {
	expected := []byte(strings.TrimSpace(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided, ok := bearerToken(r)
		if !ok || subtle.ConstantTimeCompare([]byte(provided), expected) != 1 {
			// Logged with the remote address and nothing identifying about the
			// token: enough to spot a misconfigured host or a brute-force attempt,
			// never enough to reconstruct either token.
			logger.Warn("mcp http: rejected a request",
				"remote", r.RemoteAddr, "path", r.URL.Path, "presented", ok)
			w.Header().Set("WWW-Authenticate", `Bearer realm="fieldnode-lorawan"`)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": "a bearer token is required; this MCP listener authenticates " +
					"every request",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bearerToken pulls the token out of an Authorization header, accepting only the
// Bearer scheme. A Basic header is rejected rather than decoded: accepting either
// invites a client to configure one and silently get the other.
func bearerToken(r *http.Request) (string, bool) {
	raw := strings.TrimSpace(r.Header.Get("Authorization"))
	if raw == "" {
		return "", false
	}
	scheme, value, found := strings.Cut(raw, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	value = strings.TrimSpace(value)
	return value, value != ""
}

// NewServer builds the http.Server with the timeouts worth setting here and the one
// that must not be.
//
// ReadHeaderTimeout guards against a slow-header connection. WriteTimeout is left at
// zero deliberately: responses stream as server-sent events, and a write deadline
// would cut a long tool call off mid-answer, which the client sees as a dropped
// connection rather than as a timeout it could report.
//
// It takes an address rather than a Config because the handler has already been
// built from the authenticated one; handing this function a Config without a token
// would look like a validation failure waiting to happen.
func NewServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
}
