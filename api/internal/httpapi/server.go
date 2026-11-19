package httpapi

import (
	"context"
	"net/http"

	"github.com/fieldnode/lorawan-server/api/internal/domain"
)

// Repository reads the network's state. Every method is a read: the dashboard
// does not write protocol state, it asks the network server to do that.
type Repository interface {
	Ping(context.Context) error
	ListDevices(context.Context) ([]domain.Device, error)
	GetOverview(context.Context) (domain.Overview, error)
	ListGateways(context.Context) ([]domain.Gateway, error)
	ListEvents(context.Context) ([]domain.NetworkEvent, error)
	GetSettings(context.Context) (domain.FacilitySettings, error)
	UpdateSettings(context.Context, domain.FacilitySettings) (domain.FacilitySettings, error)
}

// Provisioner puts a device on the network. It is a separate port from
// Repository because it is the one write this service makes, and it goes to the
// network server rather than to the database — see nsclient for why.
//
// A nil Provisioner is allowed and means "provisioning is not configured": the
// endpoint then answers 503 instead of pretending the device was created.
type Provisioner interface {
	CreateDevice(ctx context.Context, d Provisioning) (ProvisionedDevice, error)
}

// Provisioning is the dashboard's request to put a device on the network.
type Provisioning struct {
	DevEUI  string
	JoinEUI string
	Name    string
	Group   string
	AppKey  string
}

// ProvisionedDevice is what came back. Credentials are not part of it: the
// network server does not return them and neither should this.
type ProvisionedDevice struct {
	DevEUI     string
	Name       string
	Group      string
	Activation string
}

type Server struct {
	repository  Repository
	provisioner Provisioner
}

func New(repository Repository, provisioner Provisioner) *Server {
	return &Server{repository: repository, provisioner: provisioner}
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
