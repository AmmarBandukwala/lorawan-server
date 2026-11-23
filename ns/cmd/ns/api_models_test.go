package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/pgstore"
)

// constProbeScript is a vendor formatter in the shape vendors actually ship.
const constProbeScript = `function decodeUplink(input) {
  return { data: { temperature: (input.bytes[0] | (input.bytes[1] << 8)) / 10 } };
}`

// modelBody encodes a save-model request. Hand-written JSON cannot carry a
// multi-line formatter: a literal newline inside a JSON string is invalid, and
// every vendor snippet has several.
func modelBody(name, kind, script string) string {
	raw, err := json.Marshal(saveModelRequest{
		Name: name, Description: "DS18B20 on port 10", CodecKind: kind, CodecScript: script,
	})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func TestCreateAndReadDeviceModel(t *testing.T) {
	srv, store := newTestAPI(t)

	rec := do(srv, http.MethodPost, "/api/v1/models/cold-chain-probe", testToken,
		modelBody("Cold-chain probe v2", "JS", constProbeScript))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}

	var view modelView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode the response: %v", err)
	}
	if view.ModelID != "cold-chain-probe" || view.Name != "Cold-chain probe v2" {
		t.Errorf("view = %+v", view)
	}
	if view.CodecKind != "JS" || !view.CodecPresent {
		t.Errorf("codec fields = %+v", view)
	}
	// The script is returned: it is vendor documentation, not a secret, and an
	// operator cannot audit what they cannot see.
	if !strings.Contains(view.CodecScript, "decodeUplink") {
		t.Errorf("the stored script is missing from the response: %q", view.CodecScript)
	}

	stored, err := store.ModelByID(t.Context(), "cold-chain-probe")
	if err != nil {
		t.Fatalf("the model was not stored: %v", err)
	}
	if stored.Name != "Cold-chain probe v2" {
		t.Errorf("stored name = %q", stored.Name)
	}
}

func TestCreateModelRequiresAuth(t *testing.T) {
	srv, _ := newTestAPI(t)
	rec := do(srv, http.MethodPost, "/api/v1/models/probe", "", `{"name":"x"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401: a model carries code this server executes", rec.Code)
	}
}

// TestCreateModelRejectsAScriptThatDoesNotCompile is the point of validating at
// save time: the alternative is a model that saves cleanly and then fails on the
// device's first frame, where the only evidence is a counter nobody watches.
func TestCreateModelRejectsAScriptThatDoesNotCompile(t *testing.T) {
	srv, store := newTestAPI(t)

	rec := do(srv, http.MethodPost, "/api/v1/models/broken", testToken,
		`{"name":"Broken","codec_kind":"JS","codec_script":"function decodeUplink( { syntax"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "compile") {
		t.Errorf("the response should say the script failed to compile: %s", rec.Body.String())
	}
	if _, err := store.ModelByID(t.Context(), "broken"); err == nil {
		t.Error("a model whose script does not compile must not be stored")
	}
}

// TestCreateModelRejectsJSWithNoScript: a JS model with an empty script is a
// half-configured formatter. NONE is the answer for "no decoder", and saying so
// keeps "nobody intended one" distinct from "someone forgot".
func TestCreateModelRejectsJSWithNoScript(t *testing.T) {
	srv, _ := newTestAPI(t)
	rec := do(srv, http.MethodPost, "/api/v1/models/half-done", testToken,
		`{"name":"Half done","codec_kind":"JS","codec_script":"  "}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "NONE") {
		t.Errorf("the response should point at NONE as the way to opt out: %s", rec.Body.String())
	}
}

func TestCreateModelRejectsBadSlugs(t *testing.T) {
	srv, _ := newTestAPI(t)
	for _, id := range []string{"x", "Cold-Chain", "-probe", "probe v2", "probe~v2", strings.Repeat("a", 65)} {
		// PathEscape keeps the request itself well-formed, so what is being
		// rejected is the slug and not the URL.
		rec := do(srv, http.MethodPost, "/api/v1/models/"+url.PathEscape(id), testToken, `{"name":"Any"}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("slug %q: status = %d, want 400: %s", id, rec.Code, rec.Body.String())
		}
	}
}

