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

// stubController records the commands the handlers decided to send. The point of
// these tests is the translation layer: what the dashboard asked for versus what
// goes to the network server, and what comes back when that fails.
type stubController struct {
	gotDevEUI string
	gotFPort  int
	gotData   string
	gotConf   bool
	gotPatch  nsclient.DevicePatch
	calls     int
	resp      json.RawMessage
	err       error

	// Integration relay assertions need the id and the exact bytes forwarded.
	gotIntegrationID   string
	gotIntegrationBody json.RawMessage
}

func (c *stubController) EnqueueDownlink(_ context.Context, devEUI string, fPort int, data string, confirmed bool) (json.RawMessage, error) {
	c.calls++
	c.gotDevEUI, c.gotFPort, c.gotData, c.gotConf = devEUI, fPort, data, confirmed
	if c.err != nil {
		return nil, c.err
	}
	if c.resp != nil {
		return c.resp, nil
	}
	return json.RawMessage(`{"queued":true}`), nil
}

func (c *stubController) CancelDownlink(_ context.Context, devEUI string) (json.RawMessage, error) {
	c.calls++
	c.gotDevEUI = devEUI
	if c.err != nil {
		return nil, c.err
	}
	return json.RawMessage(`{"cancelled":true}`), nil
}

func (c *stubController) RequestDeviceStatus(_ context.Context, devEUI string) (json.RawMessage, error) {
	c.calls++
	c.gotDevEUI = devEUI
	if c.err != nil {
		return nil, c.err
	}
	return json.RawMessage(`{"scheduled":true}`), nil
}

func (c *stubController) Session(_ context.Context, devEUI string) (json.RawMessage, error) {
	c.calls++
	c.gotDevEUI = devEUI
	if c.err != nil {
		return nil, c.err
	}
	if c.resp != nil {
		return c.resp, nil
	}
	return json.RawMessage(`{"dev_eui":"0011223344556677","data_rate":5}`), nil
}

func (c *stubController) Status(context.Context) (json.RawMessage, error) {
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	if c.resp != nil {
		return c.resp, nil
	}
	return json.RawMessage(`{"region":"EU868","gateways_online":1}`), nil
}

func (c *stubController) Device(_ context.Context, devEUI string) (json.RawMessage, error) {
	c.calls++
	c.gotDevEUI = devEUI
	if c.err != nil {
		return nil, c.err
	}
	if c.resp != nil {
		return c.resp, nil
	}
	return json.RawMessage(`{"dev_eui":"0011223344556677","enabled":true,"disable_adr":false}`), nil
}

func (c *stubController) PatchDevice(_ context.Context, devEUI string, p nsclient.DevicePatch) (json.RawMessage, error) {
	c.calls++
	c.gotDevEUI, c.gotPatch = devEUI, p
	if c.err != nil {
		return nil, c.err
	}
	if c.resp != nil {
		return c.resp, nil
	}
	return json.RawMessage(`{"dev_eui":"0011223344556677","name":"edited"}`), nil
}

// The device-model and uplink-history calls. This stub exists for the control
// tests that predate them; models_test.go has its own recording fake that
// asserts on what the relay forwarded.
func (c *stubController) ListModels(context.Context) (json.RawMessage, error) {
	c.calls++
	return c.stubbed()
}

func (c *stubController) Model(context.Context, string) (json.RawMessage, error) {
	c.calls++
	return c.stubbed()
}

func (c *stubController) CreateModel(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	c.calls++
	return c.stubbed()
}

func (c *stubController) UpdateModel(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	c.calls++
	return c.stubbed()
}

func (c *stubController) DeleteModel(context.Context, string) (json.RawMessage, error) {
	c.calls++
	return c.stubbed()
}

func (c *stubController) TestModelDecode(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	c.calls++
	return c.stubbed()
}

func (c *stubController) Uplinks(context.Context, string, int) (json.RawMessage, error) {
	c.calls++
	return c.stubbed()
}

