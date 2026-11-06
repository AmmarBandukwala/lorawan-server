package httpapi

import (
	"context"
	"net/http"

	"github.com/fieldnode/lorawan-server/api/internal/domain"
)

type Repository interface {
	Ping(context.Context) error
	ListDevices(context.Context) ([]domain.Device, error)
	CreateDevice(context.Context, domain.CreateDevice) error
	GetOverview(context.Context) (domain.Overview, error)
	ListGateways(context.Context) ([]domain.Gateway, error)
	ListEvents(context.Context) ([]domain.NetworkEvent, error)
	GetSettings(context.Context) (domain.FacilitySettings, error)
	UpdateSettings(context.Context, domain.FacilitySettings) (domain.FacilitySettings, error)
}

type Server struct {
	repository Repository
}

func New(repository Repository) *Server {
	return &Server{repository: repository}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /api/v1/overview", s.getOverview)
	mux.HandleFunc("GET /api/v1/devices", s.listDevices)
	mux.HandleFunc("POST /api/v1/devices", s.createDevice)
	mux.HandleFunc("GET /api/v1/gateways", s.listGateways)
	mux.HandleFunc("GET /api/v1/events", s.listEvents)
	mux.HandleFunc("GET /api/v1/settings", s.getSettings)
	mux.HandleFunc("PUT /api/v1/settings", s.updateSettings)
	return withSecurityHeaders(mux)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if err := s.repository.Ping(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}
