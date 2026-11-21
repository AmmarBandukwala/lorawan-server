package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/fieldnode/lorawan-server/api/internal/nsclient"
)

// The integration relay's contract: right call, right id, body byte for byte, and
// the network server's answer returned unchanged. The body rule matters more here
// than it does for models, because an integration body can carry a broker
// credential — a relay that dropped a field would quietly disarm a destination.

const integrationBodyJSON = `{"id":"cloud-mq","kind":"mqtt","name":"Cloud broker",` +
	`"mqtt":{"broker_url":"tls://broker.example:8883","topic":"fieldnode/up","qos":1},` +
	`"secret":"broker-password"}`

func TestIntegrationBodyIsRelayedByteForByte(t *testing.T) {
	ctrl := &recordingController{}
	rec := doRequest(t, New(nil, nil, ctrl).Handler(), http.MethodPost, "/api/v1/integrations", integrationBodyJSON)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if ctrl.integrationBody != integrationBodyJSON {
		t.Errorf("forwarded body = %s, want %s", ctrl.integrationBody, integrationBodyJSON)
	}
}

func TestIntegrationUpdateCarriesThePathIDAndTheBody(t *testing.T) {
	ctrl := &recordingController{}
	rec := doRequest(t, New(nil, nil, ctrl).Handler(), http.MethodPut,
		"/api/v1/integrations/cloud-mq", integrationBodyJSON)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if ctrl.integrationID != "cloud-mq" {
		t.Errorf("id = %q, want cloud-mq", ctrl.integrationID)
	}
	if ctrl.integrationBody != integrationBodyJSON {
		t.Errorf("forwarded body = %s, want %s", ctrl.integrationBody, integrationBodyJSON)
	}
}

// TestIntegrationResponseIsReturnedVerbatim covers the failing probe, which is a
// 200 with ok:false at the network server. Rewriting it as an error would throw
// away the reason the operator needs.
func TestIntegrationResponseIsReturnedVerbatim(t *testing.T) {
	reply := json.RawMessage(`{"ok":false,"detail":"dial tcp: broker.example:8883: i/o timeout"}`)
	ctrl := &recordingController{reply: reply}
	rec := doRequest(t, New(nil, nil, ctrl).Handler(), http.MethodPost,
		"/api/v1/integrations/cloud-mq/test", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: a refused probe is an answer, not a transport error", rec.Code)
	}
	if rec.Body.String() != string(reply) {
		t.Errorf("body = %s, want %s", rec.Body.String(), reply)
	}
}

func TestIntegrationIDIsValidatedBeforeTheRoundTrip(t *testing.T) {
	for _, id := range []string{"A-bad", "a", "has%20space", "UPPER", "_leading", strings.Repeat("x", 65)} {
		ctrl := &recordingController{}
		rec := doRequest(t, New(nil, nil, ctrl).Handler(), http.MethodGet, "/api/v1/integrations/"+id, "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("id %q: status = %d, want 400", id, rec.Code)
			continue
		}
		// The network server must not be asked at all: the request was already
		// known to be wrong.
		if ctrl.method != "" {
			t.Errorf("id %q reached the network server (%s)", id, ctrl.method)
		}
	}
}

func TestIntegrationDeleteIsRelayedAsNoContent(t *testing.T) {
	ctrl := &recordingController{}
	rec := doRequest(t, New(nil, nil, ctrl).Handler(), http.MethodDelete,
		"/api/v1/integrations/cloud-mq", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("a 204 must carry no body, got %s", rec.Body.String())
	}
	if ctrl.integrationID != "cloud-mq" {
		t.Errorf("id = %q, want cloud-mq", ctrl.integrationID)
	}
}

func TestIntegrationListAndReadReachTheRightRoutes(t *testing.T) {
	list := &recordingController{}
	if rec := doRequest(t, New(nil, nil, list).Handler(), http.MethodGet,
		"/api/v1/integrations", ""); rec.Code != http.StatusOK {
		t.Fatalf("list status = %d", rec.Code)
	}
	if list.method != "LIST_INTEGRATIONS" {
		t.Errorf("list method = %q", list.method)
	}

	get := &recordingController{}
	if rec := doRequest(t, New(nil, nil, get).Handler(), http.MethodGet,
		"/api/v1/integrations/cloud-mq", ""); rec.Code != http.StatusOK {
		t.Fatalf("get status = %d", rec.Code)
	}
	if get.method != "GET_INTEGRATION" || get.integrationID != "cloud-mq" {
		t.Errorf("get = %s %q", get.method, get.integrationID)
	}
}

// TestIntegrationCredentialIsNotEchoedOnRejection: the network server's validation
// message is relayed because it is useful, but a rejection must never come back
// carrying the credential the operator just sent.
func TestIntegrationCredentialIsNotEchoedOnRejection(t *testing.T) {
	ctrl := &recordingController{
		err: &nsclient.APIError{
			Status: http.StatusBadRequest,
			Detail: `integration: destination "cloud-mq" needs a reachable broker`,
		},
	}
	rec := doRequest(t, New(nil, nil, ctrl).Handler(), http.MethodPost,
		"/api/v1/integrations", integrationBodyJSON)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want the network server's 400 relayed", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "broker-password") {
		t.Errorf("the rejection leaked the credential: %s", body)
	}
	// The advice survives, which is the point of relaying rather than replacing.
	if !strings.Contains(body, "reachable broker") {
		t.Errorf("the rejection lost the network server's reason: %s", body)
	}
}

func TestIntegrationOversizedBodyIsNamedAsSuch(t *testing.T) {
	body := `{"id":"big","kind":"webhook","name":"n","webhook":{"url":"https://a.test/` +
		strings.Repeat("x", maxIntegrationBody+1) + `"}}`
	ctrl := &recordingController{}
	rec := doRequest(t, New(nil, nil, ctrl).Handler(), http.MethodPost, "/api/v1/integrations", body)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), ErrIntegrationTooLarge) {
		t.Errorf("body = %s, want it to name the size limit rather than say invalid JSON",
			rec.Body.String())
	}
	if ctrl.method != "" {
		t.Errorf("an oversized body reached the network server (%s)", ctrl.method)
	}
}

func TestIntegrationRejectsAMalformedBody(t *testing.T) {
	for name, body := range map[string]string{
		"empty":      "",
		"not json":   `nonsense`,
		"truncated":  `{"id":"cloud-mq"`,
		"two values": `{"id":"a"} {"id":"b"}`,
	} {
		ctrl := &recordingController{}
		rec := doRequest(t, New(nil, nil, ctrl).Handler(), http.MethodPost, "/api/v1/integrations", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, rec.Code)
		}
		if ctrl.method != "" {
			t.Errorf("%s: a malformed body reached the network server (%s)", name, ctrl.method)
		}
	}
}

func TestIntegrationWithoutANetworkServer(t *testing.T) {
	rec := doRequest(t, New(nil, nil, nil).Handler(), http.MethodGet, "/api/v1/integrations", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), ErrControllerNotConfigured) {
		t.Errorf("body = %s, want it to say the network server is not configured", rec.Body.String())
	}
}

func TestIntegrationUnreachableNetworkServerIsABadGateway(t *testing.T) {
	ctrl := &recordingController{err: &nsclient.TransportError{
		URL: "http://ns:8080/api/v1/integrations",
		Err: context.DeadlineExceeded,
	}}
	rec := doRequest(t, New(nil, nil, ctrl).Handler(), http.MethodGet, "/api/v1/integrations", "")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "http://ns:8080") {
		t.Errorf("an internal URL leaked to the client: %s", rec.Body.String())
	}
}