// Integration destinations. The id and body are recorded because the relay's whole
// job is to pass them through unchanged — a body that lost a field on the way
// through would silently drop a broker credential or a topic.
func (c *stubController) ListIntegrations(context.Context) (json.RawMessage, error) {
	c.calls++
	return c.stubbed()
}

func (c *stubController) Integration(_ context.Context, id string) (json.RawMessage, error) {
	c.calls++
	c.gotIntegrationID = id
	return c.stubbed()
}

func (c *stubController) CreateIntegration(_ context.Context, body json.RawMessage) (json.RawMessage, error) {
	c.calls++
	c.gotIntegrationBody = body
	return c.stubbed()
}

func (c *stubController) UpdateIntegration(_ context.Context, id string, body json.RawMessage) (json.RawMessage, error) {
	c.calls++
	c.gotIntegrationID, c.gotIntegrationBody = id, body
	return c.stubbed()
}

func (c *stubController) DeleteIntegration(_ context.Context, id string) (json.RawMessage, error) {
	c.calls++
	c.gotIntegrationID = id
	if c.err != nil {
		return nil, c.err
	}
	// The network server answers a delete with no body, which the handler must
	// pass through as 204 rather than as an empty 200.
	return nil, nil
}

func (c *stubController) TestIntegration(_ context.Context, id string) (json.RawMessage, error) {
	c.calls++
	c.gotIntegrationID = id
	if c.err != nil {
		return nil, c.err
	}
	if c.resp != nil {
		return c.resp, nil
	}
	return json.RawMessage(`{"ok":true,"detail":"a test event was accepted"}`), nil
}

func (c *stubController) stubbed() (json.RawMessage, error) {
	if c.err != nil {
		return nil, c.err
	}
	if c.resp != nil {
		return c.resp, nil
	}
	return json.RawMessage(`{}`), nil
}

func (c *stubController) DeleteDevice(_ context.Context, devEUI string) (json.RawMessage, error) {
	c.calls++
	c.gotDevEUI = devEUI
	if c.err != nil {
		return nil, c.err
	}
	return nil, nil
}

func call(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

const devPath = "/api/v1/devices/0011223344556677"

func TestEnqueueDownlinkQueuesAndAnswersAccepted(t *testing.T) {
	ctl := &stubController{}
	rec := call(t, New(&stubRepository{}, nil, ctl), http.MethodPost,
		devPath+"/downlink", `{"data":"de ad be ef","f_port":10,"confirmed":true}`)

	// 202, not 200: the frame is queued for the device's next window, and a
	// client that reads 200 as "sent" would report something that has not
	// happened yet.
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if ctl.calls != 1 {
		t.Fatalf("the network server was called %d times, want 1", ctl.calls)
	}
	if ctl.gotDevEUI != "0011223344556677" {
		t.Errorf("DevEUI = %q", ctl.gotDevEUI)
	}
	if ctl.gotFPort != 10 || !ctl.gotConf {
		t.Errorf("f_port=%d confirmed=%v", ctl.gotFPort, ctl.gotConf)
	}
	// Spaces and lower case are the operator's convenience; the network server
	// gets canonical uppercase hex.
	if ctl.gotData != "DEADBEEF" {
		t.Errorf("data = %q, want DEADBEEF", ctl.gotData)
	}
}

func TestEnqueueDownlinkDefaultsFPort(t *testing.T) {
	ctl := &stubController{}
	call(t, New(&stubRepository{}, nil, ctl), http.MethodPost, devPath+"/downlink", `{"data":"01"}`)
	if ctl.gotFPort != 10 {
		t.Errorf("f_port = %d, want the default 10", ctl.gotFPort)
	}
}

func TestEnqueueDownlinkRejectsBadInputBeforeCalling(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"not hex", `{"data":"zz"}`, ErrPayloadHex},
		{"odd number of digits", `{"data":"abc"}`, ErrPayloadHex},
		{"empty", `{"data":""}`, ErrPayloadRequired},
		{"missing", `{}`, ErrPayloadRequired},
		{"too long", `{"data":"` + strings.Repeat("aa", 52) + `"}`, ErrPayloadTooLong},
		{"f_port 0 means MAC only", `{"data":"01","f_port":0}`, ErrFPortOutOfRange},
		{"f_port above the application range", `{"data":"01","f_port":224}`, ErrFPortOutOfRange},
		{"unknown field", `{"data":"01","nope":true}`, ErrInvalidJSON},
		{"trailing JSON", `{"data":"01"} {}`, ErrInvalidJSON},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctl := &stubController{}
			rec := call(t, New(&stubRepository{}, nil, ctl), http.MethodPost,
				devPath+"/downlink", tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body)
			}
			if got := errorCode(t, rec); got != tc.want {
				t.Errorf("error = %q, want %q", got, tc.want)
			}
			// A rejected request must not reach the network server: a device
			// transmitting is not something a malformed body should cause.
			if ctl.calls != 0 {
				t.Errorf("the network server was called %d times for a rejected request", ctl.calls)
			}
		})
	}
}

