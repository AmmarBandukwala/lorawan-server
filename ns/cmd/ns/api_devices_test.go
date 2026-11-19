package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
)

const (
	testAppKeyStr  = "2b7e151628aed2a6abf7158809cf4f3c"
	testNwkSKeyStr = "00112233445566778899aabbccddeeff"
	testAppSKeyStr = "ffeeddccbbaa99887766554433221100"
)

// TestProvisionOTAADeviceNeverEchoesKeys is the security property of this
// endpoint. A key that appears in a response body ends up in proxy logs and
// support tickets, and an AppKey in the wild means every future join of that
// device can be forged.
func TestProvisionOTAADeviceNeverEchoesKeys(t *testing.T) {
	srv, store := newTestAPI(t)

	body := `{"dev_eui":"aabbccddeeff0011","join_eui":"0102030405060708",` +
		`"name":"Tank 3","app_key":"` + testAppKeyStr + `"}`
	rec := do(srv, http.MethodPost, "/api/v1/devices", testToken, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	got := rec.Body.String()
	if strings.Contains(strings.ToLower(got), testAppKeyStr) {
		t.Errorf("the response leaks the AppKey: %s", got)
	}
	for _, forbidden := range []string{"app_key", "nwk_s_key", "app_s_key"} {
		if strings.Contains(got, `"`+forbidden+`"`) {
			t.Errorf("the response carries a key field %q: %s", forbidden, got)
		}
	}

	var view deviceView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode the response: %v", err)
	}
	if view.DevEUI != "AABBCCDDEEFF0011" || view.Activation != "otaa" || view.Name != "Tank 3" {
		t.Errorf("view = %+v", view)
	}
	if !view.Enabled {
		t.Error("a newly provisioned device must be enabled by default")
	}
	if view.DevAddr != "" {
		t.Errorf("an OTAA device must not report a DevAddr: %s", view.DevAddr)
	}

	// And it really was stored, with the keys the operator supplied.
	stored, err := store.DeviceByDevEUI(t.Context(), mustEUI(t, "aabbccddeeff0011"))
	if err != nil {
		t.Fatalf("the device was not stored: %v", err)
	}
	if stored.AppKey.String() != strings.ToUpper(testAppKeyStr) {
		t.Errorf("stored AppKey = %s, want the one supplied", stored.AppKey)
	}
	if stored.JoinEUI != mustEUI(t, "0102030405060708") {
		t.Errorf("stored JoinEUI = %s", stored.JoinEUI)
	}
}

