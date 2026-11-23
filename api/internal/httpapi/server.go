package httpapi

import (
	"context"
	"net/http"
	"sync"
	"time"

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

// Server holds the handlers' shared dependencies: where inventory is read
// from, how devices get provisioned, and how the live network is controlled.
type Server struct {
	repository  Repository
	provisioner Provisioner
	controller  Controller

	// agentAccess is the MCP listener's health probe, or nil when this deployment
	// has no listener. Optional by construction: the console is useful without it,
	// and the page has a truthful thing to say either way.
	agentAccess AgentAccess

	// The band list the network server publishes, cached briefly so saving a
	// facility profile does not pay a round trip. See supportedRegions.
	regionMu      sync.Mutex
	regionCache   []string
	regionCacheAt time.Time
}

// Option adjusts a Server at construction. It exists so an optional dependency can
// be added without touching the call sites that pass three arguments.
type Option func(*Server)

// WithAgentAccess points the console at an MCP listener to check. A nil value is
// ignored, so a caller can pass WithAgentAccess(NewAgentAccess(os.Getenv("MCP_URL")))
// directly and let an unset variable mean "not configured".
func WithAgentAccess(access AgentAccess) Option {
	return func(s *Server) {
		if access != nil {
			s.agentAccess = access
		}
	}
}

// New builds the console API. provisioner and controller may be nil, which means
// the console is running without a network server: reads still work and the
// endpoints that need one answer 503 instead of inventing a success.
func New(repository Repository, provisioner Provisioner, controller Controller,
	opts ...Option) *Server {
	s := &Server{repository: repository, provisioner: provisioner, controller: controller}
	for _, opt := range opts {
		opt(s)
	}
	return s
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
	// The band list the settings form offers. It is its own endpoint rather than
	// a field on /settings because it describes what this build can do, not what
	// the operator has configured.
	mux.HandleFunc("GET /api/v1/regions", s.listRegions)

	// Control plane. These make a device transmit, so they are grouped and
	// commented as such: a read endpoint returning an error is a broken
	// dashboard, and a write endpoint returning an error is a device that did not
	// get its frame.
	mux.HandleFunc("POST /api/v1/devices/{dev_eui}/downlink", s.enqueueDownlink)
	mux.HandleFunc("DELETE /api/v1/devices/{dev_eui}/downlink", s.cancelDownlink)
	mux.HandleFunc("POST /api/v1/devices/{dev_eui}/devstatus", s.requestDeviceStatus)
	mux.HandleFunc("GET /api/v1/devices/{dev_eui}/session", s.getSession)
	mux.HandleFunc("GET /api/v1/devices/{dev_eui}", s.getDevice)
	mux.HandleFunc("PATCH /api/v1/devices/{dev_eui}", s.updateDevice)
	mux.HandleFunc("DELETE /api/v1/devices/{dev_eui}", s.deleteDevice)
	mux.HandleFunc("GET /api/v1/status", s.getStatus)

	// Device models and decoded uplink history, relayed to the network server.
	// A model owns the payload formatter that turns a device's bytes into
	// readings, and only the network server can say whether a script compiles —
	// so these pass through rather than being re-modelled here.
	mux.HandleFunc("GET /api/v1/models", s.listModels)
	mux.HandleFunc("POST /api/v1/models/{model_id}", s.createModel)
	mux.HandleFunc("GET /api/v1/models/{model_id}", s.getModel)
	mux.HandleFunc("PUT /api/v1/models/{model_id}", s.updateModel)
	mux.HandleFunc("DELETE /api/v1/models/{model_id}", s.deleteModel)
	mux.HandleFunc("POST /api/v1/models/{model_id}/decode", s.testModelDecode)

	// Integration destinations, relayed to the network server. The id travels in
	// the body on create, matching the network server's own shape.
	mux.HandleFunc("GET /api/v1/integrations", s.listIntegrations)
	mux.HandleFunc("POST /api/v1/integrations", s.createIntegration)
	mux.HandleFunc("GET /api/v1/integrations/{id}", s.getIntegration)
	mux.HandleFunc("PUT /api/v1/integrations/{id}", s.updateIntegration)
	mux.HandleFunc("DELETE /api/v1/integrations/{id}", s.deleteIntegration)
	mux.HandleFunc("POST /api/v1/integrations/{id}/test", s.testIntegration)
	mux.HandleFunc("GET /api/v1/uplinks", s.listUplinks)

	// Whether an MCP listener is reachable. Read-only, and answered 200 even when
	// the listener is down: the console established that fact, it did not fail.
	mux.HandleFunc("GET /api/v1/mcp", s.getAgentAccess)
	return withSecurityHeaders(mux)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if err := s.repository.Ping(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, ErrDatabaseUnavailable)
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
