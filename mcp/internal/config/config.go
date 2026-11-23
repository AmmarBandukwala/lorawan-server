// Package config reads the environment this MCP server runs on.
//
// It is deliberately small and fixed at startup. The process holds a bearer token
// that can read every device on the network, so what it points at is a deployment
// decision, not something a tool call can steer.
package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
)

// minTokenLen mirrors the check the network server makes of its own token: it
// refuses to start with a shorter one, so anything below this length here cannot
// authenticate against any ns that is actually running. Catching it at startup
// turns a confusing 401 on the first tool call into a message that says what is
// wrong before an agent starts guessing.
const minTokenLen = 16

// Config is the resolved startup configuration.
type Config struct {
	// APIURL is the network server's application API — the token-protected one on
	// :8091. Every read goes here.
	APIURL string

	// StatusURL is where /status is served. It is normally a different port from
	// APIURL, because the network server keeps its unauthenticated health
	// endpoint apart from the authenticated API on purpose.
	StatusURL string

	// Token is the bearer token. It comes from the environment and is never
	// logged, never returned by a tool, and never part of a tool's arguments —
	// which is why it is configuration and not an input.
	Token string

	// Version is reported to clients as this server's implementation version.
	Version string
}

// Load reads and validates the environment.
//
// Every failure is fatal on purpose. A half-configured MCP server that answers
// some tools and 503s on others is worse than one that refuses to start: the
// agent cannot tell a missing permission from a broken network, and it will keep
// trying.
func Load() (Config, error) {
	cfg := Config{
		APIURL:    trim(os.Getenv("NS_API_URL")),
		StatusURL: trim(os.Getenv("NS_STATUS_URL")),
		Token:     trim(os.Getenv("NS_API_TOKEN")),
		Version:   "0.1.0",
	}

	if cfg.APIURL == "" {
		return Config{}, fmt.Errorf("NS_API_URL is required, e.g. http://127.0.0.1:8091")
	}
	if err := absolute("NS_API_URL", cfg.APIURL); err != nil {
		return Config{}, err
	}
	if cfg.StatusURL != "" {
		if err := absolute("NS_STATUS_URL", cfg.StatusURL); err != nil {
			return Config{}, err
		}
	}
	if cfg.Token == "" {
		return Config{}, fmt.Errorf("NS_API_TOKEN is required: every read this server " +
			"makes goes through the network server's authenticated API")
	}
	if len(cfg.Token) < minTokenLen {
		return Config{}, fmt.Errorf("NS_API_TOKEN must be at least %d characters; "+
			"the network server refuses to start with a shorter one, so this token "+
			"could never authenticate (generate one with: openssl rand -hex 32)", minTokenLen)
	}

	if cfg.StatusURL == "" {
		cfg.StatusURL = cfg.APIURL
		// Worth a warning rather than silence: the network server serves /status
		// on its own port, so pointing both at the API port usually means the
		// health read will 404 and nobody will know why.
		slog.Warn("NS_STATUS_URL is not set; reading /status from the API base URL. "+
			"Set NS_STATUS_URL when the network server serves it on its status port",
			"base", cfg.APIURL)
	}
	return cfg, nil
}

func trim(s string) string {
	return strings.TrimRight(strings.TrimSpace(s), "/")
}

func absolute(name, raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s is not a valid URL: %w", name, err)
	}
	if parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("%s must be an absolute http(s) URL, got %q", name, raw)
	}
	return nil
}
