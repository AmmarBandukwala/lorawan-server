package mcpclient

import "testing"

func TestConfigRejectsAnUnusableEndpoint(t *testing.T) {
	for name, cfg := range map[string]Config{
		"empty":       {Token: testToken},
		"no host":     {URL: "/mcp", Token: testToken},
		"no scheme":   {URL: "mcp:8092/mcp", Token: testToken},
		"short token": {URL: "http://mcp:8092/mcp", Token: "short"},
		"no token":    {URL: "http://mcp:8092/mcp"},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: New accepted it", name)
		}
	}
	// A trailing slash is cosmetic and must not be fatal, but must not produce a
	// doubled path either.
	client, err := New(Config{URL: "http://mcp:8092/mcp/", Token: testToken})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := client.Endpoint(); got != "http://mcp:8092/mcp" {
		t.Errorf("Endpoint() = %q", got)
	}
}

func TestHostOnly(t *testing.T) {
	for input, want := range map[string]string{
		"mcp:8092":        "mcp",
		"localhost":       "localhost",
		"[::1]:8092":      "[::1]",
		"[::1]":           "[::1]",
		"api.example.com": "api.example.com",
		"127.0.0.1:1":     "127.0.0.1",
	} {
		if got := hostOnly(input); got != want {
			t.Errorf("hostOnly(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestSameHostnameIgnoresPortButNotHost is the security property of the redirect
// guard: a port change on one host is a deployment detail, and a host change on one
// port is the token leak being guarded against.
func TestSameHostnameIgnoresPortButNotHost(t *testing.T) {
	if !sameHostname("mcp:8092", "mcp:9999") {
		t.Error("the same host on another port must count as the same host")
	}
	if !sameHostname("MCP:8092", "mcp:8092") {
		t.Error("hostnames are case-insensitive")
	}
	if sameHostname("mcp:8092", "evil:8092") {
		t.Error("a different host on the same port must not count as the same host")
	}
	if sameHostname("[::1]:8092", "[::2]:8092") {
		t.Error("two IPv6 literals must be compared as addresses, not as prefixes")
	}
}
