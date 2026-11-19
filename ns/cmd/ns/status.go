package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/gateway/semtech"
	"github.com/fieldnode/lorawan-server/ns/internal/integration"
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
	gatews  *semtech.Server
	eng     *engine.Engine
	integr  *integration.Dispatcher // nil when no endpoints are configured
	started time.Time
	http    *http.Server
}

func newStatusServer(addr string, log *slog.Logger, srv *semtech.Server, eng *engine.Engine,
	integr *integration.Dispatcher) *statusServer {

	s := &statusServer{
		addr: addr, log: log, gatews: srv, eng: eng, integr: integr, started: time.Now(),
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
	EUI             string    `json:"gateway_eui"`
	Address         string    `json:"address"`
	ProtocolVersion int       `json:"protocol_version"`
	Online          bool      `json:"online"`
	LastSeen        time.Time `json:"last_seen"`
	Packets         uint64    `json:"packets"`
	DownlinksSent   uint64    `json:"downlinks_sent"`
	DownlinkErrors  uint64    `json:"downlink_errors"`
	LastTxAckError  string    `json:"last_tx_ack_error,omitempty"`
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
	onlineFor := s.gatews.OnlineTimeout()
	gateways := s.gatews.Gateways()
	views := make([]gatewayView, 0, len(gateways))
	for _, g := range gateways {
		var statsAt *time.Time
		if !g.StatsAt.IsZero() {
			at := g.StatsAt
			statsAt = &at
		}
		views = append(views, gatewayView{
			EUI:             g.EUI.String(),
			Address:         g.Addr,
			ProtocolVersion: int(g.ProtocolVersion),
			Online:          g.Online(now, onlineFor),
			LastSeen:        g.LastSeen,
			Packets:         g.Packets,
			DownlinksSent:   g.DownlinksSent,
			DownlinkErrors:  g.DownlinkErrors,
			LastTxAckError:  g.LastTxAckError,
			StatsAt:         statsAt,
			RxReceived:      g.LastStats.RxReceived,
			RxOk:            g.LastStats.RxOK,
			TxReceived:      g.LastStats.TxReceived,
			Latitude:        g.LastStats.Latitude,
			Longitude:       g.LastStats.Longitude,
			Altitude:        g.LastStats.Altitude,
		})
	}

	cfg := s.eng.Config()
	body := statusView{
		UptimeSeconds:  now.Sub(s.started).Seconds(),
		Region:         cfg.Region,
		NetID:          cfg.NetID.String(),
		DownlinkPower:  int(cfg.DownlinkPowerdBm),
		GatewaysTotal:  len(views),
		GatewaysOnline: s.gatews.GatewaysOnline(),
		Gateways:       views,
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
