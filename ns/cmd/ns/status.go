package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/gateway"
	"github.com/fieldnode/lorawan-server/ns/internal/integration"
	"github.com/fieldnode/lorawan-server/ns/internal/region"
)

// statusServer is the operator-facing HTTP surface of the network server: a
// liveness probe for the container orchestrator and a JSON snapshot of gateway
// presence and integration health.
//
// It is read-only and stays that way. Application traffic — downlink submission,
// session reads — belongs to apiServer, which authenticates; this endpoint does
// not, so it must never gain an endpoint that changes anything. Keep it on an
// internal network or loopback.
type statusServer struct {
	addr    string
	log     *slog.Logger
	routers *gateway.Router
	eng     *engine.Engine
	integr  *integration.Dispatcher // nil when no endpoints are configured
	// listeners describes the gateway-facing endpoints, built from the process
	// configuration at startup. It is passed in rather than read from the router
	// because the router knows which transports exist and not what they bound.
	listeners []listenerView
	started   time.Time
	http      *http.Server
}

func newStatusServer(addr string, log *slog.Logger, routers *gateway.Router, eng *engine.Engine,
	integr *integration.Dispatcher, listeners []listenerView) *statusServer {

	s := &statusServer{
		addr: addr, log: log, routers: routers, eng: eng, integr: integr,
		listeners: listeners, started: time.Now(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /status", s.handleStatus)

	s.http = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		// A status poll is one request; a slow client must not hold a
		// goroutine and a connection open indefinitely.
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	return s
}

// Start binds the port and serves in the background. Binding synchronously is
// the point: a port clash must fail the process at startup, not silently leave
// it without a status endpoint.
func (s *statusServer) Start() error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("listen for status requests on %s: %w", s.addr, err)
	}
	go func() {
		if err := s.http.Serve(ln); err != nil && err != http.ErrServerClosed {
			s.log.Error("status server stopped", "err", err)
		}
	}()
	return nil
}

func (s *statusServer) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}

type gatewayView struct {
	EUI       string `json:"gateway_eui"`
	Transport string `json:"transport"`
	Protocol  string `json:"protocol"`
	Address   string `json:"address"`
	// Firmware and Model are filled only by a Basics Station, which reports them
	// during its handshake. An operator comparing two gateways that behave
	// differently looks at these first.
	Firmware string    `json:"firmware,omitempty"`
	Model    string    `json:"model,omitempty"`
	Online   bool      `json:"online"`
	LastSeen time.Time `json:"last_seen"`
	Packets  uint64    `json:"packets"`

	DownlinksSent       uint64 `json:"downlinks_sent"`
	DownlinkErrors      uint64 `json:"downlink_errors"`
	DownlinkConfirmed   uint64 `json:"downlinks_confirmed"`
	DownlinkUnconfirmed uint64 `json:"downlinks_unconfirmed"`
	LastTxAckError      string `json:"last_tx_ack_error,omitempty"`
	// StatsAt is a pointer because encoding/json treats a zero time.Time as a
	// value, so omitempty alone would publish "0001-01-01T00:00:00Z" for a
	// gateway that has not reported stats yet.
	StatsAt    *time.Time `json:"stats_at,omitempty"`
	RxReceived uint32     `json:"rx_received"`
	RxOk       uint32     `json:"rx_ok"`
	TxReceived uint32     `json:"tx_received"`
	Latitude   float64    `json:"latitude,omitempty"`
	Longitude  float64    `json:"longitude,omitempty"`
	Altitude   int        `json:"altitude,omitempty"`
}

type statusView struct {
	UptimeSeconds  float64       `json:"uptime_seconds"`
	Region         string        `json:"region"`
	NetID          string        `json:"net_id"`
	DownlinkPower  int           `json:"downlink_power_dbm"`
	GatewaysTotal  int           `json:"gateways_total"`
	GatewaysOnline int           `json:"gateways_online"`
	Gateways       []gatewayView `json:"gateways"`
	// SupportedRegions is every band this build implements, in the order a
	// console should offer them.
	//
	// It is published rather than duplicated downstream because a list kept in
	// the console and another kept here drift: the console offered three bands
	// while this server accepted six, and an operator could not record the band
	// they were actually running. Anything added to region.Names() becomes
	// configurable in the console with no further change.
	SupportedRegions []string `json:"supported_regions"`
	// Transports lists the gateway protocols this process is listening for. An
	// operator adding a Basics Station gateway checks this first: if
	// "basic-station" is absent, the port is simply not open, which is a
	// configuration answer rather than a gateway fault.
	Transports []string `json:"transports"`
	// Listeners gives the address and port of each enabled transport, which is
	// what a console needs to tell someone what to type into the gateway.
	Listeners []listenerView `json:"listeners,omitempty"`
	// Integrations reports the webhook dispatcher's counters, or nil when no
	// endpoints are configured. A dropped count that keeps growing is the signal
	// that a receiver is too slow, and this is where an operator sees it.
	Integrations *integrationView `json:"integrations,omitempty"`
	// DutyCycle reports channel occupancy per gateway and sub-band, and is
	// omitted when enforcement is disabled. A sub-band sitting at its budget is
	// the explanation for downlinks that simply do not appear, and without this
	// field the only way to find it is to read server logs.
	DutyCycle *dutyCycleView `json:"duty_cycle,omitempty"`
}

type dutyCycleView struct {
	Window  string                `json:"window"`
	Usage   []engine.SubBandUsage `json:"usage"`
	Denials map[string]uint64     `json:"denials"`
}

