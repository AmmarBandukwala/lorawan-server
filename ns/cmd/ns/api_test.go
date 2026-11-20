package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/gateway"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
)

const (
	testToken     = "0123456789abcdef0123456789abcdef"
	testDevEUIStr = "0011223344556677"
)

func mustEUI(t *testing.T, s string) lorawan.EUI64 {
	t.Helper()
	v, err := lorawan.ParseEUI64(s)
	if err != nil {
		t.Fatalf("ParseEUI64(%q): %v", s, err)
	}
	return v
}

// newTestAPI returns an API server backed by an in-memory store holding one
// joined device, so the handlers run against real engine state rather than a
// mock that could drift from it.
func newTestAPI(t *testing.T) (*apiServer, *engine.MemStore) {
	t.Helper()
	store := engine.NewMemStore()
	devEUI := mustEUI(t, testDevEUIStr)
	appKey, err := lorawan.ParseAES128Key("2b7e151628aed2a6abf7158809cf4f3c")
	if err != nil {
		t.Fatalf("parse the test key: %v", err)
	}
	ctx := context.Background()
	if err := store.SaveDevice(ctx,
		engine.NewOTAADevice(devEUI, mustEUI(t, "0102030405060708"), appKey)); err != nil {
		t.Fatalf("SaveDevice: %v", err)
	}
	// A session, because a device that has never joined cannot receive anything.
	if err := store.SaveSession(ctx, &engine.Session{
		DevEUI: devEUI, DevAddr: lorawan.DevAddr(0x01020304),
		NwkSKey: appKey, AppSKey: appKey,
		FCntUp: 7, FCntDown: 2, DataRate: 5, Region: "EU868",
		LastUplinkAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	cfg := engine.DefaultConfig()
	cfg.Region = "EU868"
	eng, err := engine.New(cfg, store.Store(), &gateway.Lazy{},
		engine.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	srv := newAPIServer("127.0.0.1:0", testToken, eng, store.Store().Devices,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	// The in-memory store serves models too, so the model endpoints are exercised
	// against the same repository the console would use. Traffic is deliberately
	// left nil: MemStore has no decoded columns, and the endpoint's 503 path is
	// worth covering.
	srv.models = store.Store().Models
	return srv, store
}

// do issues a request straight to the mux, without binding a port.
func do(srv *apiServer, method, path, token, body string) *httptest.ResponseRecorder {
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	return rec
}

func TestAPIRejectsMissingAndWrongTokens(t *testing.T) {
	srv, _ := newTestAPI(t)
	const path = "/api/v1/devices/" + testDevEUIStr + "/downlink"

	for name, hdr := range map[string]string{
		"wrong scheme":     "Basic " + testToken,
		"wrong token":      "Bearer wrong-token-wrong-token",
		"empty token":      "Bearer ",
		"missing space":    "Bearer" + testToken,
		"lowercase scheme": "bearer " + testToken,
	} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"f_port":10,"data":"ff"}`))
		req.Header.Set("Authorization", hdr)
		rec := httptest.NewRecorder()
		srv.http.Handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, rec.Code)
		}
		if rec.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%s: a 401 must carry WWW-Authenticate", name)
		}
	}

	// No header at all.
	rec := do(srv, http.MethodPost, path, "", `{"f_port":10,"data":"ff"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no header: status = %d, want 401", rec.Code)
	}
}

func TestAPIAcceptsTheRightToken(t *testing.T) {
	srv, store := newTestAPI(t)
	rec := do(srv, http.MethodPost, "/api/v1/devices/"+testDevEUIStr+"/downlink",
		testToken, `{"f_port":10,"data":"deadbeef"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body %s, want 202", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}

	ses, err := store.SessionByDevEUI(context.Background(), mustEUI(t, testDevEUIStr))
	if err != nil {
		t.Fatalf("SessionByDevEUI: %v", err)
	}
	if ses.Pending == nil {
		t.Fatal("the downlink was not queued")
	}
	if ses.Pending.FPort == nil || *ses.Pending.FPort != 10 {
		t.Errorf("pending FPort = %v, want 10", ses.Pending.FPort)
	}
	if string(ses.Pending.FRMPayload) != "\xde\xad\xbe\xef" {
		t.Errorf("pending payload = % x, want deadbeef", ses.Pending.FRMPayload)
	}
}

// TestAPIValidation covers the client errors that must be refused before the
// engine is touched: each is a request that would otherwise produce a frame the
// device cannot use.
func TestAPIValidation(t *testing.T) {
	srv, _ := newTestAPI(t)
	const path = "/api/v1/devices/" + testDevEUIStr + "/downlink"

	cases := []struct {
		name string
		body string
		want int
	}{
		{"not JSON", "nope", http.StatusBadRequest},
		{"missing f_port", `{"data":"ff"}`, http.StatusBadRequest},
		{"f_port 0 is MAC-only", `{"f_port":0,"data":"ff"}`, http.StatusBadRequest},
		{"f_port above 223", `{"f_port":224,"data":"ff"}`, http.StatusBadRequest},
		{"data not hex", `{"f_port":10,"data":"zz"}`, http.StatusBadRequest},
		{"odd hex length", `{"f_port":10,"data":"abc"}`, http.StatusBadRequest},
		{"empty data", `{"f_port":10,"data":""}`, http.StatusBadRequest},
		{"oversized data", `{"f_port":10,"data":"` + strings.Repeat("aa", 52) + `"}`,
			http.StatusBadRequest},
		{"the largest accepted payload", `{"f_port":10,"data":"` + strings.Repeat("aa", 51) + `"}`,
			http.StatusAccepted},
	}
	for _, tc := range cases {
		rec := do(srv, http.MethodPost, path, testToken, tc.body)
		if rec.Code != tc.want {
			t.Errorf("%s: status = %d body %s, want %d", tc.name, rec.Code, rec.Body, tc.want)
		}
		// Make room again: the engine keeps one pending frame per device, so a
		// case that succeeded would otherwise turn the next one into a 409.
		do(srv, http.MethodDelete, path, testToken, "")
	}
}

func TestAPIDeviceWithoutSessionIsNotFound(t *testing.T) {
	srv, _ := newTestAPI(t)
	rec := do(srv, http.MethodPost, "/api/v1/devices/9999999999999999/downlink",
		testToken, `{"f_port":10,"data":"ff"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d body %s, want 404", rec.Code, rec.Body)
	}
	// The message must say what to do about it.
	if !strings.Contains(rec.Body.String(), "join") {
		t.Errorf("body = %s, want a hint that the device must join first", rec.Body)
	}
}

func TestAPIRejectsAMalformedDevEUI(t *testing.T) {
	srv, _ := newTestAPI(t)
	for _, raw := range []string{"0011", "zz11223344556677", "00112233445566778899"} {
		rec := do(srv, http.MethodGet, "/api/v1/devices/"+raw+"/session", testToken, "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("dev_eui %q: status = %d, want 400", raw, rec.Code)
		}
	}
}

func TestAPISecondDownlinkConflictsAndCancelClearsIt(t *testing.T) {
	srv, _ := newTestAPI(t)
	const path = "/api/v1/devices/" + testDevEUIStr + "/downlink"

	if rec := do(srv, http.MethodPost, path, testToken, `{"f_port":10,"data":"ff"}`); rec.Code != http.StatusAccepted {
		t.Fatalf("first enqueue = %d, want 202", rec.Code)
	}
	rec := do(srv, http.MethodPost, path, testToken, `{"f_port":11,"data":"ee"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second enqueue = %d body %s, want 409", rec.Code, rec.Body)
	}
	if rec := do(srv, http.MethodDelete, path, testToken, ""); rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d, want 200", rec.Code)
	}
	if rec := do(srv, http.MethodPost, path, testToken, `{"f_port":11,"data":"ee"}`); rec.Code != http.StatusAccepted {
		t.Fatalf("enqueue after cancel = %d, want 202", rec.Code)
	}
}

func TestAPISessionView(t *testing.T) {
	srv, _ := newTestAPI(t)
	rec := do(srv, http.MethodGet, "/api/v1/devices/"+testDevEUIStr+"/session", testToken, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body)
	}
	var view map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode the session view: %v", err)
	}
	if view["dev_addr"] != "01020304" {
		t.Errorf("dev_addr = %v", view["dev_addr"])
	}
	if view["f_cnt_up"] != float64(7) || view["f_cnt_down"] != float64(2) {
		t.Errorf("counters = %v / %v", view["f_cnt_up"], view["f_cnt_down"])
	}
	if view["region"] != "EU868" {
		t.Errorf("region = %v", view["region"])
	}
	// Session keys must never appear on an operational endpoint.
	for _, banned := range []string{"nwk_s_key", "app_s_key", "nwks_key", "s_key", "key"} {
		if _, ok := view[banned]; ok {
			t.Errorf("the session view exposes %q", banned)
		}
	}
	if strings.Contains(rec.Body.String(), "2b7e151628aed2a6abf7158809cf4f3c") {
		t.Error("the session view leaks the application key")
	}
}

func TestAPIDevStatusIsScheduled(t *testing.T) {
	srv, store := newTestAPI(t)
	rec := do(srv, http.MethodPost, "/api/v1/devices/"+testDevEUIStr+"/devstatus", testToken, "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body %s, want 202", rec.Code, rec.Body)
	}
	ses, err := store.SessionByDevEUI(context.Background(), mustEUI(t, testDevEUIStr))
	if err != nil {
		t.Fatalf("SessionByDevEUI: %v", err)
	}
	if ses.Pending == nil || len(ses.Pending.MACCommands) == 0 {
		t.Errorf("no DevStatusReq was scheduled: %+v", ses.Pending)
	}
}

// TestAPIUnknownRouteIsNotFound guards the mux patterns: a mistyped path must
// not reach a handler with an empty path value.
//
// The device CRUD routes are deliberately not in this list — they are real now,
// and GET /api/v1/devices/{dev_eui} answers 200 rather than 404.
func TestAPIUnknownRouteIsNotFound(t *testing.T) {
	srv, _ := newTestAPI(t)
	for _, p := range []string{
		"/api/v1/devices/" + testDevEUIStr + "/unknown",
		"/api/v1/devices/" + testDevEUIStr + "/downlink/extra",
		"/api/v1/gateways",
		"/api/v1/session",
		"/",
		"/healthz",
	} {
		if rec := do(srv, http.MethodGet, p, testToken, ""); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", p, rec.Code)
		}
	}
}

// TestAPIMethodNotAllowed: DELETE on a POST-only route must not enqueue.
func TestAPIMethodNotAllowed(t *testing.T) {
	srv, store := newTestAPI(t)
	rec := do(srv, http.MethodGet, "/api/v1/devices/"+testDevEUIStr+"/downlink", testToken, "")
	if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 405 or 404", rec.Code)
	}
	ses, err := store.SessionByDevEUI(context.Background(), mustEUI(t, testDevEUIStr))
	if err != nil {
		t.Fatalf("SessionByDevEUI: %v", err)
	}
	if ses.Pending != nil {
		t.Error("a GET must not queue a downlink")
	}
}
