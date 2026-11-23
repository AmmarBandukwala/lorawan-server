package mcpclient

import (
	"net/http"
	"strings"
)

// bearerTransport attaches the listener's token to every request.
//
// It is a round-tripper rather than a per-request header setter because the SDK builds
// the requests — including the initialize call and every JSON-RPC post after it — and
// this package does not get a hook into any of them. Setting the header here means the
// token cannot be forgotten on one code path, which is the failure mode this pattern
// exists to prevent.
type bearerTransport struct {
	token string
	next  http.RoundTripper
}

func (t *bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	// Cloned rather than mutated: http.RoundTripper is required not to modify the
	// request it was handed, and the SDK may retry or reuse it.
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+t.token)

	response, err := t.next.RoundTrip(clone)
	if err != nil {
		return nil, err
	}

	// A redirect that carries the Authorization header to a different host would move
	// the token somewhere it was never meant to go. Go's client strips the header on a
	// cross-host redirect it follows, but that is a behaviour of the standard library
	// rather than a contract of this package, and the consequence of it changing is a
	// leaked token. Refusing outright is the cheaper guarantee.
	if response.Request != nil && !sameHostname(request.URL.Host, response.Request.URL.Host) {
		return nil, &RedirectError{
			From: request.URL.Host,
			To:   response.Request.URL.Host,
		}
	}
	return response, nil
}

// RedirectError reports that the listener redirected to another host. Neither the
// token nor the target's path appears in the message: the hostnames are the facts an
// operator needs in order to fix the configuration.
type RedirectError struct{ From, To string }

func (e *RedirectError) Error() string {
	return "the MCP listener redirected from " + e.From + " to " + e.To +
		"; the token is not sent across hosts, so the configured URL is probably wrong"
}

// sameHostname compares the host part of two host:port values, ignoring the port.
//
// The port is ignored deliberately. A redirect that stays on one host is a deployment
// detail, not a token leak; one that changes host is the thing being guarded against,
// and comparing the full host:port would let an attacker's host through merely by
// listening on the same port number.
func sameHostname(a, b string) bool {
	return strings.EqualFold(hostOnly(a), hostOnly(b))
}

// hostOnly strips a port, tolerating a bare host and an IPv6 literal in brackets.
func hostOnly(hostPort string) string {
	// An IPv6 literal is [::1]:8092, so the port separator is the colon *after* the
	// closing bracket; splitting on the last colon would truncate the address.
	if strings.HasPrefix(hostPort, "[") {
		if end := strings.Index(hostPort, "]"); end > 0 {
			return hostPort[:end+1]
		}
		return hostPort
	}
	if i := strings.LastIndex(hostPort, ":"); i > 0 {
		return hostPort[:i]
	}
	return hostPort
}
