package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func relativeTime(now time.Time, value *time.Time) string {
	if value == nil {
		return "Never"
	}
	age := now.Sub(*value)
	if age < time.Minute {
		return "just now"
	}
	if age < time.Hour {
		return fmt.Sprintf("%d min ago", int(age.Minutes()))
	}
	if age < 24*time.Hour {
		return fmt.Sprintf("%d hours ago", int(age.Hours()))
	}
	return fmt.Sprintf("%d days ago", int(age.Hours()/24))
}
