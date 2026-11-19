package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fieldnode/lorawan-server/api/internal/domain"
	"github.com/fieldnode/lorawan-server/api/internal/nsclient"
)

// stubRepository satisfies the read port. Only ListDevices is exercised here;
// the rest exist to make the interface compile so the fake stays a fake rather
// than a partial one that panics on an unexpected call.
type stubRepository struct {
	devices []domain.Device
	err     error
}

func (s *stubRepository) Ping(context.Context) error { return s.err }
func (s *stubRepository) ListDevices(context.Context) ([]domain.Device, error) {
	return s.devices, s.err
}
func (s *stubRepository) GetOverview(context.Context) (domain.Overview, error) {
	return domain.Overview{}, s.err
}
func (s *stubRepository) ListGateways(context.Context) ([]domain.Gateway, error) {
	return nil, s.err
}
func (s *stubRepository) ListEvents(context.Context) ([]domain.NetworkEvent, error) {
	return nil, s.err
}
func (s *stubRepository) GetSettings(context.Context) (domain.FacilitySettings, error) {
	return domain.FacilitySettings{}, s.err
}
func (s *stubRepository) UpdateSettings(_ context.Context, v domain.FacilitySettings) (domain.FacilitySettings, error) {
	return v, s.err
}

// stubProvisioner records what the handler decided to send, which is the part
// worth asserting: that the credentials reach the network server and that
// validation happened first.
type stubProvisioner struct {
	got         Provisioning
	result      ProvisionedDevice
	err         error
	calls       int
	hasDeadline bool
}

func (p *stubProvisioner) CreateDevice(ctx context.Context, d Provisioning) (ProvisionedDevice, error) {
	p.calls++
	p.got = d
	_, p.hasDeadline = ctx.Deadline()
	if p.err != nil {
		return ProvisionedDevice{}, p.err
	}
	return p.result, nil
}

func postDevice(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode the error body %q: %v", rec.Body, err)
	}
	return body.Error
}

const validBody = `{"name":"Boiler sensor","devEui":"0011223344556677",` +
	`"joinEui":"0102030405060708","appKey":"2b7e151628aed2a6abf7158809cf4f3c","group":"Basement"}`

func TestCreateDeviceProvisionsTheNetworkServer(t *testing.T) {
	prov := &stubProvisioner{result: ProvisionedDevice{
		DevEUI: "0011223344556677", Name: "Boiler sensor", Group: "Basement", Activation: "otaa",
	}}
	s := New(&stubRepository{}, prov, nil)

	rec := postDevice(t, s, validBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if prov.calls != 1 {
		t.Fatalf("the network server was called %d times, want 1", prov.calls)
	}
	// Keys are normalised to upper case on the way through, which is the form
	// the network server's schema checks for.
	if prov.got.DevEUI != "0011223344556677" || prov.got.AppKey != "2B7E151628AED2A6ABF7158809CF4F3C" {
		t.Errorf("provisioning request = %+v", prov.got)
	}
	if prov.got.JoinEUI != "0102030405060708" || prov.got.Group != "Basement" {
		t.Errorf("provisioning request = %+v", prov.got)
	}
	// A deadline must be attached: the network server is another process.
	if !prov.hasDeadline {
		t.Error("the provisioning call must carry a context with a deadline")
	}

	var out deviceResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode the response: %v", err)
	}
	if out.ID != "0011223344556677" || out.Name != "Boiler sensor" || out.Group != "Basement" {
		t.Errorf("response = %+v", out)
	}
	// The response must not echo the key back: it is the one value that must
	// never leave this process again.
	if strings.Contains(rec.Body.String(), "2b7e1516") {
		t.Errorf("the response body leaked the AppKey: %s", rec.Body)
	}
}