// TestProvisionWithoutJoinEUIStoresTheWildcard covers what the console does when
// the operator cannot read a JoinEUI off the device. The engine already treats a
// zero JoinEUI as "accept any" instead of comparing it, so an omitted field has
// to mean that rather than be rejected: forcing a guess in would produce
// join_eui_mismatch drops that read like a radio fault.
func TestProvisionWithoutJoinEUIStoresTheWildcard(t *testing.T) {
	srv, store := newTestAPI(t)

	body := `{"dev_eui":"aabbccddeeff0011","name":"Tank 3","app_key":"` + testAppKeyStr + `"}`
	rec := do(srv, http.MethodPost, "/api/v1/devices", testToken, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("an omitted join_eui must be accepted: %d %s", rec.Code, rec.Body.String())
	}

	stored, err := store.DeviceByDevEUI(t.Context(), mustEUI(t, "aabbccddeeff0011"))
	if err != nil {
		t.Fatalf("the device was not stored: %v", err)
	}
	if stored.JoinEUI != (lorawan.EUI64{}) {
		t.Errorf("stored JoinEUI = %s, want the zero value that means accept-any", stored.JoinEUI)
	}
	// A blank string is the same thing: the console sends what it was given.
	rec = do(srv, http.MethodPost, "/api/v1/devices", testToken,
		`{"dev_eui":"1122334455667788","join_eui":"  ","app_key":"`+testAppKeyStr+`"}`)
	if rec.Code != http.StatusCreated {
		t.Errorf("a blank join_eui must be accepted: %d %s", rec.Code, rec.Body.String())
	}
	// A wrong value is still a mistake worth refusing.
	rec = do(srv, http.MethodPost, "/api/v1/devices", testToken,
		`{"dev_eui":"2233445566778899","join_eui":"nope","app_key":"`+testAppKeyStr+`"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status for a malformed join_eui = %d, want 400", rec.Code)
	}
}

func TestProvisionABPDevice(t *testing.T) {
	srv, _ := newTestAPI(t)

	body := `{"dev_eui":"aabbccddeeff0011","join_eui":"0102030405060708",` +
		`"dev_addr":"01020304","nwk_s_key":"` + testNwkSKeyStr +
		`","app_s_key":"` + testAppSKeyStr + `"}`
	rec := do(srv, http.MethodPost, "/api/v1/devices", testToken, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	got := rec.Body.String()
	for _, secret := range []string{testNwkSKeyStr, testAppSKeyStr} {
		if strings.Contains(strings.ToLower(got), secret) {
			t.Errorf("the response leaks a session key: %s", got)
		}
	}

	var view deviceView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.Activation != "abp" || view.DevAddr != "01020304" {
		t.Errorf("view = %+v, want abp with the given DevAddr", view)
	}
}

// TestProvisionRejectsAmbiguousCredentials: accepting whatever fields arrived
// would turn a half-filled ABP request into an OTAA device that never joins,
// which reads like a radio fault rather than a typo.
func TestProvisionRejectsAmbiguousCredentials(t *testing.T) {
	srv, _ := newTestAPI(t)
	ok := `{"dev_eui":"aabbccddeeff0011","join_eui":"0102030405060708","app_key":"` + testAppKeyStr + `"}`
	cases := map[string]string{
		"both credential sets": `{"dev_eui":"aabbccddeeff0011","join_eui":"0102030405060708",` +
			`"app_key":"` + testAppKeyStr + `","dev_addr":"01020304",` +
			`"nwk_s_key":"` + testNwkSKeyStr + `","app_s_key":"` + testAppSKeyStr + `"}`,
		"no credentials":  `{"dev_eui":"aabbccddeeff0011","join_eui":"0102030405060708"}`,
		"partial ABP":     `{"dev_eui":"aabbccddeeff0011","join_eui":"0102030405060708","dev_addr":"01020304"}`,
		"short AppKey":    `{"dev_eui":"aabbccddeeff0011","join_eui":"0102030405060708","app_key":"aabb"}`,
		"bad DevEUI":      `{"dev_eui":"nope","join_eui":"0102030405060708","app_key":"` + testAppKeyStr + `"}`,
		"bad JoinEUI":     `{"dev_eui":"aabbccddeeff0011","join_eui":"nope","app_key":"` + testAppKeyStr + `"}`,
		"unknown field":   ok[:len(ok)-1] + `,"nope":1}`,
		"missing DevEUI":  `{"join_eui":"0102030405060708","app_key":"` + testAppKeyStr + `"}`,
		"trailing second": ok + `{}`,
	}
	for name, body := range cases {
		rec := do(srv, http.MethodPost, "/api/v1/devices", testToken, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (%s)", name, rec.Code, rec.Body.String())
		}
	}
}

func TestProvisionRequiresAuth(t *testing.T) {
	srv, _ := newTestAPI(t)
	body := `{"dev_eui":"aabbccddeeff0011","join_eui":"0102030405060708","app_key":"` + testAppKeyStr + `"}`
	for _, token := range []string{"", "wrong-token"} {
		rec := do(srv, http.MethodPost, "/api/v1/devices", token, body)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("token %q: status = %d, want 401", token, rec.Code)
		}
	}
	if rec := do(srv, http.MethodGet, "/api/v1/devices", "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated list: status = %d, want 401", rec.Code)
	}
}

// TestListDevicesOmitsKeys is the property an operator depends on every day: the
// list endpoint is what gets screenshotted into tickets and rendered by the
// dashboard, so it must be structurally incapable of carrying a key.
func TestListDevicesOmitsKeys(t *testing.T) {
	srv, _ := newTestAPI(t)

	rec := do(srv, http.MethodGet, "/api/v1/devices", testToken, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	got := rec.Body.String()
	for _, forbidden := range []string{"app_key", "nwk_s_key", "app_s_key", testAppKeyStr} {
		if strings.Contains(strings.ToLower(got), strings.ToLower(forbidden)) {
			t.Errorf("the list response leaks %q: %s", forbidden, got)
		}
	}

	var views []deviceView
	if err := json.Unmarshal(rec.Body.Bytes(), &views); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("got %d devices, want the 1 seeded device", len(views))
	}
	if views[0].DevEUI != strings.ToUpper(testDevEUIStr) || views[0].Activation != "otaa" {
		t.Errorf("view = %+v", views[0])
	}
	// The region-default sentinels must survive the round trip: a dashboard that
	// read 0 as "no channels allowed" would misreport every device.
	if views[0].MaxDataRate != engine.UseRegionDefault || views[0].MinDataRate != engine.UseRegionDefault {
		t.Errorf("data-rate defaults = %d/%d, want %d/%d",
			views[0].MaxDataRate, views[0].MinDataRate,
			engine.UseRegionDefault, engine.UseRegionDefault)
	}
}

// TestCreateRefusesToOverwriteAnExistingDevice pins the difference between POST
// and PUT. An upsert here means a mistyped DevEUI rewrites a live device's keys;
// the real device then stops joining and nothing in the log says why.
func TestCreateRefusesToOverwriteAnExistingDevice(t *testing.T) {
	srv, store := newTestAPI(t)

	body := `{"dev_eui":"` + testDevEUIStr + `","join_eui":"0102030405060708",` +
		`"name":"Impostor","app_key":"00112233445566778899aabbccddeeff"}`
	rec := do(srv, http.MethodPost, "/api/v1/devices", testToken, body)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "PUT") {
		t.Errorf("a 409 that does not name the way out is a dead end: %s", rec.Body.String())
	}

	// The refusal must be total: the original keys and name are untouched.
	stored, err := store.DeviceByDevEUI(t.Context(), mustEUI(t, testDevEUIStr))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored.AppKey.String() != strings.ToUpper(testAppKeyStr) {
		t.Errorf("the existing AppKey was overwritten with %s", stored.AppKey)
	}
	if stored.Name == "Impostor" {
		t.Error("the existing device record was modified by a refused POST")
	}
}

