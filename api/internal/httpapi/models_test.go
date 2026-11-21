package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fieldnode/lorawan-server/api/internal/nsclient"
)

// recordingController records what the relay asked the network server for and
// replies with a canned body. The contract under test is the relay itself: the
// right call, the right slug, the body byte for byte, and the network server's
// answer returned unchanged.
type recordingController struct {
	reply  json.RawMessage
	err    error
	method string
	slug   string
	body   string
	limit  int
	devEUI string
	patch  nsclient.DevicePatch

	// Integration relay: the destination id and the exact bytes forwarded.
	integrationID   string
	integrationBody string
}

func (c *recordingController) respond() (json.RawMessage, error) {
	if c.err != nil {
		return nil, c.err
	}
	if c.reply != nil {
		return c.reply, nil
	}
	return json.RawMessage(`{"ok":true}`), nil
}

func (c *recordingController) EnqueueDownlink(context.Context, string, int, string, bool) (json.RawMessage, error) {
	return c.respond()
}
func (c *recordingController) CancelDownlink(context.Context, string) (json.RawMessage, error) {
	return c.respond()
}
func (c *recordingController) RequestDeviceStatus(context.Context, string) (json.RawMessage, error) {
	return c.respond()
}
func (c *recordingController) Session(context.Context, string) (json.RawMessage, error) {
	return c.respond()
}
func (c *recordingController) Device(context.Context, string) (json.RawMessage, error) {
	return c.respond()
}
func (c *recordingController) Status(context.Context) (json.RawMessage, error) { return c.respond() }
func (c *recordingController) DeleteDevice(context.Context, string) (json.RawMessage, error) {
	return c.respond()
}

func (c *recordingController) PatchDevice(_ context.Context, _ string, p nsclient.DevicePatch) (json.RawMessage, error) {
	c.method, c.patch = "PATCH_DEVICE", p
	return c.respond()
}

func (c *recordingController) ListModels(context.Context) (json.RawMessage, error) {
	c.method = "LIST"
	return c.respond()
}

func (c *recordingController) Model(_ context.Context, id string) (json.RawMessage, error) {
	c.method, c.slug = "GET", id
	return c.respond()
}

func (c *recordingController) CreateModel(_ context.Context, id string, body json.RawMessage) (json.RawMessage, error) {
	c.method, c.slug, c.body = "CREATE", id, string(body)
	return c.respond()
}

func (c *recordingController) UpdateModel(_ context.Context, id string, body json.RawMessage) (json.RawMessage, error) {
	c.method, c.slug, c.body = "UPDATE", id, string(body)
	return c.respond()
}

func (c *recordingController) DeleteModel(_ context.Context, id string) (json.RawMessage, error) {
	c.method, c.slug = "DELETE", id
	return c.respond()
}

func (c *recordingController) TestModelDecode(_ context.Context, id string, body json.RawMessage) (json.RawMessage, error) {
	c.method, c.slug, c.body = "DECODE", id, string(body)
	return c.respond()
}

func (c *recordingController) Uplinks(_ context.Context, devEUI string, limit int) (json.RawMessage, error) {
	c.method, c.devEUI, c.limit = "UPLINKS", devEUI, limit
	return c.respond()
}

func (c *recordingController) ListIntegrations(context.Context) (json.RawMessage, error) {
	c.method = "LIST_INTEGRATIONS"
	return c.respond()
}

func (c *recordingController) Integration(_ context.Context, id string) (json.RawMessage, error) {
	c.method, c.integrationID = "GET_INTEGRATION", id
	return c.respond()
}

func (c *recordingController) CreateIntegration(_ context.Context, body json.RawMessage) (json.RawMessage, error) {
	c.method, c.integrationBody = "CREATE_INTEGRATION", string(body)
	return c.respond()
}

func (c *recordingController) UpdateIntegration(_ context.Context, id string, body json.RawMessage) (json.RawMessage, error) {
	c.method, c.integrationID, c.integrationBody = "UPDATE_INTEGRATION", id, string(body)
	return c.respond()
}

