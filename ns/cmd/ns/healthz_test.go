package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHealthzURL(t *testing.T) {
	cases := map[string]string{
		":8090":          "http://127.0.0.1:8090/healthz",
		"0.0.0.0:8090":   "http://127.0.0.1:8090/healthz",
		"127.0.0.1:8090": "http://127.0.0.1:8090/healthz",
		"127.0.0.1:9999": "http://127.0.0.1:9999/healthz",
	}
	for addr, want := range cases {
		if got := healthzURL(addr); got != want {
			t.Errorf("healthzURL(%q) = %q, want %q", addr, got, want)
		}
	}
	// A disabled endpoint must be reported, not probed at a bogus address.
	for _, addr := range []string{"", "  ", "off", "OFF", "not-an-address"} {
		if got := healthzURL(addr); got != "" {
			t.Errorf("healthzURL(%q) = %q, want empty", addr, got)
		}
	}
}

func TestRunHealthzSucceedsAgainstALiveServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	}))
	defer srv.Close()

	if code := runHealthz(strings.TrimPrefix(srv.URL, "http://"), 2*time.Second); code != 0 {
		t.Errorf("runHealthz = %d, want 0", code)
	}
}

func TestRunHealthzFailsWhenNothingIsListening(t *testing.T) {
	// Port 1 on loopback is not served; the probe must report failure rather
	// than hang, because a health check that hangs stalls the orchestrator.
	start := time.Now()
	code := runHealthz("127.0.0.1:1", 500*time.Millisecond)
	if code == 0 {
		t.Error("runHealthz reported success against a closed port")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("the probe took %v; it must respect its timeout", elapsed)
	}
}

func TestRunHealthzRejectsADisabledEndpoint(t *testing.T) {
	if code := runHealthz("off", time.Second); code != 2 {
		t.Errorf("runHealthz = %d, want 2 (usage error, distinct from a failed probe)", code)
	}
}
