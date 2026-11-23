package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/fieldnode/lorawan-server/mcp/internal/httpserver"
)

// runHealthz asks this process's own listener whether it is up and returns a process
// exit code. It exists because the runtime image is distroless: there is no shell and
// no curl in it, so the binary has to be its own probe, which is what the network
// server does too.
//
// It deliberately does not consult the network server. A probe that failed whenever
// the ns was down would report this process as broken when it is not, and an
// orchestrator would restart a healthy container until it hit backoff — while the
// thing actually needing attention, the network server, sat unrestarted and
// unmentioned. Liveness of this process and health of the network are different
// questions with different responders.
func runHealthz(addr string, timeout time.Duration) int {
	url := healthzURL(addr)
	if url == "" {
		fmt.Fprintf(os.Stderr,
			"fieldnode-mcp: -healthz needs a listener; MCP_HTTP_ADDR is %q, and this "+
				"process serves stdio when it is empty\n", addr)
		return 2
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fieldnode-mcp: %s is not answering: %v\n", url, err)
		return 1
	}
	defer resp.Body.Close()
	// Drained so the connection can be reused; the probe runs every few seconds,
	// which is not free to ignore.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "fieldnode-mcp: %s replied %s: %s\n",
			url, resp.Status, strings.TrimSpace(string(body)))
		return 1
	}
	fmt.Println("ok")
	return 0
}

// healthzURL turns a listen address into a loopback URL. ":8092" and "0.0.0.0:8092"
// both mean "probe myself on 127.0.0.1".
//
// A host other than a bare port or a wildcard is left alone: someone binding to a
// specific interface address wants the probe to go to that address, and rewriting it
// to loopback would report a healthy listener as dead.
func healthzURL(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	host, port, found := strings.Cut(addr, ":")
	if !found || port == "" {
		return ""
	}
	switch host {
	case "", "0.0.0.0", "[::]", "::", "*":
		host = "127.0.0.1"
	}
	return "http://" + host + ":" + port + httpserver.HealthPath
}
