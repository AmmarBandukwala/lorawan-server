package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fieldnode/lorawan-server/api/internal/domain"
)

type settingsRequest struct {
	FacilityName string `json:"facilityName"`
	Region       string `json:"region"`
	OperatorName string `json:"operatorName"`
	OperatorRole string `json:"operatorRole"`
}

type settingsResponse struct {
	Configured   bool   `json:"configured"`
	FacilityName string `json:"facilityName"`
	Region       string `json:"region"`
	OperatorName string `json:"operatorName"`
	OperatorRole string `json:"operatorRole"`
}

// defaultSupportedRegions is the fallback list, used only when the network
// server cannot be reached. The authoritative list comes from the network server
// itself (statusView.supported_regions, which is region.Names()), because a list
// kept here and another kept there drift: the console offered three bands while
// the server accepted six, and an operator could not record the band they were
// actually running.
//
// It is a fallback rather than nothing, because the facility profile is metadata
// and an operator should still be able to finish configuring a console whose
// network server is restarting.
var defaultSupportedRegions = []string{
	"US915", "EU868", "AS923", "AS923-2", "AS923-3", "AS923-4",
}

// regionCacheTTL bounds how long a list read from the network server is reused.
// The list only changes when the server is rebuilt, so this is about not making
// every settings save pay a round trip, not about freshness.
const regionCacheTTL = time.Minute

// supportedRegions returns the bands this deployment can be configured for, and
// where that answer came from, so the console can tell an operator whether the
// list is the network server's or a fallback.
func (s *Server) supportedRegions(ctx context.Context) (regions []string, source string) {
	fallback := func() ([]string, string) {
		out := make([]string, len(defaultSupportedRegions))
		copy(out, defaultSupportedRegions)
		return out, "fallback"
	}
	if s.controller == nil {
		return fallback()
	}

	s.regionMu.Lock()
	defer s.regionMu.Unlock()
	if len(s.regionCache) > 0 && time.Now().Before(s.regionCacheAt.Add(regionCacheTTL)) {
		out := make([]string, len(s.regionCache))
		copy(out, s.regionCache)
		return out, "network-server"
	}

	raw, err := s.controller.Status(ctx)
	if err != nil {
		// A network server that is restarting must not stop the console from
		// saving a facility name. The fallback is wider than wrong: it lets an
		// operator record a band the running server does not enforce, which the
		// console already surfaces elsewhere by showing the server's own region.
		return fallback()
	}
	var status struct {
		SupportedRegions []string `json:"supported_regions"`
	}
	if err := json.Unmarshal(raw, &status); err != nil || len(status.SupportedRegions) == 0 {
		// An older network server, or a malformed body. Either way the operator
		// gets a list rather than an error page.
		return fallback()
	}
	s.regionCache = append([]string(nil), status.SupportedRegions...)
	s.regionCacheAt = time.Now()
	return append([]string(nil), status.SupportedRegions...), "network-server"
}

// listRegions tells the console which bands can be chosen. The console renders
// this instead of keeping its own list, so a band added to the network server
// becomes selectable without a front-end change.
func (s *Server) listRegions(w http.ResponseWriter, r *http.Request) {
	regions, source := s.supportedRegions(r.Context())
	settings, err := s.repository.GetSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrSettingsQueryFailed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"regions": regions,
		"source":  source,
		// The band the network is actually enforcing, which is the network
		// server's setting and not this profile's. Showing both is what lets an
		// operator notice they disagree.
		"configured": settings.Region,
	})
}

// supportedRegionsFor is the list only, for validation.
func (s *Server) supportedRegionsFor(ctx context.Context) []string {
	regions, _ := s.supportedRegions(ctx)
	return regions
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.repository.GetSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrSettingsQueryFailed)
		return
	}
	writeJSON(w, http.StatusOK, toSettingsResponse(settings))
}

func (s *Server) updateSettings(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBytes)
	var request settingsRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidJSON)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, ErrInvalidJSON)
		return
	}

	settings := domain.FacilitySettings{
		FacilityName: strings.TrimSpace(request.FacilityName),
		Region:       strings.ToUpper(strings.TrimSpace(request.Region)),
		OperatorName: strings.TrimSpace(request.OperatorName),
		OperatorRole: strings.TrimSpace(request.OperatorRole),
	}
	// The band list comes from the network server, so a band it implements is
	// accepted here even when this build's fallback list has not caught up.
	if errorCode := validateFacilitySettings(settings, s.supportedRegionsFor(r.Context())); errorCode != "" {
		writeError(w, http.StatusUnprocessableEntity, errorCode)
		return
	}
	updated, err := s.repository.UpdateSettings(r.Context(), settings)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrSettingsUpdateFail)
		return
	}
	writeJSON(w, http.StatusOK, toSettingsResponse(updated))
}

// validateFacilitySettings checks a profile against the bands the deployment can
// be configured for. The list is a parameter rather than a package variable so
// the caller decides where it came from — the network server when reachable, the
// compiled-in fallback otherwise — and this stays a pure function.
func validateFacilitySettings(settings domain.FacilitySettings, regions []string) string {
	if length := utf8.RuneCountInString(settings.FacilityName); length == 0 || length > MaxNameLen {
		return ErrInvalidSettings
	}
	if length := utf8.RuneCountInString(settings.OperatorName); length == 0 || length > MaxOperatorNameLen {
		return ErrInvalidSettings
	}
	if length := utf8.RuneCountInString(settings.OperatorRole); length == 0 || length > MaxOperatorRoleLen {
		return ErrInvalidSettings
	}
	for _, r := range regions {
		if r == settings.Region {
			return ""
		}
	}
	return ErrUnsupportedRegion
}

func toSettingsResponse(settings domain.FacilitySettings) settingsResponse {
	return settingsResponse{
		Configured: settings.Configured, FacilityName: settings.FacilityName,
		Region: settings.Region, OperatorName: settings.OperatorName, OperatorRole: settings.OperatorRole,
	}
}