// listenerView describes one gateway-facing endpoint this process is listening
// on, and it exists for one reason: the console has to tell an operator what to
// type into the gateway.
//
// A gateway is not added from this console — it announces itself, and inventing a
// "register gateway" form would produce a record of a gateway that cannot reach
// the server while the real one keeps failing to connect. What actually unblocks
// someone standing at a gateway with a serial console is the endpoint and the
// transport it speaks, so both are published rather than left to be guessed from
// a log line.
//
// The JSON shape is the contract; the type itself stays unexported because this
// is package main and nothing imports it. Only this process knows which
// transports it opened — NS_LBS_ADDR=off removes one — and a console that
// assumed both would send an operator to configure a station against a port
// nobody is listening on.
type listenerView struct {
	// Transport is "semtech-udp" or "basic-station", matching the transport field
	// on each gateway so an operator can match a discovered gateway back to the
	// endpoint it used.
	Transport string `json:"transport"`
	// BindAddr is the address as configured, for example ":6858".
	BindAddr string `json:"bind_addr"`
	// Port is the numeric port, which is the one field a gateway configuration
	// form always asks for.
	Port int `json:"port"`
	// HostWildcard is true when the bind address carries no host part, meaning
	// the server accepts connections on any of its interfaces. The console then
	// has to supply the host itself — the gateway needs an address the operator
	// knows is reachable, and a wildcard says nothing about which that is.
	HostWildcard bool `json:"host_wildcard"`
	// Secured reports whether this endpoint authenticates the gateway. The UDP
	// transport does not (the protocol has no credential to carry), which the
	// console should say out loud rather than let someone assume.
	Secured bool `json:"secured"`
}

// newListenerView builds the view for one endpoint. secured says whether the
// endpoint authenticates the gateway, which differs per transport and is decided
// by the caller because only it knows whether a token was configured.
func newListenerView(transport, bindAddr string, secured bool) listenerView {
	host, portStr, err := net.SplitHostPort(bindAddr)
	if err != nil {
		// Not in host:port form — an operator may have written just a port.
		// Report it as a wildcard host with an unknown port rather than dropping
		// the endpoint from the view, since "the server is listening somewhere"
		// is still worth knowing.
		return listenerView{Transport: transport, BindAddr: bindAddr, HostWildcard: true}
	}
	port, _ := strconv.Atoi(portStr)
	return listenerView{
		Transport:    transport,
		BindAddr:     bindAddr,
		Port:         port,
		HostWildcard: host == "" || host == "0.0.0.0" || host == "::" || host == "*",
		Secured:      secured,
	}
}

type integrationView struct {
	Sinks     []string `json:"sinks"`
	Published int64    `json:"published"`
	Failed    int64    `json:"delivery_failures"`
	Dropped   int64    `json:"dropped"`
	Queued    int64    `json:"queued"`
}

// handleStatus reports timestamps, never relative strings: a cached or proxied
// response must stay interpretable, and the UI owns localisation.
func (s *statusServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	// Each transport decides what "online" means, because the protocols give
	// different evidence: a forwarder is judged by the gap between its PULL_DATA
	// packets, a station by whether it still holds the connection.
	timeouts := s.routers.OnlineTimeouts()
	presence := s.routers.Presence()
	views := make([]gatewayView, 0, len(presence))
	online := 0
	for _, g := range presence {
		// A transport that reports Connected has already applied its own
		// threshold; the timeout is the fallback for transports that only track
		// a last-heard timestamp.
		isOnline := g.Connected
		if timeout, ok := timeouts[g.Transport]; ok && timeout > 0 {
			isOnline = !g.LastSeen.IsZero() && now.Sub(g.LastSeen) <= timeout
		}
		if isOnline {
			online++
		}

		var statsAt *time.Time
		if !g.StatsAt.IsZero() {
			at := g.StatsAt
			statsAt = &at
		}
		views = append(views, gatewayView{
			EUI:                 g.EUI.String(),
			Transport:           g.Transport,
			Protocol:            g.Protocol,
			Address:             g.Address,
			Firmware:            g.Firmware,
			Model:               g.Model,
			Online:              isOnline,
			LastSeen:            g.LastSeen,
			Packets:             g.Packets,
			DownlinksSent:       g.DownlinksSent,
			DownlinkErrors:      g.DownlinkErrors,
			DownlinkConfirmed:   g.DownlinkConfirm,
			DownlinkUnconfirmed: g.DownlinkUnconfirmed,
			LastTxAckError:      g.LastTxError,
			StatsAt:             statsAt,
			RxReceived:          g.RxReceived,
			RxOk:                g.RxOk,
			TxReceived:          g.TxReceived,
			Latitude:            g.Latitude,
			Longitude:           g.Longitude,
			Altitude:            g.Altitude,
		})
	}

	cfg := s.eng.Config()
	body := statusView{
		UptimeSeconds:    now.Sub(s.started).Seconds(),
		Region:           cfg.Region,
		NetID:            cfg.NetID.String(),
		DownlinkPower:    int(cfg.DownlinkPowerdBm),
		GatewaysTotal:    len(views),
		GatewaysOnline:   online,
		Gateways:         views,
		Transports:       s.routers.Names(),
		Listeners:        s.listeners,
		SupportedRegions: region.Names(),
	}
	if s.integr != nil {
		st := s.integr.Stats()
		body.Integrations = &integrationView{
			Sinks:     s.integr.Sinks(),
			Published: st.Published,
			Failed:    st.Failed,
			Dropped:   st.Dropped,
			Queued:    st.Queued,
		}
	}
	if !cfg.DisableDutyCycleEnforcement {
		body.DutyCycle = &dutyCycleView{
			Window:  cfg.DutyCycleWindow.String(),
			Usage:   s.eng.DutyCycleUsage(),
			Denials: s.eng.DutyCycleDenials(),
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		s.log.Error("encode the status response", "err", err)
	}
}
