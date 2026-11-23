package llm

import (
	"io"
	"net/http"
)

// readAll drains a request body to a string, for tests that assert on what was sent.
func readAll(r *http.Request) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	return string(raw), err
}
