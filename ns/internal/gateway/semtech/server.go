package semtech

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/gateway"
	"github.com/fieldnode/lorawan-server/ns/internal/region"
)

// Handler is the network-server side of the transport. engine.Engine
// implements it.
type Handler interface {
	HandleUplink(ctx context.Context, f gateway.UplinkFrame) error
}

// Config configures a Server.
type Config struct {
	// Addr is the UDP listen address, for example ":6858". 6858 is the
	// well-known packet-forwarder port; using another one means setting
	// server_port in the gateway's global_conf.json.
	Addr string

	// Region stamps received frames. Left empty, the engine's configured
	// default region applies, and downlinks still work because the engine
	// names the band on every DownlinkFrame.
	Region string

	Handler Handler
	Logger  *slog.Logger
	Clock   func() time.Time

	// GatewayTimeout decides "online" in the registry. Two PULL_DATA intervals
	// is the usual choice.
	GatewayTimeout time.Duration

	// MaxConcurrentPackets bounds how many datagrams are processed at once, so
	// a slow store cannot make the server spawn goroutines without limit.
	MaxConcurrentPackets int
}

// Server is a Semtech UDP packet-forwarder server: it receives uplinks and
// transmits downlinks, and it is the gateway.Gateway implementation the engine
// schedules against.
type Server struct {
	cfg  Config
	log  *slog.Logger
	now  func() time.Time
	conn *net.UDPConn
	reg  *registry
	// seq numbers the PULL_RESP packets we send. There is no atomic.Uint16 in
	// the standard library, so the counter is 32-bit and truncated: the
	// protocol's sequence field wraps the same way.
	seq    atomic.Uint32
	sem    chan struct{}
	wg     sync.WaitGroup
	closed atomic.Bool

	regionMu sync.Mutex
	regions  map[string]region.Region
}

// Compile-time proof that the server satisfies the engine's transport port.
var _ gateway.Gateway = (*Server)(nil)

// New validates the configuration. Nothing is bound until Start.
func New(cfg Config) (*Server, error) {
	if cfg.Addr == "" {
		return nil, errors.New("semtech: Config.Addr is required")
	}
	if cfg.Handler == nil {
		return nil, errors.New("semtech: Config.Handler is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.GatewayTimeout <= 0 {
		cfg.GatewayTimeout = 3 * time.Minute
	}
	if cfg.MaxConcurrentPackets <= 0 {
		cfg.MaxConcurrentPackets = 64
	}
	return &Server{
		cfg:     cfg,
		log:     cfg.Logger,
		now:     cfg.Clock,
		reg:     newRegistry(cfg.GatewayTimeout, cfg.Clock),
		sem:     make(chan struct{}, cfg.MaxConcurrentPackets),
		regions: map[string]region.Region{},
	}, nil
}

// Start binds the UDP socket and begins serving. It returns once the port is
// bound; the read loop runs in the background until Close.
func (s *Server) Start(ctx context.Context) error {
	addr, err := net.ResolveUDPAddr("udp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("semtech: resolve %s: %w", s.cfg.Addr, err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return fmt.Errorf("semtech: listen on %s: %w", s.cfg.Addr, err)
	}
	s.conn = conn
	s.wg.Add(1)
	go s.readLoop(ctx)
	s.log.Info("semtech gateway server listening",
		"addr", conn.LocalAddr().String(), "region", s.cfg.Region)
	return nil
}

// Close stops the read loop and waits for in-flight packets. Closing the socket
// is what unblocks ReadFromUDP; context cancellation alone cannot.
func (s *Server) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	var err error
	if s.conn != nil {
		err = s.conn.Close()
	}
	s.wg.Wait()
	return err
}

// ListenAddr is the bound address, which is a real port when the configuration
// asked for ":0" — the tests rely on that.
func (s *Server) ListenAddr() string {
	if s.conn == nil {
		return ""
	}
	return s.conn.LocalAddr().String()
}

// Gateways snapshots the registry for the dashboard and health endpoint.
func (s *Server) Gateways() []GatewayRecord { return s.reg.list() }

// GatewaysOnline is the count the dashboard shows.
func (s *Server) GatewaysOnline() int { return s.reg.onlineCount() }

// OnlineTimeout is how long a gateway may stay silent before it counts as
// offline, so status reporting and the registry cannot disagree.
func (s *Server) OnlineTimeout() time.Duration { return s.cfg.GatewayTimeout }
