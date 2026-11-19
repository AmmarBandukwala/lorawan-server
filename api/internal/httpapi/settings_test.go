package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fieldnode/lorawan-server/api/internal/domain"
)

func TestValidateFacilitySettings(t *testing.T) {
	validSettings := domain.FacilitySettings{
		FacilityName: "West facility",
		Region:       "EU868",
		OperatorName: "Facility operator",
		OperatorRole: "Administrator",
	}

	tests := []struct {
		name     string
		settings domain.FacilitySettings
		want     string
	}{
		{name: "valid profile", settings: validSettings},
		{name: "missing facility", settings: domain.FacilitySettings{Region: "EU868", OperatorName: "Operator", OperatorRole: "Admin"}, want: "invalid_settings"},
		{name: "operator role too long", settings: domain.FacilitySettings{FacilityName: "Site", Region: "EU868", OperatorName: "Operator", OperatorRole: strings.Repeat("a", 81)}, want: "invalid_settings"},
		{name: "region the server does not implement", settings: domain.FacilitySettings{FacilityName: "Site", Region: "IN865", OperatorName: "Operator", OperatorRole: "Admin"}, want: "unsupported_region"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := validateFacilitySettings(test.settings, defaultSupportedRegions); got != test.want {
				t.Errorf("validateFacilitySettings() = %q, want %q", got, test.want)
			}
		})
	}
}

// TestListRegionsRelaysTheNetworkServerList is the point of the endpoint: the
// console's dropdown is the network server's list, not a copy that drifts.
func TestListRegionsRelaysTheNetworkServerList(t *testing.T) {
	controller := &stubController{resp: json.RawMessage(
		`{"region":"US915","supported_regions":["US915","EU868","AS923-3"]}`)}
	server := httptest.NewServer(New(&stubRepository{}, nil, controller).Handler())
	defer server.Close()

	body := getJSON(t, server.URL+"/api/v1/regions")
	if got := body["source"]; got != "network-server" {
		t.Errorf("source = %v, want network-server", got)
	}
	regions, ok := body["regions"].([]any)
	if !ok || len(regions) != 3 {
		t.Fatalf("regions = %v, want the three the network server advertised", body["regions"])
	}
	if regions[0] != "US915" || regions[2] != "AS923-3" {
		t.Errorf("regions = %v, want the server's order preserved", regions)
	}
}

// TestListRegionsFallsBackWhenTheServerIsUnreachable: a network server that is
// restarting must leave the settings form usable, and must say the list is a
// fallback rather than presenting a guess as an answer.
func TestListRegionsFallsBackWhenTheServerIsUnreachable(t *testing.T) {
	controller := &stubController{err: errors.New("connection refused")}
	server := httptest.NewServer(New(&stubRepository{}, nil, controller).Handler())
	defer server.Close()

	body := getJSON(t, server.URL+"/api/v1/regions")
	if got := body["source"]; got != "fallback" {
		t.Errorf("source = %v, want fallback", got)
	}
	regions, ok := body["regions"].([]any)
	if !ok || len(regions) != len(defaultSupportedRegions) {
		t.Fatalf("regions = %v, want the %d-entry fallback", body["regions"], len(defaultSupportedRegions))
	}
}

// TestListRegionsFallsBackForAnOlderServer covers the upgrade order where the
// console is newer than the network server: a status body without the field must
// degrade to the fallback instead of offering an empty dropdown.
func TestListRegionsFallsBackForAnOlderServer(t *testing.T) {
	controller := &stubController{resp: json.RawMessage(`{"region":"EU868","gateways_online":1}`)}
	server := httptest.NewServer(New(&stubRepository{}, nil, controller).Handler())
	defer server.Close()

	body := getJSON(t, server.URL+"/api/v1/regions")
	if got := body["source"]; got != "fallback" {
		t.Errorf("source = %v, want fallback for a status body without supported_regions", got)
	}
}

// TestListRegionsWithoutAController: the console runs with provisioning disabled
// by design, so a nil controller must not panic on the settings page.
func TestListRegionsWithoutAController(t *testing.T) {
	server := httptest.NewServer(New(&stubRepository{}, nil, nil).Handler())
	defer server.Close()

	body := getJSON(t, server.URL+"/api/v1/regions")
	if got := body["source"]; got != "fallback" {
		t.Errorf("source = %v, want fallback", got)
	}
}

