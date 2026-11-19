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

// supportedRegions is exactly the set the network server implements regional
// parameters for. Offering more in the console would let an operator record a
// region the server then ignores, since the enforced region is NS_REGION on the
// ns itself — a profile that disagrees with the running network is worse than a
// shorter list.
var supportedRegions = map[string]struct{}{"EU868": {}, "US915": {}, "AS923": {}}

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
	if errorCode := validateFacilitySettings(settings); errorCode != "" {
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

func validateFacilitySettings(settings domain.FacilitySettings) string {
	if n := utf8.RuneCountInString(settings.FacilityName); n == 0 || n > MaxNameLen {
		return ErrInvalidSettings
	}
	if n := utf8.RuneCountInString(settings.OperatorName); n == 0 || n > MaxOperatorNameLen {
		return ErrInvalidSettings
	}
	if n := utf8.RuneCountInString(settings.OperatorRole); n == 0 || n > MaxOperatorRoleLen {
		return ErrInvalidSettings
	}
	if _, ok := supportedRegions[settings.Region]; !ok {
		return ErrUnsupportedRegion
	}
	return ""
}

func toSettingsResponse(settings domain.FacilitySettings) settingsResponse {
	return settingsResponse{
		Configured: settings.Configured, FacilityName: settings.FacilityName,
		Region: settings.Region, OperatorName: settings.OperatorName, OperatorRole: settings.OperatorRole,
	}
}
