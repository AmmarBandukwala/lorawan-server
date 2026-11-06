package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
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

var supportedRegions = map[string]struct{}{"EU868": {}}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.repository.GetSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "settings_query_failed")
		return
	}
	writeJSON(w, http.StatusOK, toSettingsResponse(settings))
}

func (s *Server) updateSettings(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	var request settingsRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_json")
		return
	}

	settings := domain.FacilitySettings{
		FacilityName: strings.TrimSpace(request.FacilityName),
		Region:       strings.ToUpper(strings.TrimSpace(request.Region)),
		OperatorName: strings.TrimSpace(request.OperatorName),
		OperatorRole: strings.TrimSpace(request.OperatorRole),
	}
	if errorCode := validateFacilitySettings(settings); errorCode != "" {
		writeError(w, http.StatusUnprocessableEntity, errorCode)
		return
	}
	updated, err := s.repository.UpdateSettings(r.Context(), settings)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "settings_update_failed")
		return
	}
	writeJSON(w, http.StatusOK, toSettingsResponse(updated))
}

func validateFacilitySettings(settings domain.FacilitySettings) string {
	if utf8.RuneCountInString(settings.FacilityName) == 0 || utf8.RuneCountInString(settings.FacilityName) > 120 ||
		utf8.RuneCountInString(settings.OperatorName) == 0 || utf8.RuneCountInString(settings.OperatorName) > 120 ||
		utf8.RuneCountInString(settings.OperatorRole) == 0 || utf8.RuneCountInString(settings.OperatorRole) > 80 {
		return "invalid_settings"
	}
	if _, ok := supportedRegions[settings.Region]; !ok {
		return "unsupported_region"
	}
	return ""
}

func toSettingsResponse(settings domain.FacilitySettings) settingsResponse {
	return settingsResponse{
		Configured: settings.Configured, FacilityName: settings.FacilityName,
		Region: settings.Region, OperatorName: settings.OperatorName, OperatorRole: settings.OperatorRole,
	}
}
