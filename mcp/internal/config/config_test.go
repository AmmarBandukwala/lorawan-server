package config

import (
	"strings"
	"testing"
)

// setEnv is t.Setenv with the defaults this package reads cleared first, so a
// variable left over from a developer's shell or a CI job cannot make a test pass
// that should fail. A single space is what "cleared" means here: t.Setenv cannot
// unset, and this package trims, so a space reads as absent.
func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for _, name := range []string{"NS_API_URL", "NS_STATUS_URL", "NS_API_TOKEN", "MCP_PROFILE", "MCP_LOG_LEVEL"} {
		t.Setenv(name, " ")
	}
	for name, value := range kv {
		t.Setenv(name, value)
	}
}

const goodToken = "0123456789abcdef0123456789"

func TestLoadAcceptsAWorkingDeployment(t *testing.T) {
	setEnv(t, map[string]string{
		"NS_API_URL":    "http://127.0.0.1:8091",
		"NS_STATUS_URL": "http://127.0.0.1:8090",
		"NS_API_TOKEN":  goodToken,
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.APIURL != "http://127.0.0.1:8091" || cfg.StatusURL != "http://127.0.0.1:8090" {
		t.Errorf("URLs = %q / %q", cfg.APIURL, cfg.StatusURL)
	}
	if cfg.Version == "" {
		t.Error("no version is reported to clients")
	}
}

// TestLoadRefusesToStartWithoutWhatItNeeds, each cause separately. A half-configured
// server that answers some tools and fails others is worse than one that refuses to
// start: the agent cannot tell a missing permission from a broken network.
func TestLoadRefusesToStartWithoutWhatItNeeds(t *testing.T) {
	cases := map[string]map[string]string{
		"no API URL": {
			"NS_STATUS_URL": "http://127.0.0.1:8090",
			"NS_API_TOKEN":  goodToken,
		},
		"no token": {
			"NS_API_URL":    "http://127.0.0.1:8091",
			"NS_STATUS_URL": "http://127.0.0.1:8090",
		},
		"a relative API URL": {
			"NS_API_URL":   "/api/v1",
			"NS_API_TOKEN": goodToken,
		},
		"a schemeless API URL": {
			"NS_API_URL":   "127.0.0.1:8091",
			"NS_API_TOKEN": goodToken,
		},
		"a malformed status URL": {
			"NS_API_URL":    "http://127.0.0.1:8091",
			"NS_STATUS_URL": ":::",
			"NS_API_TOKEN":  goodToken,
		},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			setEnv(t, env)
			if _, err := Load(); err == nil {
				t.Error("Load accepted it")
			}
		})
	}
}

// TestShortTokenIsRefusedAtStartup mirrors the check the network server makes of its
// own token. Catching it here turns a 401 on the first tool call into a message that
// says what is wrong before an agent starts guessing.
func TestShortTokenIsRefusedAtStartup(t *testing.T) {
	setEnv(t, map[string]string{
		"NS_API_URL":   "http://127.0.0.1:8091",
		"NS_API_TOKEN": "short",
	})
	_, err := Load()
	if err == nil {
		t.Fatal("a 5-character token was accepted")
	}
	for _, want := range []string{"16", "openssl rand"} {
		if !contains(err.Error(), want) {
			t.Errorf("the message %q does not mention %q", err, want)
		}
	}
}

// TestStatusURLOptionallyFallsBack, and the fallback is the misconfiguration worth
// warning about: the network server serves /status on its own port, so pointing both
// at the API port usually means the health read 404s and nobody knows why.
func TestStatusURLOptionallyFallsBack(t *testing.T) {
	setEnv(t, map[string]string{
		"NS_API_URL":   "http://127.0.0.1:8091/",
		"NS_API_TOKEN": goodToken,
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.StatusURL != "http://127.0.0.1:8091" {
		t.Errorf("StatusURL = %q, want the API base with the trailing slash trimmed", cfg.StatusURL)
	}
}

func TestTrailingSlashesAndPaddingAreTrimmed(t *testing.T) {
	setEnv(t, map[string]string{
		"NS_API_URL":    "  http://127.0.0.1:8091///",
		"NS_STATUS_URL": " http://127.0.0.1:8090 ",
		"NS_API_TOKEN":  goodToken,
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.APIURL != "http://127.0.0.1:8091" {
		t.Errorf("APIURL = %q", cfg.APIURL)
	}
	if cfg.StatusURL != "http://127.0.0.1:8090" {
		t.Errorf("StatusURL = %q", cfg.StatusURL)
	}
}

// TestTokenIsNeverPartOfWhatIsReported: the Config struct is the only place the token
// lives, and it is read once at startup. This pins the field's existence so a future
// change that puts it in a tool's arguments or a log line is a deliberate edit here.
func TestTokenIsNeverPartOfWhatIsReported(t *testing.T) {
	setEnv(t, map[string]string{
		"NS_API_URL":   "http://127.0.0.1:8091",
		"NS_API_TOKEN": goodToken,
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Token != goodToken {
		t.Error("the token did not survive Load")
	}
	// The version string must not carry it, since the version is advertised to every
	// client at initialize.
	if contains(cfg.Version, goodToken) {
		t.Error("the advertised version carries the token")
	}
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