// TestSupportedRegionsIsCached keeps a settings save from paying a round trip per
// keystroke-triggered validation. The list only changes when the server is
// rebuilt, so one fetch per minute is the right cost.
func TestSupportedRegionsIsCached(t *testing.T) {
	controller := &stubController{resp: json.RawMessage(
		`{"supported_regions":["US915","EU868"]}`)}
	server := httptest.NewServer(New(&stubRepository{}, nil, controller).Handler())
	defer server.Close()

	for i := 0; i < 5; i++ {
		getJSON(t, server.URL+"/api/v1/regions")
	}
	if controller.calls != 1 {
		t.Errorf("the network server was asked %d times for five requests, want 1", controller.calls)
	}
}

// TestUpdateSettingsAcceptsABandOnlyTheServerKnowsAbout is the regression this
// whole change is about: AS923-3 was implemented by the network server and
// refused by the console, because the console kept its own shorter list.
func TestUpdateSettingsAcceptsABandOnlyTheServerKnowsAbout(t *testing.T) {
	controller := &stubController{resp: json.RawMessage(
		`{"supported_regions":["US915","EU868","AS923","AS923-2","AS923-3","AS923-4"]}`)}
	server := httptest.NewServer(New(&stubRepository{}, nil, controller).Handler())
	defer server.Close()

	response, payload := putSettings(t, server.URL, `{"facilityName":"Site",
		"region":"AS923-3","operatorName":"Operator","operatorRole":"Admin"}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d (%v), want 200: the network server implements AS923-3",
			response.StatusCode, payload)
	}
}

// TestUpdateSettingsRefusesABandTheServerDoesNotImplement: widening the list must
// not have turned validation into nothing.
func TestUpdateSettingsRefusesABandTheServerDoesNotImplement(t *testing.T) {
	controller := &stubController{resp: json.RawMessage(`{"supported_regions":["US915"]}`)}
	server := httptest.NewServer(New(&stubRepository{}, nil, controller).Handler())
	defer server.Close()

	response, payload := putSettings(t, server.URL, `{"facilityName":"Site",
		"region":"AU915","operatorName":"Operator","operatorRole":"Admin"}`)
	if response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 for a band the server does not implement",
			response.StatusCode)
	}
	if !strings.Contains(payload, ErrUnsupportedRegion) {
		t.Errorf("payload = %s, want it to name %s", payload, ErrUnsupportedRegion)
	}
}

func getJSON(t *testing.T, url string) map[string]any {
	t.Helper()
	response, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d, want 200", url, response.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
	return body
}

func putSettings(t *testing.T, baseURL, body string) (*http.Response, string) {
	t.Helper()
	// A real PUT: the route is registered as "PUT /api/v1/settings", and a POST
	// would be answered 405 by the mux rather than by the validator, which is a
	// different question than the one these tests ask.
	request, err := http.NewRequest(http.MethodPut, baseURL+"/api/v1/settings", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("PUT settings: %v", err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	return response, string(raw)
}

// TestValidateFacilitySettingsEveryAdvertisedBand: any band the fallback list
// advertises must be storable. The bug this guards against is the list and the
// validator disagreeing, which left AS923-3 selectable in principle and
// unsavable in practice.
func TestValidateFacilitySettingsEveryAdvertisedBand(t *testing.T) {
	for _, region := range defaultSupportedRegions {
		settings := domain.FacilitySettings{
			FacilityName: "Site", Region: region,
			OperatorName: "Operator", OperatorRole: "Admin",
		}
		if got := validateFacilitySettings(settings, defaultSupportedRegions); got != "" {
			t.Errorf("band %s was rejected with %q, although it is advertised", region, got)
		}
	}
}

// TestValidateFacilitySettingsUsesTheGivenList proves the validator follows the
// network server rather than its own opinion: a band only the server knows about
// must be accepted, and one the server does not must be refused even when the
// compiled-in list has it.
func TestValidateFacilitySettingsUsesTheGivenList(t *testing.T) {
	settings := domain.FacilitySettings{
		FacilityName: "Site", Region: "AU915",
		OperatorName: "Operator", OperatorRole: "Admin",
	}
	if got := validateFacilitySettings(settings, []string{"AU915", "US915"}); got != "" {
		t.Errorf("a band the network server advertises was rejected with %q", got)
	}
	settings.Region = "EU868"
	if got := validateFacilitySettings(settings, []string{"US915"}); got != "unsupported_region" {
		t.Errorf("a band the network server does not advertise = %q, want unsupported_region", got)
	}
	// An empty list must refuse rather than accept everything: it means the list
	// could not be read, which is not the same as every band being available.
	if got := validateFacilitySettings(settings, nil); got != "unsupported_region" {
		t.Errorf("an empty band list = %q, want unsupported_region", got)
	}
}
