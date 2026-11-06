package httpapi

import (
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
		{name: "unsupported region", settings: domain.FacilitySettings{FacilityName: "Site", Region: "US915", OperatorName: "Operator", OperatorRole: "Admin"}, want: "unsupported_region"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := validateFacilitySettings(test.settings); got != test.want {
				t.Errorf("validateFacilitySettings() = %q, want %q", got, test.want)
			}
		})
	}
}