// TestUpdatePreservesJoinState guards a replay-protection property that is
// invisible until it is broken. NextJoinNonce and the DevNonce log are engine
// state, not operator input; a PUT that reset them would make a join-accept
// captured months ago valid again.
func TestUpdatePreservesJoinState(t *testing.T) {
	srv, store := newTestAPI(t)
	ctx := t.Context()
	devEUI := mustEUI(t, testDevEUIStr)
	joinEUI := mustEUI(t, "0102030405060708")

	stored, err := store.DeviceByDevEUI(ctx, devEUI)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	stored.NextJoinNonce = 41
	stored.UsedDevNonces = map[lorawan.EUI64][]uint16{joinEUI: {7, 13}}
	if err := store.SaveDevice(ctx, stored); err != nil {
		t.Fatalf("seed the join state: %v", err)
	}

	body := `{"dev_eui":"` + testDevEUIStr + `","join_eui":"0102030405060708",` +
		`"name":"Tank 3 relocated","app_key":"` + testAppKeyStr + `","disable_adr":true}`
	rec := do(srv, http.MethodPut, "/api/v1/devices/"+testDevEUIStr, testToken, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	updated, err := store.DeviceByDevEUI(ctx, devEUI)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if updated.NextJoinNonce != 41 {
		t.Errorf("NextJoinNonce = %d after PUT, want 41: resetting it lets a captured join-accept be replayed",
			updated.NextJoinNonce)
	}
	if got := updated.UsedDevNonces[joinEUI]; len(got) != 2 || got[0] != 7 || got[1] != 13 {
		t.Errorf("UsedDevNonces = %v after PUT, want the recorded [7 13]", got)
	}
	// And the fields the operator did change actually changed.
	if updated.Name != "Tank 3 relocated" || !updated.DisableADR {
		t.Errorf("the PUT did not apply: %+v", updated)
	}
}

// TestGetDeviceServesOneRecord covers the endpoint a dashboard uses to render a
// device page: fetch by DevEUI without scanning the inventory, and without keys.
func TestGetDeviceServesOneRecord(t *testing.T) {
	srv, _ := newTestAPI(t)

	rec := do(srv, http.MethodGet, "/api/v1/devices/"+testDevEUIStr, testToken, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), testAppKeyStr) {
		t.Errorf("the response leaks the AppKey: %s", rec.Body.String())
	}
	var view deviceView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.DevEUI != strings.ToUpper(testDevEUIStr) {
		t.Errorf("DevEUI = %q, want %q", view.DevEUI, strings.ToUpper(testDevEUIStr))
	}

	// Unknown device: a 404 that names the address, which is how an operator
	// distinguishes "never provisioned" from "typo".
	rec = do(srv, http.MethodGet, "/api/v1/devices/a1b2c3d4e5f60718", testToken, "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown device: status = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "A1B2C3D4E5F60718") {
		t.Errorf("the 404 should name the address: %s", rec.Body.String())
	}

	// A malformed address is the caller's mistake, not a missing record.
	if rec := do(srv, http.MethodGet, "/api/v1/devices/not-an-eui", testToken, ""); rec.Code != http.StatusBadRequest {
		t.Errorf("malformed dev_eui: status = %d, want 400", rec.Code)
	}
	// And it is behind the same token as everything else.
	if rec := do(srv, http.MethodGet, "/api/v1/devices/"+testDevEUIStr, "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated get: status = %d, want 401", rec.Code)
	}
}