func TestControlEndpointsRejectAMalformedDevEUI(t *testing.T) {
	routes := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/devices/not-an-eui/downlink"},
		{http.MethodDelete, "/api/v1/devices/0011/downlink"},
		{http.MethodPost, "/api/v1/devices/zzzzzzzzzzzzzzzz/devstatus"},
		{http.MethodGet, "/api/v1/devices/001122334455667711/session"},
	}
	for _, r := range routes {
		ctl := &stubController{}
		rec := call(t, New(&stubRepository{}, nil, ctl), r.method, r.path, "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s %s: status = %d, want 400", r.method, r.path, rec.Code)
		}
		if got := errorCode(t, rec); got != ErrDevEUIHex {
			t.Errorf("%s %s: error = %q, want %q", r.method, r.path, got, ErrDevEUIHex)
		}
		if ctl.calls != 0 {
			t.Errorf("%s %s reached the network server", r.method, r.path)
		}
	}
}

// TestControlEndpointsAnswer503WithoutANetworkServer: the console runs usefully
// with no network server configured, and the endpoints that cannot work must say
// so in a way the dashboard renders as "not configured" rather than "failed".
func TestControlEndpointsAnswer503WithoutANetworkServer(t *testing.T) {
	routes := []struct{ method, path, body string }{
		{http.MethodPost, devPath + "/downlink", `{"data":"01"}`},
		{http.MethodDelete, devPath + "/downlink", ""},
		{http.MethodPost, devPath + "/devstatus", ""},
		{http.MethodGet, devPath + "/session", ""},
		{http.MethodGet, "/api/v1/status", ""},
	}
	for _, r := range routes {
		rec := call(t, New(&stubRepository{}, nil, nil), r.method, r.path, r.body)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: status = %d, want 503", r.method, r.path, rec.Code)
		}
		if got := errorCode(t, rec); got != ErrControllerNotConfigured {
			t.Errorf("%s %s: error = %q, want %q", r.method, r.path, got, ErrControllerNotConfigured)
		}
	}
}