// TestCreateModelDoesNotOverwrite: a typo in the slug must not silently replace a
// formatter a fleet depends on.
func TestCreateModelDoesNotOverwrite(t *testing.T) {
	srv, store := newTestAPI(t)
	first := `{"name":"Original","codec_kind":"NONE"}`
	if rec := do(srv, http.MethodPost, "/api/v1/models/probe", testToken, first); rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d: %s", rec.Code, rec.Body.String())
	}

	rec := do(srv, http.MethodPost, "/api/v1/models/probe", testToken, `{"name":"Typo"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	stored, err := store.ModelByID(t.Context(), "probe")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored.Name != "Original" {
		t.Errorf("the existing model was overwritten: name = %q", stored.Name)
	}
}

func TestUpdateModelIsAnUpsert(t *testing.T) {
	srv, store := newTestAPI(t)

	rec := do(srv, http.MethodPut, "/api/v1/models/new-by-put", testToken, `{"name":"By PUT"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if _, err := store.ModelByID(t.Context(), "new-by-put"); err != nil {
		t.Errorf("PUT to a new slug must create it: %v", err)
	}

	rec = do(srv, http.MethodPut, "/api/v1/models/new-by-put", testToken, `{"name":"Renamed"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: status = %d: %s", rec.Code, rec.Body.String())
	}
	stored, _ := store.ModelByID(t.Context(), "new-by-put")
	if stored.Name != "Renamed" {
		t.Errorf("name = %q, want Renamed", stored.Name)
	}
}

// TestModelListOmitsScripts keeps the picker cheap: a fleet's worth of formatters
// is hundreds of kilobytes, and codec_present says what the list needs to say.
func TestModelListOmitsScripts(t *testing.T) {
	srv, _ := newTestAPI(t)
	if rec := do(srv, http.MethodPost, "/api/v1/models/probe", testToken,
		modelBody("Probe", "JS", constProbeScript)); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	rec := do(srv, http.MethodGet, "/api/v1/models", testToken, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Models []modelView `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Models) != 1 {
		t.Fatalf("models = %d, want 1", len(out.Models))
	}
	if out.Models[0].CodecScript != "" {
		t.Errorf("the list must not carry scripts: %q", out.Models[0].CodecScript)
	}
	if !out.Models[0].CodecPresent {
		t.Error("codec_present must say the model decodes")
	}
}

func TestGetUnknownModelIsFourOhFour(t *testing.T) {
	srv, _ := newTestAPI(t)
	rec := do(srv, http.MethodGet, "/api/v1/models/nothing-here", testToken, "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

// TestDeleteModelReportsWhatItUnassociates: deleting a model silently stops
// decoding its devices, so the response has to say how many.
func TestDeleteModelReportsWhatItUnassociates(t *testing.T) {
	srv, store := newTestAPI(t)
	if rec := do(srv, http.MethodPost, "/api/v1/models/probe", testToken,
		`{"name":"Probe","codec_kind":"NONE"}`); rec.Code != http.StatusCreated {
		t.Fatalf("create model: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(srv, http.MethodPost, "/api/v1/devices", testToken,
		`{"dev_eui":"aabbccddeeff0011","join_eui":"0102030405060708",`+
			`"app_key":"`+testAppKeyStr+`","model_id":"probe"}`); rec.Code != http.StatusCreated {
		t.Fatalf("create device: %d %s", rec.Code, rec.Body.String())
	}

	rec := do(srv, http.MethodDelete, "/api/v1/models/probe", testToken, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "no longer decoded") {
		t.Errorf("the response should state the consequence: %s", rec.Body.String())
	}

	// The device survives and simply has no model.
	stored, err := store.DeviceByDevEUI(t.Context(), mustEUI(t, "aabbccddeeff0011"))
	if err != nil {
		t.Fatalf("the device should still exist: %v", err)
	}
	if stored.ModelID != "" {
		t.Errorf("the device still points at a deleted model: %q", stored.ModelID)
	}
}

// TestDeviceCannotPointAtAnUnknownModel: the database would refuse this with a
// foreign-key violation, which surfaces as an opaque 500. Naming the model the
// operator mistyped is the whole reason for the check.
func TestDeviceCannotPointAtAnUnknownModel(t *testing.T) {
	srv, _ := newTestAPI(t)
	rec := do(srv, http.MethodPost, "/api/v1/devices", testToken,
		`{"dev_eui":"1122334455667788","join_eui":"0102030405060708",`+
			`"app_key":"`+testAppKeyStr+`","model_id":"cold-chain-probe-v3"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "cold-chain-probe-v3") {
		t.Errorf("the response should name the missing model: %s", rec.Body.String())
	}
}

func TestDeviceCarriesItsModel(t *testing.T) {
	srv, _ := newTestAPI(t)
	if rec := do(srv, http.MethodPost, "/api/v1/models/probe", testToken,
		`{"name":"Probe","codec_kind":"NONE"}`); rec.Code != http.StatusCreated {
		t.Fatalf("create model: %d %s", rec.Code, rec.Body.String())
	}

	rec := do(srv, http.MethodPost, "/api/v1/devices", testToken,
		`{"dev_eui":"1122334455667788","join_eui":"0102030405060708",`+
			`"app_key":"`+testAppKeyStr+`","model_id":"probe"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var view deviceView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.ModelID != "probe" {
		t.Errorf("model_id = %q, want probe", view.ModelID)
	}

	// PATCH to a model that does not exist is refused, and to "" clears it.
	if rec := do(srv, http.MethodPatch, "/api/v1/devices/1122334455667788", testToken,
		`{"model_id":"nope"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown model on PATCH: status = %d: %s", rec.Code, rec.Body.String())
	}
	rec = do(srv, http.MethodPatch, "/api/v1/devices/1122334455667788", testToken, `{"model_id":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clearing model_id: status = %d: %s", rec.Code, rec.Body.String())
	}
	var cleared deviceView
	if err := json.Unmarshal(rec.Body.Bytes(), &cleared); err != nil {
		t.Fatalf("decode after clear: %v", err)
	}
	if cleared.ModelID != "" {
		t.Errorf("model_id = %q, want it cleared", cleared.ModelID)
	}
}

// TestTestDecodeRunsAnUnsavedScript is what makes the endpoint useful: a vendor's
// snippet gets checked against a real frame before it ever reaches a fleet.
func TestTestDecodeRunsAnUnsavedScript(t *testing.T) {
	srv, _ := newTestAPI(t)
	if rec := do(srv, http.MethodPost, "/api/v1/models/probe", testToken,
		`{"name":"Probe","codec_kind":"NONE"}`); rec.Code != http.StatusCreated {
		t.Fatalf("create model: %d %s", rec.Code, rec.Body.String())
	}

	raw, err := json.Marshal(map[string]any{
		"payload": "6400", "f_port": 10, "script": constProbeScript,
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := do(srv, http.MethodPost, "/api/v1/models/probe/decode", testToken, string(raw))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		OK    bool           `json:"ok"`
		Data  map[string]any `json:"data"`
		Error string         `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if !out.OK {
		t.Fatalf("ok = false: %s", out.Error)
	}
	if got, ok := out.Data["temperature"].(float64); !ok || got != 10.0 {
		t.Errorf("data = %v, want temperature 10.0", out.Data)
	}
}

// TestTestDecodeReportsAFailingScriptAsAnAnswer: the question was "does this
// script work", and "no, because the sensor flag is set" is the answer. A 4xx
// would make a console show a transport error instead.
func TestTestDecodeReportsAFailingScriptAsAnAnswer(t *testing.T) {
	srv, _ := newTestAPI(t)
	if rec := do(srv, http.MethodPost, "/api/v1/models/probe", testToken,
		`{"name":"Probe","codec_kind":"JS","codec_script":"function decodeUplink(i){throw new Error('bad frame');}"}`); rec.Code != http.StatusCreated {
		t.Fatalf("create model: %d %s", rec.Code, rec.Body.String())
	}

	rec := do(srv, http.MethodPost, "/api/v1/models/probe/decode", testToken,
		`{"payload":"00","f_port":10}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with ok:false: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.OK || !strings.Contains(out.Error, "bad frame") {
		t.Errorf("out = %+v, want ok:false carrying the formatter's own error", out)
	}
}

func TestTestDecodeRejectsUnusableRequests(t *testing.T) {
	srv, _ := newTestAPI(t)
	if rec := do(srv, http.MethodPost, "/api/v1/models/probe", testToken,
		`{"name":"Probe","codec_kind":"JS","codec_script":"function decodeUplink(i){return {data:{}};}"}`); rec.Code != http.StatusCreated {
		t.Fatalf("create model: %d %s", rec.Code, rec.Body.String())
	}
	for name, body := range map[string]string{
		"not hex":       `{"payload":"zz","f_port":10}`,
		"empty payload": `{"payload":"","f_port":10}`,
		"bad port":      `{"payload":"00","f_port":999}`,
		"bad dev_eui":   `{"payload":"00","f_port":10,"dev_eui":"nope"}`,
	} {
		rec := do(srv, http.MethodPost, "/api/v1/models/probe/decode", testToken, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400: %s", name, rec.Code, rec.Body.String())
		}
	}
}

// TestUplinksEndpointNeedsADatabaseStore: the in-memory store has no decoded
// columns. A 503 says so; an empty list would read as a quiet network.
func TestUplinksEndpointNeedsADatabaseStore(t *testing.T) {
	srv, _ := newTestAPI(t)
	rec := do(srv, http.MethodGet, "/api/v1/uplinks", testToken, "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
}

// fakeTraffic records the query the endpoint issued and returns nothing.
type fakeTraffic struct {
	gotDevEUI string
	gotKind   string
	gotLimit  int
}

// RecentEvents keeps the stub whole for the widened TrafficReader. The event
// endpoint has its own stub in api_events_test.go, which can return rows; this one
// exists so the uplink tests keep compiling against an interface that now carries
// two reads, and so that widening is visible here rather than silent.
func (f *fakeTraffic) RecentEvents(_ context.Context, devEUI, kind string, limit int,
) ([]pgstore.EventRow, error) {
	f.gotDevEUI, f.gotKind, f.gotLimit = devEUI, kind, limit
	return nil, nil
}

func (f *fakeTraffic) RecentUplinks(_ context.Context, devEUI string, limit int,
) ([]pgstore.UplinkRow, error) {
	f.gotDevEUI, f.gotLimit = devEUI, limit
	return []pgstore.UplinkRow{{
		UplinkID: 7, ReceivedAt: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC),
		DevEUI: "0011223344556677", FCntUp: 42, FPort: 10,
		FRMPayload: []byte{0x64, 0x00}, RawPayload: []byte{0x40, 0x01},
		DataRate: 5, RSSI: -71, SNR: 9,
		Decoded: []byte(`{"temperature":10.0}`),
	}}, nil
}

func TestUplinksReturnsDecodedReadings(t *testing.T) {
	srv, _ := newTestAPI(t)
	traffic := &fakeTraffic{}
	srv.traffic = traffic

	rec := do(srv, http.MethodGet, "/api/v1/uplinks?dev_eui=0011223344556677&limit=5", testToken, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if traffic.gotDevEUI != "0011223344556677" || traffic.gotLimit != 5 {
		t.Errorf("query = %q / %d", traffic.gotDevEUI, traffic.gotLimit)
	}
	var out struct {
		Uplinks []uplinkView `json:"uplinks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if len(out.Uplinks) != 1 {
		t.Fatalf("uplinks = %d, want 1", len(out.Uplinks))
	}
	u := out.Uplinks[0]
	if !u.HasCodec || u.DecodeError != "" {
		t.Errorf("codec fields = %+v", u)
	}
	// The reading must arrive as the formatter's own JSON, not re-encoded.
	if string(u.Decoded) != `{"temperature":10.0}` {
		t.Errorf("decoded = %s", u.Decoded)
	}
}

// TestUplinksDistinguishesNoCodecFromAFailedDecode: the console renders these two
// differently, and collapsing them hides a malformed frame behind "nobody has a
// decoder".
func TestUplinksDistinguishesNoCodecFromAFailedDecode(t *testing.T) {
	srv, _ := newTestAPI(t)
	srv.traffic = &failedDecodeTraffic{}
	rec := do(srv, http.MethodGet, "/api/v1/uplinks", testToken, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Uplinks []uplinkView `json:"uplinks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Uplinks) != 2 {
		t.Fatalf("uplinks = %d, want 2", len(out.Uplinks))
	}
	plain, failed := out.Uplinks[0], out.Uplinks[1]
	if plain.HasCodec || plain.DecodeError != "" {
		t.Errorf("a frame with no formatter should say nothing: %+v", plain)
	}
	if !failed.HasCodec || failed.DecodeError != "codec: payload formatter reported: bad frame" {
		t.Errorf("a rejected frame must carry its reason: %+v", failed)
	}
	if failed.Data == "" {
		t.Error("a frame the formatter rejected must still carry its raw bytes")
	}
}

type failedDecodeTraffic struct{}

func (failedDecodeTraffic) RecentUplinks(context.Context, string, int) ([]pgstore.UplinkRow, error) {
	return []pgstore.UplinkRow{
		{UplinkID: 1, DevEUI: "0011223344556677", FPort: 10, FRMPayload: []byte{1}},
		{UplinkID: 2, DevEUI: "0011223344556677", FPort: 10, FRMPayload: []byte{2},
			DecodeError: "codec: payload formatter reported: bad frame"},
	}, nil
}

func (failedDecodeTraffic) RecentEvents(context.Context, string, string, int) ([]pgstore.EventRow, error) {
	return nil, nil
}

func TestUplinksRejectsABadDeviceFilter(t *testing.T) {
	srv, _ := newTestAPI(t)
	srv.traffic = &fakeTraffic{}
	rec := do(srv, http.MethodGet, "/api/v1/uplinks?dev_eui=xyz", testToken, "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	rec = do(srv, http.MethodGet, "/api/v1/uplinks?limit=0", testToken, "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("limit=0: status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}