func TestUpdateRejectsPathBodyMismatchAndUnknownDevice(t *testing.T) {
	srv, _ := newTestAPI(t)

	// Aimed at one device, addressed to another: the path must not silently win,
	// or a request meant for a spare unit overwrites a device in the field.
	mismatch := `{"dev_eui":"ffffffffffffffff","join_eui":"0102030405060708",` +
		`"app_key":"` + testAppKeyStr + `"}`
	rec := do(srv, http.MethodPut, "/api/v1/devices/"+testDevEUIStr, testToken, mismatch)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("mismatched dev_eui: status = %d, want 400: %s", rec.Code, rec.Body.String())
	}

	unknown := `{"dev_eui":"a1b2c3d4e5f60718","join_eui":"0102030405060708",` +
		`"app_key":"` + testAppKeyStr + `"}`
	rec = do(srv, http.MethodPut, "/api/v1/devices/a1b2c3d4e5f60718", testToken, unknown)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown device: status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

// TestDeleteRemovesTheSessionToo is what makes DELETE a revocation rather than a
// rename. A surviving session would let a withdrawn device keep exchanging
// frames with keys the operator just removed.
func TestDeleteRemovesTheSessionToo(t *testing.T) {
	srv, store := newTestAPI(t)
	ctx := t.Context()
	devEUI := mustEUI(t, testDevEUIStr)

	// The fixture gives the device a session; confirm that before relying on it.
	if _, err := store.SessionByDevEUI(ctx, devEUI); err != nil {
		t.Fatalf("the fixture has no session to revoke: %v", err)
	}

	rec := do(srv, http.MethodDelete, "/api/v1/devices/"+testDevEUIStr, testToken, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if _, err := store.DeviceByDevEUI(ctx, devEUI); err == nil {
		t.Error("the device record survived DELETE")
	}
	if _, err := store.SessionByDevEUI(ctx, devEUI); err == nil {
		t.Error("the session survived DELETE: the device can still talk with withdrawn keys")
	}

	// A second DELETE is a 404, not a panic and not a silent success.
	if rec := do(srv, http.MethodDelete, "/api/v1/devices/"+testDevEUIStr, testToken, ""); rec.Code != http.StatusNotFound {
		t.Errorf("second DELETE: status = %d, want 404", rec.Code)
	}
}

// TestCreateRejectsUnknownFields: a misspelled field ("appkey") would otherwise
// create a device with no credentials that never joins, and the operator would
// spend the evening debugging radio instead of the typo.
func TestCreateRejectsUnknownFields(t *testing.T) {
	srv, _ := newTestAPI(t)
	body := `{"dev_eui":"aabbccddeeff0011","join_eui":"0102030405060708",` +
		`"appkey":"` + testAppKeyStr + `"}`
	rec := do(srv, http.MethodPost, "/api/v1/devices", testToken, body)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "appkey") {
		t.Errorf("the error should name the offending field: %s", rec.Body.String())
	}
}

// TestCreateRejectsMalformedCredentials covers the field-specific messages, which
// is the whole reason this endpoint validates instead of parsing loosely.
func TestCreateRejectsMalformedCredentials(t *testing.T) {
	srv, _ := newTestAPI(t)
	cases := map[string]string{
		"short dev_eui":  `{"dev_eui":"aabb","join_eui":"0102030405060708","app_key":"` + testAppKeyStr + `"}`,
		"bad join_eui":   `{"dev_eui":"aabbccddeeff0011","join_eui":"zz02030405060708","app_key":"` + testAppKeyStr + `"}`,
		"short app_key":  `{"dev_eui":"aabbccddeeff0011","join_eui":"0102030405060708","app_key":"0011"}`,
		"partial ABP":    `{"dev_eui":"aabbccddeeff0011","join_eui":"0102030405060708","dev_addr":"01020304"}`,
		"no credentials": `{"dev_eui":"aabbccddeeff0011","join_eui":"0102030405060708"}`,
		"both key sets": `{"dev_eui":"aabbccddeeff0011","join_eui":"0102030405060708",` +
			`"app_key":"` + testAppKeyStr + `","dev_addr":"01020304",` +
			`"nwk_s_key":"` + testNwkSKeyStr + `","app_s_key":"` + testAppSKeyStr + `"}`,
	}
	for name, body := range cases {
		rec := do(srv, http.MethodPost, "/api/v1/devices", testToken, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400: %s", name, rec.Code, rec.Body.String())
		}
	}
}