// TestUpstreamRejectionKeepsStatusAndDetail: "this device has never joined" is
// worth more to an operator than "downlink_queue_failed", so the network
// server's own reason has to survive the hop.
func TestUpstreamRejectionKeepsStatusAndDetail(t *testing.T) {
	ctl := &stubController{
		err: &nsclient.APIError{Status: http.StatusNotFound, Detail: "device has never joined"},
	}
	rec := call(t, New(&stubRepository{}, nil, ctl), http.MethodPost,
		devPath+"/downlink", `{"data":"01"}`)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want the network server's 404", rec.Code)
	}
	var body struct {
		Error  string `json:"error"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	if body.Error != ErrDownlinkQueueFailed {
		t.Errorf("error = %q, want %q", body.Error, ErrDownlinkQueueFailed)
	}
	if body.Detail != "device has never joined" {
		t.Errorf("detail = %q, want the network server's reason", body.Detail)
	}
}

// TestUpstreamUnreachableIsBadGateway: the two failure shapes must stay
// distinguishable, because one means "restart the network server" and the other
// means "your request was wrong".
func TestUpstreamUnreachableIsBadGateway(t *testing.T) {
	ctl := &stubController{
		err: &nsclient.TransportError{URL: "http://ns:8091", Err: context.DeadlineExceeded},
	}
	rec := call(t, New(&stubRepository{}, nil, ctl), http.MethodGet, devPath+"/session", "")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if got := errorCode(t, rec); got != ErrNetworkServerUnreachable {
		t.Errorf("error = %q, want %q", got, ErrNetworkServerUnreachable)
	}
}

// TestSessionRelaysUnknownFieldsVerbatim pins the passthrough: a field this
// console has never heard of must still reach the browser, so a network-server
// upgrade shows up as a new value rather than as a column that quietly emptied.
func TestSessionRelaysUnknownFieldsVerbatim(t *testing.T) {
	ctl := &stubController{resp: json.RawMessage(
		`{"dev_eui":"0011223344556677","data_rate":5,"a_field_invented_tomorrow":42}`)}
	rec := call(t, New(&stubRepository{}, nil, ctl), http.MethodGet, devPath+"/session", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"a_field_invented_tomorrow":42`) {
		t.Errorf("the body was rewritten and lost the unknown field: %s", rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q", got)
	}
}

