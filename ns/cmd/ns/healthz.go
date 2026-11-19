package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// runHealthz answers a container health check by asking the running process its
// own status endpoint, then exits 0 or 1.
//
// The image is distroless: there is no shell, no curl and no wget, so the binary
// has to be the probe. `/ns -healthz` costs one loopback request and proves the
// thing a port check does not — that the HTTP listener is actually serving.
func runHealthz(addr string, timeout time.Duration) int {
	url := healthzURL(addr)
	if url == "" {
		fmt.Fprintf(os.Stderr, "ns: -healthz needs a status address; NS_STATUS_ADDR is %q\n", addr)
		return 2
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ns: %s is not answering: %v\n", url, err)
		return 1
	}
	defer resp.Body.Close()
	// The body must be drained or the connection cannot be reused; the probe
	// runs every few seconds, so this is not free to ignore.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "ns: %s replied %s: %s\n", url, resp.Status, strings.TrimSpace(string(body)))
		return 1
	}
	fmt.Println("ok")
	return 0
}

// healthzURL turns a listen address into a loopback URL. ":8090" and
// "0.0.0.0:8090" both mean "probe myself on 127.0.0.1".
func healthzURL(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" || strings.EqualFold(addr, "off") {
		return ""
	}
	host, port, err := splitHostPort(addr)
	if err != nil {
		return ""
	}
	switch host {
	case "", "*", "0.0.0.0", "[::]", "::":
		host = "127.0.0.1"
	}
	return "http://" + host + ":" + port + "/healthz"
}

func splitHostPort(addr string) (host, port string, err error) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return "", "", fmt.Errorf("ns: %q is not a host:port address", addr)
	}
	return addr[:i], addr[i+1:], nil
}
