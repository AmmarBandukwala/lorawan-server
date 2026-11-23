package nsclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestEveryWriteHitsTheRouteAndMethodTheNetworkServerServes is the write half of the
// route pinning, and it matters more than the read half: a read that hits the wrong
// route answers 404, while a request with the wrong verb can be accepted by a handler
// that was never meant to run.
//
// The ns and mcp modules share no package, so none of this is a compile error.
func TestEveryWriteHitsTheRouteAndMethodTheNetworkServerServes(t *testing.T) {
	const eui = "0011223344556677"
	var seenMethod, seenPath, seenAuth, seenBody, seenQuery string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenMethod, seenPath, seenQuery = r.Method, r.URL.Path, r.URL.RawQuery
		seenAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		seenBody = string(body)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"queued":true}`))
	}))
	t.Cleanup(srv.Close)

	c, err := New(srv.URL, "0123456789abcdef0123456789")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	cases := []struct {
		name     string
		call     func() error
		method   string
		path     string
		wantBody string
	}{
		{
			name: "EnqueueDownlink", method: http.MethodPost,
			path: "/api/v1/devices/" + eui + "/downlink",
			call: func() error {
				_, _, err := c.EnqueueDownlink(ctx, eui,
					DownlinkRequest{Data: "0102", FPort: 10, Confirmed: true})
				return err
			},
			wantBody: `"data":"0102"`,
		},
		{
			// Cancel takes no body. Sending an empty object instead of nothing is
			// how a client ends up tripping a handler that rejects unexpected input.
			name: "CancelPendingDownlink", method: http.MethodDelete,
			path: "/api/v1/devices/" + eui + "/downlink",
			call: func() error {
				_, _, err := c.CancelPendingDownlink(ctx, eui)
				return err
			},
		},
		{
			name: "RequestDeviceStatus", method: http.MethodPost,
			path: "/api/v1/devices/" + eui + "/devstatus",
			call: func() error {
				_, _, err := c.RequestDeviceStatus(ctx, eui)
				return err
			},
		},
		{
			name: "PatchDevice", method: http.MethodPatch,
			path: "/api/v1/devices/" + eui,
			call: func() error {
				_, _, err := c.PatchDevice(ctx, eui, DevicePatch{Name: ptrOf("x")})
				return err
			},
			wantBody: `"name":"x"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seenBody = ""
			if err := tc.call(); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if seenMethod != tc.method {
				t.Errorf("method = %s, want %s", seenMethod, tc.method)
			}
			if seenPath != tc.path {
				t.Errorf("path = %q, want %q", seenPath, tc.path)
			}
			if seenQuery != "" {
				t.Errorf("query = %q, want none", seenQuery)
			}
			if want := "Bearer 0123456789abcdef0123456789"; seenAuth != want {
				t.Errorf("Authorization = %q, want %q", seenAuth, want)
			}
			switch {
			case tc.wantBody != "" && !strings.Contains(seenBody, tc.wantBody):
				t.Errorf("body = %s, want it to contain %s", seenBody, tc.wantBody)
			case tc.wantBody == "" && seenBody != "":
				t.Errorf("body = %s, want no body at all", seenBody)
			}
		})
	}
}

// TestWriteReturnsTheUpstreamStatusBecauseTwoHundredTwoIsThePoint: the caller needs
// 202 to tell "queued for a future window" from "done", and a client that flattened
// every success to 200 would erase the distinction the tool's text depends on.
func TestWriteReturnsTheUpstreamStatusBecauseTwoHundredTwoIsThePoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"queued":true,"explanation":"next RX window"}`))
	}))
	t.Cleanup(srv.Close)

	c, err := New(srv.URL, "0123456789abcdef0123456789")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	status, raw, err := c.EnqueueDownlink(context.Background(), "0011223344556677",
		DownlinkRequest{Data: "0102", FPort: 10})
	if err != nil {
		t.Fatalf("EnqueueDownlink: %v", err)
	}
	if status != http.StatusAccepted {
		t.Errorf("status = %d, want 202", status)
	}
	if !strings.Contains(string(raw), "next RX window") {
		t.Errorf("the body should come back verbatim, got %s", raw)
	}
}

// TestDevicePatchBodyOmitsWhatWasNotSupplied is the client-level half of the
// pointer-semantics guarantee, asserted on the encoded bytes rather than through a
// tool so a regression names this package as the cause.
func TestDevicePatchBodyOmitsWhatWasNotSupplied(t *testing.T) {
	got, err := DevicePatch{Name: ptrOf("kitchen")}.body()
	if err != nil {
		t.Fatalf("body: %v", err)
	}
	for _, mustNot := range []string{"enabled", "disable_adr", "location", "attributes",
		"model_id", "group", "max_data_rate", "min_data_rate", "max_tx_power",
		"supports_class_c"} {
		if strings.Contains(string(got), mustNot) {
			t.Errorf("body %s contains %q, which would overwrite a value the caller "+
				"never mentioned", got, mustNot)
		}
	}

	// A false that was asked for must survive, and so must an empty map meaning
	// "no attributes" and an explicit null meaning "clear the position".
	got, err = DevicePatch{Enabled: ptrOf(false), Attributes: &map[string]string{},
		LocationJSON: json.RawMessage("null")}.body()
	if err != nil {
		t.Fatalf("body: %v", err)
	}
	for _, must := range []string{`"enabled":false`, `"attributes":{}`, `"location":null`} {
		if !strings.Contains(string(got), must) {
			t.Errorf("body = %s, want it to contain %s", got, must)
		}
	}
}

func TestDevicePatchEmptyIsDetected(t *testing.T) {
	if !(DevicePatch{}).Empty() {
		t.Error("an all-nil patch should report itself empty")
	}
	// A zero value that was explicitly supplied is not empty.
	if (DevicePatch{Enabled: ptrOf(false)}).Empty() {
		t.Error("enabled:false is a change, not an absence")
	}
	if (DevicePatch{LocationJSON: json.RawMessage("null")}).Empty() {
		t.Error("an explicit null location is a change, not an absence")
	}
}

func ptrOf[T any](v T) *T { return &v }
