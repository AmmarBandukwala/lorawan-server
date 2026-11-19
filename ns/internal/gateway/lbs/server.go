package lbs

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/fieldnode/lorawan-server/ns/internal/gateway"
	"github.com/fieldnode/lorawan-server/ns/internal/region"
)

// Config configures a Server.
type Config struct {
	// Addr is the TCP listen address, for example ":30001". Basic Station
	// defaults to port 30001 for an unencrypted LNS, so keeping that number
	// means a station needs no configuration beyond the server address.
	Addr string

	// Region is the band the gateway operates in. It is stamped on received
	// frames and used to build the router_config the station is asked to adopt.
	Region string

	// NetID is the network identifier announced in router_config.
	NetID [3]byte

	// JoinEUIs restricts which join servers the station will forward. Empty
	// means "forward every join-request", which is correct while join EUIs are
	// configured per device.
	JoinEUIs []string

	// Token, when set, must be presented by the station in the Authorization
	// header or the token query parameter. Empty disables the check, which is
	// only appropriate behind a proxy that authenticates.
	Token string

	// RadioPlanPath names a file holding an sx1301_conf object to forward to
	// stations. Empty means the station keeps its own radio plan, which is the
	// safe default: see the note at the top of routerconfig.go.
	RadioPlanPath string

	// AllowUnlimitedTransmit tells stations not to enforce duty cycle or dwell
	// time. It exists for test chambers and concentrated deployments where a
	// licensed operator holds the spectrum; it logs a warning when set.
	AllowUnlimitedTransmit bool

	Handler gateway.Handler
	Logger  *slog.Logger
	Clock   func() time.Time

	// GatewayTimeout decides "online" for a station. The station keeps the
	// connection open and sends timesync on its own schedule, so this is
	// generous by default.
	GatewayTimeout time.Duration

	// ReadTimeout is how long a connection may go without any message before
	// the server considers it dead. It must exceed the station's keepalive
	// interval or the server drops healthy gateways.
	ReadTimeout time.Duration

	// TxConfirmWindow is how long to wait for a station's dntxed before a
	// downlink is counted as unconfirmed. It should exceed the longest RX delay
	// plus transmission time in use; the default covers RX2 at 6 s with margin.
	TxConfirmWindow time.Duration
}

// Server accepts LoRa Basics Station connections and is the gateway.Gateway
// implementation the engine schedules against.
type Server struct {
	cfg     Config
	log     *slog.Logger
	now     func() time.Time
	handler gateway.Handler

	listener net.Listener
	server   *http.Server
	upgrader websocket.Upgrader

	mu       sync.RWMutex
	sessions map[eui]*session

	// regionMu guards the resolved band. It is resolved lazily because an
	// unknown region must not stop the listener from starting: a station that
	// connects gets a clear error and a closed socket, while the rest of the
	// server stays diagnosable.
	regionMu    sync.Mutex
	regionCache region.Region

	radioPlan json.RawMessage

	closed atomic.Bool
	wg     sync.WaitGroup
}

// Compile-time proof that the server satisfies the engine's transport port.
var _ gateway.Gateway = (*Server)(nil)

// session is one connected station.
//
// Writes are serialised through a channel because the WebSocket library permits
// only one concurrent writer per connection: two goroutines writing at once
// corrupts the frame stream in a way that surfaces as a station that
// intermittently stops receiving downlinks. Everything that wants to send
// therefore hands bytes to the writer goroutine instead of touching the socket.
type session struct {
	eui       eui
	conn      *websocket.Conn
	remote    string
	srv       *Server
	write     chan []byte
	lastSeen  atomic.Value // time.Time
	xtime     atomic.Value // xtime
	connected time.Time

	// diid numbers the downlinks sent on this connection. A station reports
	// what it did with each one in a dntxed message carrying the same diid,
	// which is the only way to tie a transmission confirmation to a schedule.
	diid atomic.Int64

	// rctx is the radio context of the most recent uplink. A dnmsg has to carry
	// the one belonging to the frame it answers, so a multi-RF-chain concentrator
	// picks the same chain for the downlink that heard the uplink. The 8-byte
	// transport token is already full (it holds the whole xtime), so this is the
	// only place it can live; see the limitation note in downstream.go.
	rctx atomic.Int64

	// Operator-facing counters. They live on the connection rather than on the
	// server because a station that reconnects starts a new concentrator session,
	// and mixing the two would make "downlinks this gateway has transmitted" mean
	// nothing.
	packets        atomic.Uint64
	downlinksSent  atomic.Uint64
	downlinkErrors atomic.Uint64
	lastTxError    atomic.Value // string
	info           atomic.Value // stationInfo

	// tx correlates dnmsg with dntxed.
	tx *txTracker

	// done is closed when the connection is torn down. The writer goroutine and
	// anything enqueueing a message watch it, because the write channel is never
	// closed: closing it would race with a concurrent enqueue, and a send on a
	// closed channel panics. Without this, every gateway that ever connected
	// leaves a writer goroutine parked on the channel for the life of the
	// process, which on a gateway that flaps every few minutes is a slow but
	// unbounded leak.
	done      chan struct{}
	closeOnce sync.Once
}