func (c *recordingController) DeleteIntegration(_ context.Context, id string) (json.RawMessage, error) {
	c.method, c.integrationID = "DELETE_INTEGRATION", id
	if c.err != nil {
		return nil, c.err
	}
	// A delete answers with no body, which the relay must keep a 204.
	return nil, nil
}

func (c *recordingController) TestIntegration(_ context.Context, id string) (json.RawMessage, error) {
	c.method, c.integrationID = "TEST_INTEGRATION", id
	return c.respond()
}

func doRequest(t *testing.T, handler http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// TestModelBodyIsRelayedByteForByte: the script is the payload. Re-marshalling it
// would reorder keys and re-escape strings, so what the operator saved would not
// be what the network server compiled.
func TestModelBodyIsRelayedByteForByte(t *testing.T) {
	body := `{"name":"Probe","codec_kind":"JS","codec_script":"function decodeUplink(b){return {t:b[0]}}"}`
	ctrl := &recordingController{}
	rec := doRequest(t, New(nil, nil, ctrl).Handler(), http.MethodPut, "/api/v1/models/probe-1", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if ctrl.body != body {
		t.Errorf("forwarded body = %s, want %s", ctrl.body, body)
	}
}

// TestModelResponseIsReturnedVerbatim covers the failing-decode case, which is a
// 200 with ok:false from the network server. Turning it into an error would hide
// the parser message that names the offending line.
func TestModelResponseIsReturnedVerbatim(t *testing.T) {
	reply := json.RawMessage(`{"ok":false,"error":"the payload formatter cannot be compiled: unexpected token }"}`)
	ctrl := &recordingController{reply: reply}
	rec := doRequest(t, New(nil, nil, ctrl).Handler(), http.MethodPost, "/api/v1/models/probe-1/decode", `{"payload":"0A"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: a failing script is an answer, not a transport error", rec.Code)
	}
	if rec.Body.String() != string(reply) {
		t.Errorf("body = %s, want %s", rec.Body.String(), reply)
	}
}

// TestModelSlugIsValidatedBeforeTheRoundTrip: a mistyped slug is refused here with
// the rule spelled out, and the network server is never asked.
func TestModelSlugIsValidatedBeforeTheRoundTrip(t *testing.T) {
	for _, slug := range []string{"A-bad", "a", "has%20space", "UPPER", "_leading", strings.Repeat("x", 65)} {
		ctrl := &recordingController{}
		rec := doRequest(t, New(nil, nil, ctrl).Handler(), http.MethodGet, "/api/v1/models/"+slug, "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("slug %q: status = %d, want 400", slug, rec.Code)
			continue
		}
		if ctrl.method != "" {
			t.Errorf("slug %q reached the network server (%s)", slug, ctrl.method)
		}
	}
}

// TestOversizedFormatterIsNamedAsSuch. The generic 16 KiB cap would reject a
// vendor decoder between 16 KiB and 64 KiB with "invalid JSON", which sends an
// operator looking in the wrong place.
func TestOversizedFormatterIsNamedAsSuch(t *testing.T) {
	script := strings.Repeat("a", MaxModelBodyBytes+1)
	body := `{"name":"Probe","codec_kind":"JS","codec_script":"` + script + `"}`
	ctrl := &recordingController{}
	rec := doRequest(t, New(nil, nil, ctrl).Handler(), http.MethodPut, "/api/v1/models/probe-1", body)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), ErrModelTooLarge) {
		t.Errorf("body = %s, want %q", rec.Body.String(), ErrModelTooLarge)
	}
	if ctrl.method != "" {
		t.Error("an oversized body must not reach the network server")
	}
}

// TestFormatterAtTheNetworkServersLimitPassesThrough pins the two limits agreeing:
// a 64 KiB script is what the ns accepts, so the relay must not be the thing that
// refuses it.
func TestFormatterAtTheNetworkServersLimitPassesThrough(t *testing.T) {
	script := strings.Repeat("a", 64<<10)
	body := `{"name":"Probe","codec_kind":"JS","codec_script":"` + script + `"}`
	ctrl := &recordingController{}
	rec := doRequest(t, New(nil, nil, ctrl).Handler(), http.MethodPut, "/api/v1/models/probe-1", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if ctrl.method != "UPDATE" {
		t.Errorf("reached %s, want UPDATE", ctrl.method)
	}
}

func TestModelBodyMustBeJSON(t *testing.T) {
	for _, body := range []string{"{", "not json"} {
		ctrl := &recordingController{}
		rec := doRequest(t, New(nil, nil, ctrl).Handler(), http.MethodPut, "/api/v1/models/probe-1", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400", body, rec.Code)
		}
	}
}

// TestModelRoutesWithoutANetworkServer: the console runs without one, and then the
// honest answer is 503 rather than an empty list that reads as "no models yet".
func TestModelRoutesWithoutANetworkServer(t *testing.T) {
	for _, target := range []string{"/api/v1/models", "/api/v1/models/probe-1", "/api/v1/uplinks"} {
		rec := doRequest(t, New(nil, nil, nil).Handler(), http.MethodGet, target, "")
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: status = %d, want 503", target, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), ErrControllerNotConfigured) {
			t.Errorf("%s: body = %s, want %q", target, rec.Body.String(), ErrControllerNotConfigured)
		}
	}
}

// TestModelRoutesRelayTheNetworkServer is the contract the console depends on:
// each route reaches the matching network-server call and returns its body
// verbatim, so a field added on the ns shows up in the dashboard without a change
// here.
func TestModelRoutesRelayTheNetworkServer(t *testing.T) {
	cases := []struct {
		name       string
		method     string
		target     string
		body       string
		wantMethod string
		wantSlug   string
		wantStatus int
	}{
		{"list", http.MethodGet, "/api/v1/models", "", "LIST", "", http.StatusOK},
		{"get", http.MethodGet, "/api/v1/models/cold-chain-probe_v2", "", "GET", "cold-chain-probe_v2", http.StatusOK},
		{"create", http.MethodPost, "/api/v1/models/probe-1", `{"name":"Probe"}`, "CREATE", "probe-1", http.StatusCreated},
		{"update", http.MethodPut, "/api/v1/models/probe-1", `{"name":"Probe"}`, "UPDATE", "probe-1", http.StatusOK},
		{"delete", http.MethodDelete, "/api/v1/models/probe-1", "", "DELETE", "probe-1", http.StatusOK},
		{"decode", http.MethodPost, "/api/v1/models/probe-1/decode", `{"payload":"0A"}`, "DECODE", "probe-1", http.StatusOK},
	}
	for _, tc := range cases {
		ctrl := &recordingController{}
		rec := doRequest(t, New(nil, nil, ctrl).Handler(), tc.method, tc.target, tc.body)
		if rec.Code != tc.wantStatus {
			t.Errorf("%s: status = %d, want %d (%s)", tc.name, rec.Code, tc.wantStatus, rec.Body.String())
			continue
		}
		if ctrl.method != tc.wantMethod {
			t.Errorf("%s: reached %s, want %s", tc.name, ctrl.method, tc.wantMethod)
		}
		if ctrl.slug != tc.wantSlug {
			t.Errorf("%s: slug = %q, want %q", tc.name, ctrl.slug, tc.wantSlug)
		}
	}
}

func TestUplinksQueryIsParsedAndBounded(t *testing.T) {
	cases := []struct {
		name       string
		target     string
		wantStatus int
		wantLimit  int
		wantDevEUI string
	}{
		{"default limit", "/api/v1/uplinks", http.StatusOK, 50, ""},
		{"explicit limit", "/api/v1/uplinks?limit=5", http.StatusOK, 5, ""},
		{"device filter", "/api/v1/uplinks?dev_eui=0011223344556677", http.StatusOK, 50, "0011223344556677"},
		{"zero limit", "/api/v1/uplinks?limit=0", http.StatusBadRequest, 0, ""},
		{"negative limit", "/api/v1/uplinks?limit=-3", http.StatusBadRequest, 0, ""},
		{"limit above the ceiling", "/api/v1/uplinks?limit=501", http.StatusBadRequest, 0, ""},
		{"non-numeric limit", "/api/v1/uplinks?limit=many", http.StatusBadRequest, 0, ""},
		{"malformed eui", "/api/v1/uplinks?dev_eui=zz", http.StatusBadRequest, 0, ""},
	}
	for _, tc := range cases {
		ctrl := &recordingController{}
		rec := doRequest(t, New(nil, nil, ctrl).Handler(), http.MethodGet, tc.target, "")
		if rec.Code != tc.wantStatus {
			t.Errorf("%s: status = %d, want %d", tc.name, rec.Code, tc.wantStatus)
			continue
		}
		if tc.wantStatus != http.StatusOK {
			continue
		}
		if ctrl.limit != tc.wantLimit {
			t.Errorf("%s: limit = %d, want %d", tc.name, ctrl.limit, tc.wantLimit)
		}
		if ctrl.devEUI != tc.wantDevEUI {
			t.Errorf("%s: dev_eui = %q, want %q", tc.name, ctrl.devEUI, tc.wantDevEUI)
		}
	}
}

// TestPatchDeviceCarriesTheModelAssociation covers both directions. The clearing
// case is the one a naive implementation loses, because an empty string looks like
// nothing worth sending — and a model that cannot be detached is a device stuck
// being decoded by a formatter that no longer describes it.
func TestPatchDeviceCarriesTheModelAssociation(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantSet  bool
		wantSlug string
	}{
		{"set", `{"model_id":"cold-chain-probe-v2"}`, true, "cold-chain-probe-v2"},
		{"clear", `{"model_id":""}`, true, ""},
		{"other field only", `{"name":"Renamed"}`, false, ""},
	}
	for _, tc := range cases {
		ctrl := &recordingController{}
		rec := doRequest(t, New(nil, nil, ctrl).Handler(), http.MethodPatch,
			"/api/v1/devices/0011223344556677", tc.body)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200: %s", tc.name, rec.Code, rec.Body.String())
			continue
		}
		gotSet := ctrl.patch.ModelID != nil
		if gotSet != tc.wantSet {
			t.Errorf("%s: ModelID sent = %v, want %v", tc.name, gotSet, tc.wantSet)
			continue
		}
		if tc.wantSet && *ctrl.patch.ModelID != tc.wantSlug {
			t.Errorf("%s: ModelID = %q, want %q", tc.name, *ctrl.patch.ModelID, tc.wantSlug)
		}
	}
}

func TestPatchDeviceRejectsAMalformedModelID(t *testing.T) {
	for _, body := range []string{`{"model_id":"Cold Chain"}`, `{"model_id":"x"}`, `{"model_id":"UPPER"}`} {
		ctrl := &recordingController{}
		rec := doRequest(t, New(nil, nil, ctrl).Handler(), http.MethodPatch,
			"/api/v1/devices/0011223344556677", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %s: status = %d, want 400", body, rec.Code)
			continue
		}
		if ctrl.method != "" {
			t.Errorf("body %s reached the network server", body)
		}
	}
}

// TestPatchDeviceRejectsAnEmptyBodyStill: adding model_id to the field list must
// not turn "nothing was sent" into a request worth forwarding.
func TestPatchDeviceRejectsAnEmptyBodyStill(t *testing.T) {
	ctrl := &recordingController{}
	rec := doRequest(t, New(nil, nil, ctrl).Handler(), http.MethodPatch,
		"/api/v1/devices/0011223344556677", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), ErrNoFieldsToChange) {
		t.Errorf("body = %s, want %q", rec.Body.String(), ErrNoFieldsToChange)
	}
}

func TestValidateModelIDField(t *testing.T) {
	ptr := func(s string) *string { return &s }
	cases := []struct {
		name string
		in   *string
		want string
	}{
		{"absent", nil, ""},
		{"empty clears", ptr(""), ""},
		{"a normal slug", ptr("cold-chain-probe-v2"), ""},
		{"underscore and digits", ptr("probe_v2"), ""},
		{"upper case", ptr("Probe"), ErrModelIDInvalid},
		{"a space", ptr("cold chain"), ErrModelIDInvalid},
		{"one character", ptr("p"), ErrModelIDInvalid},
		{"a slash", ptr("a/b"), ErrModelIDInvalid},
	}
	for _, tc := range cases {
		if got := validateModelIDField(tc.in); got != tc.want {
			t.Errorf("%s: validateModelIDField = %q, want %q", tc.name, got, tc.want)
		}
	}
}
