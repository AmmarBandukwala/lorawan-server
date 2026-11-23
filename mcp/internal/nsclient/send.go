package nsclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// send is the write path, and it returns the status code because a write's meaning
// lives there: 202 means "queued for a window that has not happened yet", 200 means
// "done". A tool that flattens the two loses the distinction the network server was
// careful enough to make.
//
// A non-2xx still decodes {"error": "..."} into a typed error, so the network server's
// sentence — which for a 409 explains the one-pending-frame rule and what to do about
// it — reaches the model instead of being replaced by a bare status number.
func (c *Client) send(ctx context.Context, method, path string, q url.Values, body any) (int, json.RawMessage, error) {
	target := c.apiURL + path
	if len(q) > 0 {
		target += "?" + q.Encode()
	}

	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("nsclient: encode the request body: %w", err)
		}
		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return 0, nil, fmt.Errorf("nsclient: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("nsclient: cannot reach the network server at %s: %w", c.apiURL, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("nsclient: read response from %s: %w", target, err)
	}
	if len(raw) > maxResponseBytes {
		return resp.StatusCode, nil, fmt.Errorf(
			"nsclient: the response from %s is larger than %d bytes", target, maxResponseBytes)
	}
	if resp.StatusCode >= 300 {
		return resp.StatusCode, nil, statusError(resp.StatusCode, raw)
	}
	return resp.StatusCode, json.RawMessage(raw), nil
}

// statusError maps a status onto the typed errors the tools switch on. Split out so
// the read path and the write path cannot disagree about what a 404 means.
func statusError(code int, raw []byte) error {
	detail := errorDetail(raw)
	switch code {
	case http.StatusNotFound:
		return &NotFoundError{Detail: detail}
	case http.StatusUnauthorized, http.StatusForbidden:
		return &AuthError{Detail: detail}
	default:
		return &APIError{Status: code, Detail: detail}
	}
}

// ConflictError reports whether the network server refused because the device already
// has a frame pending. It is a distinct case rather than a generic APIError because
// the right response is a different tool call — cancel first — and not a retry.
func ConflictError(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusConflict
}
