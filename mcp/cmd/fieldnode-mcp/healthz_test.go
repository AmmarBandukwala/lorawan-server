package main

import (
	"testing"

	"github.com/fieldnode/lorawan-server/mcp/internal/httpserver"
)

// TestHealthzURLKeepsTheProbePointedAtTheListener. A probe that resolves to the
// wrong address reports a healthy container as dead, which is worse than no probe:
// the orchestrator restarts a working process until it reaches backoff.
func TestHealthzURLKeepsTheProbePointedAtTheListener(t *testing.T) {
	cases := map[string]string{
		":8092":          "http://127.0.0.1:8092/healthz",
		"0.0.0.0:8092":   "http://127.0.0.1:8092/healthz",
		"127.0.0.1:8092": "http://127.0.0.1:8092/healthz",
		"10.0.0.5:8092":  "http://10.0.0.5:8092/healthz",
	}
	for addr, want := range cases {
		if got := healthzURL(addr); got != want {
			t.Errorf("healthzURL(%q) = %q, want %q", addr, got, want)
		}
	}

	// An address that cannot be probed must produce "" so runHealthz exits 2 with a
	// message, rather than issuing a request to a nonsense URL.
	for _, addr := range []string{"", "   ", "8092", ": ", "off"} {
		if got := healthzURL(addr); got != "" {
			t.Errorf("healthzURL(%q) = %q, want empty", addr, got)
		}
	}
}

// TestTheProbePathIsTheOneTheListenerServes: the binary and the handler agree by
// reference, and this is what keeps that true if either name changes.
func TestTheProbePathIsTheOneTheListenerServes(t *testing.T) {
	want := "http://127.0.0.1:8092" + httpserver.HealthPath
	if got := healthzURL(":8092"); got != want {
		t.Errorf("probe URL = %q, want %q", got, want)
	}
}