func TestStatusRelaysNetworkServerHealth(t *testing.T) {
	ctl := &stubController{}
	rec := call(t, New(&stubRepository{}, nil, ctl), http.MethodGet, "/api/v1/status", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"gateways_online":1`) {
		t.Errorf("body = %s", rec.Body)
	}
}

func TestEditDeviceRelaysTheFieldsThatWereSent(t *testing.T) {
	ctl := &stubController{}
	rec := call(t, New(&stubRepository{}, nil, ctl), http.MethodPatch,
		devPath, `{"name":"Renamed","disable_adr":true}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if ctl.calls != 1 {
		t.Fatalf("the network server was called %d times, want 1", ctl.calls)
	}
	if ctl.gotDevEUI != "0011223344556677" {
		t.Errorf("DevEUI = %q", ctl.gotDevEUI)
	}
	// Only what the form actually changed may reach the network server. A nil
	// here means "leave alone", and the ns depends on that distinction.
	if ctl.gotPatch.Name == nil || *ctl.gotPatch.Name != "Renamed" {
		t.Errorf("Name = %v, want \"Renamed\"", ctl.gotPatch.Name)
	}
	if ctl.gotPatch.DisableADR == nil || !*ctl.gotPatch.DisableADR {
		t.Errorf("DisableADR = %v, want true", ctl.gotPatch.DisableADR)
	}
	if ctl.gotPatch.Enabled != nil || ctl.gotPatch.Group != nil ||
		ctl.gotPatch.SupportsClassC != nil || ctl.gotPatch.MaxDataRate != nil ||
		ctl.gotPatch.MinDataRate != nil || ctl.gotPatch.MaxTxPower != nil {
		t.Errorf("fields that were not sent must stay nil, got %+v", ctl.gotPatch)
	}
	// The network server's own view is passed through, so a field this console
	// has not heard of still reaches the dashboard.
	if !strings.Contains(rec.Body.String(), `"name":"edited"`) {
		t.Errorf("the upstream response was not relayed: %s", rec.Body)
	}
}

func TestEditDeviceRejectsAnEmptyBody(t *testing.T) {
	ctl := &stubController{}
	rec := call(t, New(&stubRepository{}, nil, ctl), http.MethodPatch, devPath, `{}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), ErrNoFieldsToChange) {
		t.Errorf("body = %s, want %s", rec.Body, ErrNoFieldsToChange)
	}
	// Nothing should have been sent: an empty patch is a form submitted
	// unchanged, not a request the network server needs to see.
	if ctl.calls != 0 {
		t.Errorf("the network server was called %d times for an empty patch", ctl.calls)
	}
}

func TestEditDeviceRejectsBadInputLocally(t *testing.T) {
	cases := map[string]string{
		"blank name":      `{"name":"   "}`,
		"name too long":   `{"name":"` + strings.Repeat("n", 121) + `"}`,
		"group too long":  `{"group":"` + strings.Repeat("g", 61) + `"}`,
		"data rate high":  `{"max_data_rate":16}`,
		"data rate low":   `{"min_data_rate":-2}`,
		"tx power high":   `{"max_tx_power":99}`,
		"min above max":   `{"min_data_rate":6,"max_data_rate":3}`,
		"unknown field":   `{"name":"ok","app_key":"2b7e151628aed2a6abf7158809cf4f3c"}`,
		"trailing object": `{"name":"ok"} {"name":"again"}`,
		"not an object":   `["name"]`,
		"wrong type":      `{"enabled":"yes"}`,
	}
	for label, body := range cases {
		ctl := &stubController{}
		rec := call(t, New(&stubRepository{}, nil, ctl), http.MethodPatch, devPath, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400: %s", label, rec.Code, rec.Body)
		}
		// A rejection this layer can make must not reach the network server.
		if ctl.calls != 0 {
			t.Errorf("%s: the network server was called %d times for input that should be refused locally",
				label, ctl.calls)
		}
	}
}

// TestEditDeviceAcceptsRegionDefault pins -1 as "no limit". A console needs to
// lift a restriction, and 0 would mean DR0 rather than "unrestricted".
func TestEditDeviceAcceptsRegionDefault(t *testing.T) {
	ctl := &stubController{}
	rec := call(t, New(&stubRepository{}, nil, ctl), http.MethodPatch,
		devPath, `{"max_data_rate":-1,"min_data_rate":-1,"max_tx_power":-1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if ctl.gotPatch.MaxDataRate == nil || *ctl.gotPatch.MaxDataRate != -1 {
		t.Errorf("MaxDataRate = %v, want -1", ctl.gotPatch.MaxDataRate)
	}
}

// TestEditDeviceAllowsMinAboveAnUnrestrictedMax: -1 is not a bound, so min 6
// with max -1 is a valid request rather than an ordering violation.
func TestEditDeviceAllowsMinAboveAnUnrestrictedMax(t *testing.T) {
	ctl := &stubController{}
	rec := call(t, New(&stubRepository{}, nil, ctl), http.MethodPatch,
		devPath, `{"min_data_rate":6,"max_data_rate":-1}`)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
}

func TestEditDeviceTrimsWhitespace(t *testing.T) {
	ctl := &stubController{}
	if rec := call(t, New(&stubRepository{}, nil, ctl), http.MethodPatch,
		devPath, `{"name":"  Padded  ","group":"  Floor 2  "}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if ctl.gotPatch.Name == nil || *ctl.gotPatch.Name != "Padded" {
		t.Errorf("Name = %v, want the trimmed \"Padded\"", ctl.gotPatch.Name)
	}
	if ctl.gotPatch.Group == nil || *ctl.gotPatch.Group != "Floor 2" {
		t.Errorf("Group = %v, want the trimmed \"Floor 2\"", ctl.gotPatch.Group)
	}
}

// TestEditDeviceWithoutANetworkServerIsUnavailable: a console with no ns cannot
// edit a device, and 503 says so rather than reporting a save that did nothing.
func TestEditDeviceWithoutANetworkServerIsUnavailable(t *testing.T) {
	rec := call(t, New(&stubRepository{}, nil, nil), http.MethodPatch, devPath, `{"name":"x"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), ErrControllerNotConfigured) {
		t.Errorf("body = %s, want %s", rec.Body, ErrControllerNotConfigured)
	}
}

// TestEditDeviceKeepsTheNetworkServersRejection: a 404 for a device that was
// deleted elsewhere must arrive as 404, not as a generic 502, so the dashboard
// can say "this device is gone" instead of "try again".
func TestEditDeviceKeepsTheNetworkServersRejection(t *testing.T) {
	ctl := &stubController{err: &nsclient.APIError{Status: http.StatusNotFound, Detail: "not provisioned"}}
	rec := call(t, New(&stubRepository{}, nil, ctl), http.MethodPatch, devPath, `{"name":"x"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), ErrDeviceUpdateFailed) {
		t.Errorf("body = %s, want %s", rec.Body, ErrDeviceUpdateFailed)
	}
}

func TestEditDeviceReportsAnUnreachableNetworkServer(t *testing.T) {
	ctl := &stubController{err: &nsclient.TransportError{URL: "http://ns:8091", Err: context.DeadlineExceeded}}
	rec := call(t, New(&stubRepository{}, nil, ctl), http.MethodPatch, devPath, `{"name":"x"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), ErrNetworkServerUnreachable) {
		t.Errorf("body = %s, want %s", rec.Body, ErrNetworkServerUnreachable)
	}
}

func TestDeleteDeviceRevokesThroughTheNetworkServer(t *testing.T) {
	ctl := &stubController{}
	rec := call(t, New(&stubRepository{}, nil, ctl), http.MethodDelete, devPath, "")

	// 204: there is nothing left to return.
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if ctl.calls != 1 || ctl.gotDevEUI != "0011223344556677" {
		t.Errorf("calls = %d, DevEUI = %q", ctl.calls, ctl.gotDevEUI)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("a 204 must carry no body, got %s", rec.Body)
	}
}

func TestDeleteDeviceKeepsTheNetworkServersRejection(t *testing.T) {
	ctl := &stubController{err: &nsclient.APIError{Status: http.StatusNotFound, Detail: "not provisioned"}}
	rec := call(t, New(&stubRepository{}, nil, ctl), http.MethodDelete, devPath, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), ErrDeviceDeleteFailed) {
		t.Errorf("body = %s, want %s", rec.Body, ErrDeviceDeleteFailed)
	}
}

func TestDeleteDeviceWithoutANetworkServerIsUnavailable(t *testing.T) {
	rec := call(t, New(&stubRepository{}, nil, nil), http.MethodDelete, devPath, "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
}

func TestEditAndDeleteRejectAMalformedDevEUI(t *testing.T) {
	for _, method := range []string{http.MethodPatch, http.MethodDelete} {
		rec := call(t, New(&stubRepository{}, nil, &stubController{}), method,
			"/api/v1/devices/not-an-eui", `{"name":"x"}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400: %s", method, rec.Code, rec.Body)
		}
	}
}

// TestReadDeviceSettingsRelaysTheKeysFreeView: the settings an edit form needs
// must arrive, and the response must still be key-free because it is relayed
// verbatim from an endpoint that omits them by construction.
func TestReadDeviceSettingsRelaysTheKeysFreeView(t *testing.T) {
	ctl := &stubController{}
	rec := call(t, New(&stubRepository{}, nil, ctl), http.MethodGet, devPath, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if ctl.calls != 1 || ctl.gotDevEUI != "0011223344556677" {
		t.Errorf("calls = %d, DevEUI = %q", ctl.calls, ctl.gotDevEUI)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"disable_adr":false`) {
		t.Errorf("the settings an edit form needs did not arrive: %s", body)
	}
	for _, forbidden := range []string{"app_key", "nwk_s_key", "app_s_key"} {
		if strings.Contains(body, `"`+forbidden+`"`) {
			t.Errorf("the relayed view carries a key field %q: %s", forbidden, body)
		}
	}
}

func TestReadDeviceWithoutANetworkServerIsUnavailable(t *testing.T) {
	rec := call(t, New(&stubRepository{}, nil, nil), http.MethodGet, devPath, "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
}

// TestSessionRouteStillWinsOverTheDeviceRoute guards the mux pattern ordering:
// /devices/{eui}/session and /devices/{eui} are registered together, and a
// mistake here would make the session panel read device settings instead.
func TestSessionRouteStillWinsOverTheDeviceRoute(t *testing.T) {
	ctl := &stubController{}
	rec := call(t, New(&stubRepository{}, nil, ctl), http.MethodGet, devPath+"/session", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"data_rate":5`) {
		t.Errorf("the session endpoint did not answer: %s", rec.Body)
	}
}
