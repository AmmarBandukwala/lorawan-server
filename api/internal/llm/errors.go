package llm

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// maxErrorBytes bounds how much of an error body is read. A gateway in front of the
// runtime can return a full HTML page, and quoting all of it into a log line or a
// tool result helps nobody.
const maxErrorBytes = 2 << 10

// statusError turns a rejection into a typed error carrying the endpoint's own words.
func statusError(response *http.Response) error {
	detail := readDetail(response.Body)
	switch response.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return &AuthError{Detail: detail}
	case http.StatusTooManyRequests:
		return &RateLimitError{Detail: detail}
	default:
		return &APIError{Status: response.StatusCode, Detail: detail}
	}
}

// readDetail pulls the provider's message out of the {"error":{"message":...}} shape
// it uses, falling back to the raw body so a proxy's HTML page is still reported
// rather than swallowed into an empty string.
func readDetail(body io.Reader) string {
	raw, err := io.ReadAll(io.LimitReader(body, maxErrorBytes))
	if err != nil {
		return "the endpoint returned an error body this server could not read"
	}
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) == nil {
		if msg := strings.TrimSpace(envelope.Error.Message); msg != "" {
			if kind := strings.TrimSpace(envelope.Error.Type); kind != "" {
				return msg + " (" + kind + ")"
			}
			return msg
		}
	}
	// Ollama answers with {"error":"model not found"} rather than the nested shape,
	// and a bare string is worth handling because it is what a misconfigured
	// deployment actually returns.
	var flat struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &flat) == nil && strings.TrimSpace(flat.Error) != "" {
		return strings.TrimSpace(flat.Error)
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return "no detail supplied"
	}
	if len(text) > 300 {
		text = text[:300] + "…"
	}
	return text
}

// unwrapURLError strips the `Post "http://...": ` wrapper Go adds, leaving the cause.
// The address is already in the message this produces, so keeping the wrapper would
// print it twice.
func unwrapURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return urlErr.Err
	}
	return err
}