// TestCreateDeviceNormalizesFormatting: keys arrive copied from a datasheet or a
// QR payload with separators and lower case.
func TestCreateDeviceNormalizesFormatting(t *testing.T) {
	prov := &stubProvisioner{result: ProvisionedDevice{DevEUI: "0011223344556677"}}
	s := New(&stubRepository{}, prov, nil)

	rec := postDevice(t, s, `{"name":"x","devEui":"00-11-22-33-44-55-66-77",`+
		`"appKey":"2b7e151628aed2a6abf7158809cf4f3c"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if prov.got.DevEUI != "0011223344556677" {
		t.Errorf("DevEUI = %q, want the separators stripped and upper-cased", prov.got.DevEUI)
	}
	// JoinEUI is optional; the network server applies its own default.
	if prov.got.JoinEUI != "" {
		t.Errorf("JoinEUI = %q, want empty so the ns default applies", prov.got.JoinEUI)
	}
	if prov.got.Group != "Unassigned" {
		t.Errorf("Group = %q, want the default label", prov.got.Group)
	}
}

func TestCreateDeviceRejectsInvalidInputBeforeProvisioning(t *testing.T) {
	cases := map[string]struct {
		body       string
		wantStatus int
		wantCode   string
	}{
		"short deveui": {`{"name":"x","devEui":"0011223344","appKey":"2b7e151628aed2a6abf7158809cf4f3c"}`,
			http.StatusBadRequest, "deveui_must_be_16_hex_characters"},
		"non-hex deveui": {`{"name":"x","devEui":"ZZ11223344556677","appKey":"2b7e151628aed2a6abf7158809cf4f3c"}`,
			http.StatusBadRequest, "deveui_must_be_16_hex_characters"},
		"short appkey": {`{"name":"x","devEui":"0011223344556677","appKey":"2b7e1516"}`,
			http.StatusBadRequest, "appkey_must_be_32_hex_characters"},
		"missing appkey": {`{"name":"x","devEui":"0011223344556677"}`,
			http.StatusBadRequest, "appkey_must_be_32_hex_characters"},
		"bad joineui": {`{"name":"x","devEui":"0011223344556677","joinEui":"nope",` +
			`"appKey":"2b7e151628aed2a6abf7158809cf4f3c"}`,
			http.StatusBadRequest, "joineui_must_be_16_hex_characters"},
		"empty name": {`{"name":"   ","devEui":"0011223344556677","appKey":"2b7e151628aed2a6abf7158809cf4f3c"}`,
			http.StatusBadRequest, "name_must_be_1_to_120_characters"},
		"unknown field": {`{"name":"x","devEui":"0011223344556677",` +
			`"appKey":"2b7e151628aed2a6abf7158809cf4f3c","admin":true}`,
			http.StatusBadRequest, "invalid_json"},
		"trailing object": {validBody + validBody, http.StatusBadRequest, "invalid_json"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			prov := &stubProvisioner{}
			rec := postDevice(t, New(&stubRepository{}, prov, nil), tc.body)
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (%s)", rec.Code, tc.wantStatus, rec.Body)
			}
			if got := errorCode(t, rec); got != tc.wantCode {
				t.Errorf("error = %q, want %q", got, tc.wantCode)
			}
			if prov.calls != 0 {
				t.Errorf("an invalid request reached the network server %d time(s)", prov.calls)
			}
		})
	}
}

// TestCreateDeviceWithoutProvisioningConfigured: the dashboard is still useful
// when it cannot reach a network server, but registration must say so rather
// than write a row that does nothing.
func TestCreateDeviceWithoutProvisioningConfigured(t *testing.T) {
	rec := postDevice(t, New(&stubRepository{}, nil, nil), validBody)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if got := errorCode(t, rec); got != "provisioning_not_configured" {
		t.Errorf("error = %q", got)
	}
}

func TestCreateDeviceMapsNetworkServerErrors(t *testing.T) {
	cases := []struct {
		err        error
		wantStatus int
		wantCode   string
	}{
		{&nsclient.ConflictError{Detail: "already provisioned"}, http.StatusConflict, "device_already_registered"},
		{&nsclient.APIError{Status: 400, Detail: "app_key is not 128-bit"}, http.StatusBadGateway, "network_server_unreachable"},
		{context.DeadlineExceeded, http.StatusGatewayTimeout, "network_server_timed_out"},
	}
	for _, tc := range cases {
		prov := &stubProvisioner{err: tc.err}
		rec := postDevice(t, New(&stubRepository{}, prov, nil), validBody)
		if rec.Code != tc.wantStatus {
			t.Errorf("for %v: status = %d, want %d", tc.err, rec.Code, tc.wantStatus)
		}
		if got := errorCode(t, rec); got != tc.wantCode {
			t.Errorf("for %v: error = %q, want %q", tc.err, got, tc.wantCode)
		}
		// The ns's own words name a field, which helps in the server log; echoing
		// them to a browser would describe internals to whoever is probing it.
		if strings.Contains(rec.Body.String(), "128-bit") {
			t.Errorf("the upstream detail leaked to the client: %s", rec.Body)
		}
	}
}

func TestListDevicesRendersHealth(t *testing.T) {
	repo := &stubRepository{devices: []domain.Device{{
		DevEUI: "0011223344556677", Name: "Boiler sensor", Group: "Basement",
		Gateway: "AA55555555555555", State: "Online", Battery: 88, RSSI: -71,
	}}}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/devices", nil)
	rec := httptest.NewRecorder()
	New(repo, nil, nil).Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var out []deviceResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d devices, want 1", len(out))
	}
	if out[0].ID != "0011223344556677" || out[0].State != "Online" || out[0].Battery != 88 {
		t.Errorf("device = %+v", out[0])
	}
	if out[0].LastSeen != "Never" {
		t.Errorf("LastSeen = %q, want Never for a device with no uplink", out[0].LastSeen)
	}
}
