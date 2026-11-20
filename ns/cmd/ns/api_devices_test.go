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

// TestPatchDeviceRenamesWithoutCredentials is the reason PATCH exists: PUT is a
// replace that demands credentials, and this API never returns them, so without
// PATCH a client cannot rename a device it provisioned.
func TestPatchDeviceRenamesWithoutCredentials(t *testing.T) {
	srv, store := newTestAPI(t)

	rec := do(srv, http.MethodPatch, "/api/v1/devices/"+testDevEUIStr, testToken,
		`{"name":"Renamed boiler","group":"Boiler room"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var view deviceView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode the response: %v", err)
	}
	if view.Name != "Renamed boiler" || view.Group != "Boiler room" {
		t.Errorf("view = %+v, want the new name and group", view)
	}
	if !view.Enabled {
		t.Error("omitting enabled must leave the device enabled")
	}

	// The credentials survived the round trip. Losing the AppKey here would
	// store a device that can never join again, and the failure would surface
	// weeks later as a device that simply stops appearing.
	stored, err := store.DeviceByDevEUI(t.Context(), mustEUI(t, testDevEUIStr))
	if err != nil {
		t.Fatalf("DeviceByDevEUI: %v", err)
	}
	if stored.AppKey == (lorawan.AES128Key{}) {
		t.Error("the patch wiped the AppKey; the device can no longer join")
	}
	if stored.JoinEUI != mustEUI(t, "0102030405060708") {
		t.Errorf("JoinEUI = %s, want it carried over unchanged", stored.JoinEUI)
	}
}

// TestPatchResponseCarriesNoKeys repeats the provisioning endpoint's security
// rule on the endpoint an operator uses daily.
func TestPatchResponseCarriesNoKeys(t *testing.T) {
	srv, _ := newTestAPI(t)

	rec := do(srv, http.MethodPatch, "/api/v1/devices/"+testDevEUIStr, testToken, `{"name":"x"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
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
}

// TestPatchLeavesAbsentFieldsAlone is the pointer-fields contract: a request
// that changes only the name must not silently re-enable ADR or drop the device
// to class A.
func TestPatchLeavesAbsentFieldsAlone(t *testing.T) {
	srv, store := newTestAPI(t)
	ctx := t.Context()
	eui := mustEUI(t, testDevEUIStr)

	dev, err := store.DeviceByDevEUI(ctx, eui)
	if err != nil {
		t.Fatalf("DeviceByDevEUI: %v", err)
	}
	dev.DisableADR = true
	dev.SupportsClassC = true
	dev.MaxDataRate = 3
	if err := store.SaveDevice(ctx, dev); err != nil {
		t.Fatalf("SaveDevice: %v", err)
	}

	rec := do(srv, http.MethodPatch, "/api/v1/devices/"+testDevEUIStr, testToken, `{"name":"only the name"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	stored, err := store.DeviceByDevEUI(ctx, eui)
	if err != nil {
		t.Fatalf("DeviceByDevEUI: %v", err)
	}
	if !stored.DisableADR {
		t.Error("omitting disable_adr must not enable ADR")
	}
	if !stored.SupportsClassC {
		t.Error("omitting supports_class_c must not demote the device to class A")
	}
	if stored.MaxDataRate != 3 {
		t.Errorf("MaxDataRate = %d, want the stored 3", stored.MaxDataRate)
	}
	if stored.Name != "only the name" {
		t.Errorf("Name = %q, want the new name", stored.Name)
	}
}

func TestPatchDeviceToggles(t *testing.T) {
	srv, store := newTestAPI(t)

	// Disabling is how a staged rollout is held back: the device stays
	// provisioned but the engine refuses it.
	rec := do(srv, http.MethodPatch, "/api/v1/devices/"+testDevEUIStr, testToken,
		`{"enabled":false,"disable_adr":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	stored, err := store.DeviceByDevEUI(t.Context(), mustEUI(t, testDevEUIStr))
	if err != nil {
		t.Fatalf("DeviceByDevEUI: %v", err)
	}
	if stored.Enabled {
		t.Error("enabled:false was not applied")
	}
	if !stored.DisableADR {
		t.Error("disable_adr:true was not applied")
	}
}

func TestPatchDeviceRejectsBadValues(t *testing.T) {
	srv, store := newTestAPI(t)

	cases := map[string]string{
		"data rate above the band": `{"max_data_rate":16}`,
		"negative data rate":       `{"min_data_rate":-2}`,
		"tx power above the band":  `{"max_tx_power":99}`,
		"min above max":            `{"min_data_rate":6,"max_data_rate":3}`,
		"unknown field":            `{"name":"ok","app_key":"` + testAppKeyStr + `"}`,
		"trailing JSON":            `{"name":"ok"} {"name":"again"}`,
	}
	for label, body := range cases {
		rec := do(srv, http.MethodPatch, "/api/v1/devices/"+testDevEUIStr, testToken, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400: %s", label, rec.Code, rec.Body.String())
		}
	}

	// A rejected request must not have half-applied anything.
	stored, err := store.DeviceByDevEUI(t.Context(), mustEUI(t, testDevEUIStr))
	if err != nil {
		t.Fatalf("DeviceByDevEUI: %v", err)
	}
	if stored.Name != "" || stored.MaxDataRate != engine.UseRegionDefault ||
		stored.MinDataRate != engine.UseRegionDefault || stored.MaxTxPower != engine.UseRegionDefault {
		t.Errorf("a rejected patch changed the device: %+v", stored)
	}
	// In particular, the "unknown field" request must not have written the key
	// it tried to smuggle in, nor wiped the one on file.
	if stored.AppKey == (lorawan.AES128Key{}) {
		t.Error("the rejected patch wiped the AppKey")
	}
}

// TestPatchDeviceAcceptsRegionDefault pins -1 as the way to lift a limit. A
// console needs to say "no restriction" explicitly, and 0 would mean DR0.
func TestPatchDeviceAcceptsRegionDefault(t *testing.T) {
	srv, store := newTestAPI(t)

	if rec := do(srv, http.MethodPatch, "/api/v1/devices/"+testDevEUIStr, testToken,
		`{"max_data_rate":3}`); rec.Code != http.StatusOK {
		t.Fatalf("setting a limit: status = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := do(srv, http.MethodPatch, "/api/v1/devices/"+testDevEUIStr, testToken,
		`{"max_data_rate":-1}`); rec.Code != http.StatusOK {
		t.Fatalf("lifting the limit: status = %d: %s", rec.Code, rec.Body.String())
	}
	stored, err := store.DeviceByDevEUI(t.Context(), mustEUI(t, testDevEUIStr))
	if err != nil {
		t.Fatalf("DeviceByDevEUI: %v", err)
	}
	if stored.MaxDataRate != engine.UseRegionDefault {
		t.Errorf("MaxDataRate = %d, want UseRegionDefault (%d)", stored.MaxDataRate, engine.UseRegionDefault)
	}
}

func TestPatchUnknownDeviceIsNotFound(t *testing.T) {
	srv, _ := newTestAPI(t)
	rec := do(srv, http.MethodPatch, "/api/v1/devices/ffffffffffffffff", testToken, `{"name":"ghost"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

func TestPatchRequiresTheToken(t *testing.T) {
	srv, _ := newTestAPI(t)
	rec := do(srv, http.MethodPatch, "/api/v1/devices/"+testDevEUIStr, "", `{"name":"nope"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

// TestDeleteUnknownDeviceIsNotFound covers an EUI that was never provisioned,
// which the existing delete test does not: it deletes a device that existed.
func TestDeleteUnknownDeviceIsNotFound(t *testing.T) {
	srv, _ := newTestAPI(t)
	rec := do(srv, http.MethodDelete, "/api/v1/devices/ffffffffffffffff", testToken, "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

func TestDeleteRequiresTheToken(t *testing.T) {
	srv, _ := newTestAPI(t)
	rec := do(srv, http.MethodDelete, "/api/v1/devices/"+testDevEUIStr, "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	// Refusing on auth must not have deleted anything.
	if _, err := srv.devices.DeviceByDevEUI(t.Context(), mustEUI(t, testDevEUIStr)); err != nil {
		t.Errorf("an unauthorised delete removed the device: %v", err)
	}
}

// TestPatchStoresAttributesAndPosition covers the operator context: tags that
// describe what a device is for, and where it is installed.
func TestPatchStoresAttributesAndPosition(t *testing.T) {
	srv, store := newTestAPI(t)
	ctx := t.Context()
	eui := mustEUI(t, testDevEUIStr)

	rec := do(srv, http.MethodPatch, "/api/v1/devices/"+testDevEUIStr, testToken,
		`{"attributes":{"line":"3","Purpose":"cold store"},"location":{"latitude":51.5074,"longitude":-0.1278,"altitude":12}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	stored, err := store.DeviceByDevEUI(ctx, eui)
	if err != nil {
		t.Fatalf("DeviceByDevEUI: %v", err)
	}
	// Keys are normalised on the way in, so the console and the store agree on
	// how a tag is spelled.
	if stored.Attributes["line"] != "3" || stored.Attributes["purpose"] != "cold store" {
		t.Errorf("Attributes = %v, want lower-cased keys", stored.Attributes)
	}
	if stored.Location == nil {
		t.Fatal("Location was not stored")
	}
	if stored.Location.Latitude != 51.5074 || stored.Location.Longitude != -0.1278 ||
		stored.Location.Altitude != 12 {
		t.Errorf("Location = %+v", stored.Location)
	}

	// The response must show them too: an edit form reloads from it, and a field
	// that saves but does not read back looks like a save that did nothing.
	var view map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode the response: %v", err)
	}
	attrs, ok := view["attributes"].(map[string]any)
	if !ok || attrs["line"] != "3" {
		t.Errorf("response attributes = %v, want the stored pair", view["attributes"])
	}
	loc, ok := view["location"].(map[string]any)
	if !ok || loc["latitude"] != 51.5074 {
		t.Errorf("response location = %v", view["location"])
	}
}

// TestPatchNullLocationClearsIt is the three-state rule for a position: absent
// leaves it, null removes it, an object sets it. A typed field would read the
// first two as the same thing, and a position could never be removed.
func TestPatchNullLocationClearsIt(t *testing.T) {
	srv, store := newTestAPI(t)
	ctx := t.Context()
	eui := mustEUI(t, testDevEUIStr)

	if rec := do(srv, http.MethodPatch, "/api/v1/devices/"+testDevEUIStr, testToken,
		`{"location":{"latitude":1,"longitude":2}}`); rec.Code != http.StatusOK {
		t.Fatalf("set location: status = %d: %s", rec.Code, rec.Body.String())
	}
	// An unrelated patch must not disturb it.
	if rec := do(srv, http.MethodPatch, "/api/v1/devices/"+testDevEUIStr, testToken,
		`{"name":"renamed"}`); rec.Code != http.StatusOK {
		t.Fatalf("rename: status = %d: %s", rec.Code, rec.Body.String())
	}
	stored, err := store.DeviceByDevEUI(ctx, eui)
	if err != nil {
		t.Fatalf("DeviceByDevEUI: %v", err)
	}
	if stored.Location == nil {
		t.Fatal("omitting location cleared a stored position")
	}

	rec := do(srv, http.MethodPatch, "/api/v1/devices/"+testDevEUIStr, testToken, `{"location":null}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear location: status = %d: %s", rec.Code, rec.Body.String())
	}
	stored, err = store.DeviceByDevEUI(ctx, eui)
	if err != nil {
		t.Fatalf("DeviceByDevEUI: %v", err)
	}
	if stored.Location != nil {
		t.Errorf("location = %+v, want it cleared by an explicit null", stored.Location)
	}
	// The response omits the field rather than emitting a zeroed position, which
	// is what keeps 0,0 from appearing as a place in the Gulf of Guinea.
	if strings.Contains(rec.Body.String(), `"location"`) {
		t.Errorf("a cleared position must be omitted from the response: %s", rec.Body.String())
	}
}

// TestPatchEmptyAttributesClearsThem: an empty object is a deliberate clearing,
// distinct from omitting the field.
func TestPatchEmptyAttributesClearsThem(t *testing.T) {
	srv, store := newTestAPI(t)
	ctx := t.Context()
	eui := mustEUI(t, testDevEUIStr)

	if rec := do(srv, http.MethodPatch, "/api/v1/devices/"+testDevEUIStr, testToken,
		`{"attributes":{"line":"3"}}`); rec.Code != http.StatusOK {
		t.Fatalf("set attributes: status = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := do(srv, http.MethodPatch, "/api/v1/devices/"+testDevEUIStr, testToken,
		`{"name":"renamed"}`); rec.Code != http.StatusOK {
		t.Fatalf("rename: status = %d: %s", rec.Code, rec.Body.String())
	}
	stored, err := store.DeviceByDevEUI(ctx, eui)
	if err != nil {
		t.Fatalf("DeviceByDevEUI: %v", err)
	}
	if stored.Attributes["line"] != "3" {
		t.Errorf("omitting attributes dropped a stored tag: %v", stored.Attributes)
	}

	if rec := do(srv, http.MethodPatch, "/api/v1/devices/"+testDevEUIStr, testToken,
		`{"attributes":{}}`); rec.Code != http.StatusOK {
		t.Fatalf("clear attributes: status = %d: %s", rec.Code, rec.Body.String())
	}
	stored, err = store.DeviceByDevEUI(ctx, eui)
	if err != nil {
		t.Fatalf("DeviceByDevEUI: %v", err)
	}
	if len(stored.Attributes) != 0 {
		t.Errorf("Attributes = %v, want empty after an explicit clearing", stored.Attributes)
	}
}

func TestPatchRejectsUnusableContext(t *testing.T) {
	srv, _ := newTestAPI(t)
	cases := map[string]string{
		"latitude without longitude": `{"location":{"latitude":51.5}}`,
		"longitude without latitude": `{"location":{"longitude":-0.1}}`,
		"latitude out of range":      `{"location":{"latitude":91,"longitude":0}}`,
		"longitude out of range":     `{"location":{"latitude":0,"longitude":181}}`,
		// 9000 m is deliberately not in this list: the bound is -1000..10000 so
		// that Everest fits, and a value inside the range must be accepted.
		"altitude out of range":     `{"location":{"latitude":0,"longitude":0,"altitude":10001}}`,
		"empty attribute value":     `{"attributes":{"line":""}}`,
		"attribute key with spaces": `{"attributes":{"rooftop antenna":"1"}}`,
		"empty attribute key":       `{"attributes":{"":"value"}}`,
	}
	for name, body := range cases {
		rec := do(srv, http.MethodPatch, "/api/v1/devices/"+testDevEUIStr, testToken, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (%s)", name, rec.Code, rec.Body.String())
		}
	}
}

// TestPatchContextRejectionChangesNothing: a rejected field must not half-apply.
// The console shows the error and the operator fixes the other field, so a patch
// that stored the valid half would leave a device placed somewhere nobody typed.
func TestPatchContextRejectionChangesNothing(t *testing.T) {
	srv, store := newTestAPI(t)

	rec := do(srv, http.MethodPatch, "/api/v1/devices/"+testDevEUIStr, testToken,
		`{"name":"should not stick","location":{"latitude":91,"longitude":0}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	stored, err := store.DeviceByDevEUI(t.Context(), mustEUI(t, testDevEUIStr))
	if err != nil {
		t.Fatalf("DeviceByDevEUI: %v", err)
	}
	if stored.Name == "should not stick" {
		t.Error("a rejected patch still renamed the device")
	}
	if stored.Location != nil {
		t.Error("a rejected patch stored a position")
	}
}

// TestProvisionAcceptsContext covers creating a device with its context in one
// request, which is how a device arrives from a commissioning sheet.
func TestProvisionAcceptsContext(t *testing.T) {
	srv, store := newTestAPI(t)

	rec := do(srv, http.MethodPost, "/api/v1/devices", testToken, `{
		"dev_eui":"0011223344556699","join_eui":"0102030405060708",
		"app_key":"2b7e151628aed2a6abf7158809cf4f3c","name":"New sensor",
		"group":"Roof","attributes":{"rack":"b2"},
		"location":{"latitude":-33.86,"longitude":151.21,"altitude":40}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	stored, err := store.DeviceByDevEUI(t.Context(), mustEUI(t, "0011223344556699"))
	if err != nil {
		t.Fatalf("DeviceByDevEUI: %v", err)
	}
	if stored.Attributes["rack"] != "b2" {
		t.Errorf("Attributes = %v", stored.Attributes)
	}
	if stored.Location == nil || stored.Location.Altitude != 40 {
		t.Errorf("Location = %+v", stored.Location)
	}
	// Keys still never leave the server, whatever else the response carries.
	if strings.Contains(rec.Body.String(), "2b7e151628aed2a6abf7158809cf4f3c") {
		t.Error("the provisioning response echoed the AppKey")
	}
}

// TestProvisionRejectsHalfTypedPosition is the same rule as the patch path: a
// latitude alone cannot become a place.
func TestProvisionRejectsHalfTypedPosition(t *testing.T) {
	srv, _ := newTestAPI(t)

	rec := do(srv, http.MethodPost, "/api/v1/devices", testToken, `{
		"dev_eui":"0011223344556698","join_eui":"0102030405060708",
		"app_key":"2b7e151628aed2a6abf7158809cf4f3c","name":"Half typed",
		"location":{"latitude":51.5}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}
