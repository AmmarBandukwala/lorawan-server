package nsclient

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestResponsesArePassedThroughUnchanged(t *testing.T) {
	// A field the network server adds must reach the caller. Decoding into a struct
	// here would have dropped it silently, which is the failure this client exists
	// to avoid.
	const body = `{"uplinks":[{"uplink_id":7,"brand_new_field":{"nested":true}}]}`
	c := newRecorderClient(t, &recorder{body: body})

	raw, err := c.ListUplinks(context.Background(), "", 0)
	if err != nil {
		t.Fatalf("ListUplinks: %v", err)
	}
	if string(raw) != body {
		t.Errorf("body = %s, want it verbatim", raw)
	}
}

func TestErrorStatusesBecomeTypedErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		check  func(error) bool
		want   string
	}{
		{
			name: "404 keeps the ns's own sentence", status: http.StatusNotFound,
			body:  `{"error":"dev_eui 0011223344556677 is not provisioned"}`,
			check: func(err error) bool { var nf *NotFoundError; return errors.As(err, &nf) },
			want:  "is not provisioned",
		},
		{
			name: "401 is an auth error", status: http.StatusUnauthorized,
			body:  `{"error":"invalid token"}`,
			check: func(err error) bool { var ae *AuthError; return errors.As(err, &ae) },
			want:  "invalid token",
		},
		{
			name: "403 is an auth error too", status: http.StatusForbidden,
			body:  `{"error":"token lacks that scope"}`,
			check: func(err error) bool { var ae *AuthError; return errors.As(err, &ae) },
			want:  "lacks that scope",
		},
		{
			name: "503 keeps its status code", status: http.StatusServiceUnavailable,
			body: `{"error":"event history is not available with this database configuration"}`,
			check: func(err error) bool {
				var ae *APIError
				return errors.As(err, &ae) && ae.Status == http.StatusServiceUnavailable
			},
			want: "not available with this database configuration",
		},
		{
			// A proxy in front of the ns answers in HTML. Swallowing that would leave
			// the agent with an empty reason and no way to know why.
			name: "an HTML error page is still reported", status: http.StatusBadGateway,
			body:  "<html><body>502 Bad Gateway</body></html>",
			check: func(err error) bool { var ae *APIError; return errors.As(err, &ae) },
			want:  "502 Bad Gateway",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newRecorderClient(t, &recorder{status: tc.status, body: tc.body})
			_, err := c.GetDevice(context.Background(), "0011223344556677")
			if err == nil {
				t.Fatal("want an error, got nil")
			}
			if !tc.check(err) {
				t.Errorf("wrong error type: %T (%v)", err, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

// TestHugeResponseIsRefusedNotTruncated: the device list is an unbounded query on
// the ns side. Half a JSON document is not JSON, and an agent handed one would
// report a truncated fleet as the whole fleet.
func TestHugeResponseIsRefusedNotTruncated(t *testing.T) {
	body := `{"devices":[` + strings.Repeat(`{"dev_eui":"0011223344556677"},`, 400_000) + `]}`
	c := newRecorderClient(t, &recorder{body: body})

	_, err := c.ListDevices(context.Background())
	if err == nil {
		t.Fatal("want a refusal for an oversized response, got nil")
	}
	if !strings.Contains(err.Error(), "larger than") {
		t.Errorf("error = %q, want it to name the size limit", err)
	}
}

// TestErrorDetailFallsBackToRaw covers the fallback branch: an error body that is
// not the ns's own shape must still reach the caller rather than becoming "".
func TestErrorDetailFallsBackToRaw(t *testing.T) {
	if got := errorDetail([]byte(`{"error":"named"}`)); got != "named" {
		t.Errorf("errorDetail = %q, want the named reason", got)
	}
	if got := errorDetail(nil); got != "no detail supplied" {
		t.Errorf("errorDetail(nil) = %q, want the placeholder", got)
	}
	long := strings.Repeat("x", 300)
	got := errorDetail([]byte(long))
	if !strings.HasSuffix(got, "…") {
		t.Errorf("a long body should be marked as truncated, got %q", got[len(got)-8:])
	}
	if len(got) > 210 {
		t.Errorf("a long body should be shortened, got %d bytes", len(got))
	}
	// An {"error":""} is not a reason; the raw text is more informative.
	if got := errorDetail([]byte(`{"error":""}`)); got == "" {
		t.Error("an empty error field must not become an empty reason")
	}
}