// New validates the configuration and loads any radio plan. Nothing is bound
// until Start.
func New(cfg Config) (*Server, error) {
	if cfg.Addr == "" {
		return nil, errors.New("lbs: Config.Addr is required")
	}
	if cfg.Handler == nil {
		return nil, errors.New("lbs: Config.Handler is required")
	}
	if cfg.Region == "" {
		return nil, errors.New("lbs: Config.Region is required; a station must be told which band it is in")
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
	if cfg.ReadTimeout <= 0 {
		cfg.ReadTimeout = 90 * time.Second
	}
	if cfg.TxConfirmWindow <= 0 {
		cfg.TxConfirmWindow = 15 * time.Second
	}

	s := &Server{
		cfg:      cfg,
		log:      cfg.Logger,
		now:      cfg.Clock,
		handler:  cfg.Handler,
		sessions: map[eui]*session{},
		upgrader: websocket.Upgrader{
			// A station sends no Origin header, so the origin check is off:
			// the default policy would reject every gateway. Subprotocols are
			// not used, so none are offered — offering one the station cannot
			// name makes some firmwares abort the handshake.
			HandshakeTimeout: 15 * time.Second,
			ReadBufferSize:   4096,
			WriteBufferSize:  4096,
			CheckOrigin:      func(*http.Request) bool { return true },
		},
	}

	if cfg.RadioPlanPath != "" {
		raw, err := os.ReadFile(cfg.RadioPlanPath)
		if err != nil {
			return nil, fmt.Errorf("lbs: read radio plan: %w", err)
		}
		// Validating here rather than at handshake time means a malformed plan
		// fails the deployment at startup instead of silently mis-configuring
		// every gateway that connects.
		if !json.Valid(raw) {
			return nil, fmt.Errorf("lbs: radio plan %s is not valid JSON", cfg.RadioPlanPath)
		}
		s.radioPlan = json.RawMessage(raw)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /discovery/v2/uri", s.handleDiscovery)
	mux.HandleFunc("/connectedtcp/", s.handleTraffic)
	mux.HandleFunc("/ws/", s.handleTraffic)
	s.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 15 * time.Second,
	}
	return s, nil
}

// Start binds the TCP port and begins serving.
func (s *Server) Start(_ context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("lbs: listen on %s: %w", s.cfg.Addr, err)
	}
	s.listener = ln
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := s.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("lbs listener stopped", "err", err)
		}
	}()
	if s.cfg.AllowUnlimitedTransmit {
		s.log.Warn("duty cycle and dwell time are disabled for stations on this server; " +
			"only appropriate where the spectrum is licensed or dedicated, such as a test chamber " +
			"(see NS_DISABLE_DUTY_CYCLE for the server-side equivalent)")
	}
	s.log.Info("basic station server listening",
		"addr", ln.Addr().String(), "region", s.cfg.Region,
		"radio_plan_pushed", s.radioPlan != nil, "auth", s.cfg.Token != "")
	return nil
}

// Close stops accepting connections and waits for in-flight handlers.
func (s *Server) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	s.mu.RLock()
	for _, sess := range s.sessions {
		sess.shutdown()
	}
	s.mu.RUnlock()

	err := s.server.Shutdown(ctx)
	s.wg.Wait()
	return err
}

// ListenAddr is the bound address, which is a real port when the configuration
// asked for ":0" — the tests rely on that.
func (s *Server) ListenAddr() string {
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// authenticate checks the presented credential, when one is required.
//
// Constant-time comparison matters here: the port is reachable by anyone who can
// route to it, and a timing side channel on a long-lived gateway token is a
// cheap way to recover it.
func (s *Server) authenticate(r *http.Request) bool {
	if s.cfg.Token == "" {
		return true
	}
	presented := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if presented == "" {
		presented = r.URL.Query().Get("token")
	}
	if presented == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(s.cfg.Token)) == 1
}

// shutdown closes a session's socket once, waking its read loop.
func (sess *session) shutdown() {
	sess.closeOnce.Do(func() {
		close(sess.done)
		_ = sess.conn.Close()
	})
}
