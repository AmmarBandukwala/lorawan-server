package main

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/codec"
	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/pgstore"
)

// apiServer is the application-facing HTTP API: submit a downlink, ask for a
// device-status report, read a session.
//
// It is separate from the status endpoint because the two carry different
// guarantees. /status is read-only and may live on loopback; this API can make a
// device transmit radio frames, so it requires a bearer token and the process
// refuses to start with a weak one.
//
// Everything here is asynchronous by design. A class-A device listens for two
// seconds after it speaks and then goes quiet, so "send this now" has no
// meaning: a downlink is queued and leaves in the next window the device opens.
// Callers get a 202 and learn the outcome from the device's next uplink or from
// a webhook, never from this response.
type apiServer struct {
	eng     *engine.Engine
	devices engine.DeviceRepo
	// models and traffic back the device-model, decoded-uplink and event-history
	// endpoints. Either may be nil on a store that cannot serve it — the in-memory
	// store has no model repository and no event table — and then those endpoints
	// answer 503 rather than pretending to work.
	models  engine.ModelRepo
	traffic TrafficReader
	// integrations backs the integration CRUD endpoints, and reloadIntegrations
	// applies a saved change to the live sinks. Both are nil on the in-memory
	// store, which has nowhere to keep a destination, and then those endpoints
	// answer 503 rather than accepting a write that would be lost on restart.
	integrations       IntegrationStore
	reloadIntegrations Integrations
	// environmentSinkNames are destinations from NS_WEBHOOK_URLS. They are
	// reported to the panel as read-only so it can account for every destination
	// that is actually delivering.
	environmentSinkNames []string

	// invalidate tells the live payload decoder that a model changed, so an edit
	// takes effect on the next frame instead of after a restart.
	invalidate    func(modelID string)
	codecOpts     codec.Options
	decodeTimeout time.Duration

	log   *slog.Logger
	token []byte
	http  *http.Server
}

// TrafficReader is the read side of stored traffic. pgstore implements it; the
// in-memory store keeps its records in engine shapes and is served elsewhere.
type TrafficReader interface {
	RecentUplinks(ctx context.Context, devEUI string, limit int) ([]pgstore.UplinkRow, error)
	RecentEvents(ctx context.Context, devEUI, kind string, limit int) ([]pgstore.EventRow, error)
}

// maxDownlinkBytes is the largest application payload accepted. LoRaWAN 1.0.x
// allows 243 bytes of FRMPayload at the highest data rates and far less at the
// low ones, so anything near this cap is a mistake; the engine would reject it
// per window anyway, and a client gets a clearer answer here.
const maxDownlinkBytes = 51

func newAPIServer(addr, token string, eng *engine.Engine, devices engine.DeviceRepo, log *slog.Logger) *apiServer {
	server := &apiServer{eng: eng, devices: devices, log: log, token: []byte(token)}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/devices/{dev_eui}/downlink", server.requireAuth(server.handleEnqueueDownlink))
	mux.HandleFunc("DELETE /api/v1/devices/{dev_eui}/downlink", server.requireAuth(server.handleCancelDownlink))
	mux.HandleFunc("POST /api/v1/devices/{dev_eui}/devstatus", server.requireAuth(server.handleDeviceStatus))
	mux.HandleFunc("GET /api/v1/devices/{dev_eui}/session", server.requireAuth(server.handleSession))

	// Provisioning. These sit behind the same token as the downlink endpoints:
	// being able to install keys is strictly more dangerous than being able to
	// send a frame with keys that are already installed.
	mux.HandleFunc("GET /api/v1/devices", server.requireAuth(server.handleListDevices))
	mux.HandleFunc("POST /api/v1/devices", server.requireAuth(server.handleCreateDevice))
	mux.HandleFunc("GET /api/v1/devices/{dev_eui}", server.requireAuth(server.handleGetDevice))
	mux.HandleFunc("PUT /api/v1/devices/{dev_eui}", server.requireAuth(server.handleUpdateDevice))
	mux.HandleFunc("PATCH /api/v1/devices/{dev_eui}", server.requireAuth(server.handlePatchDevice))
	mux.HandleFunc("DELETE /api/v1/devices/{dev_eui}", server.requireAuth(server.handleDeleteDevice))

	// Device models and decoded traffic. A model carries vendor-supplied
	// JavaScript, so these sit behind the same token as provisioning: being able
	// to install a formatter is being able to run code on this server.
	mux.HandleFunc("GET /api/v1/models", server.requireAuth(server.handleListModels))
	mux.HandleFunc("GET /api/v1/models/{model_id}", server.requireAuth(server.handleGetModel))
	mux.HandleFunc("POST /api/v1/models/{model_id}", server.requireAuth(server.handleCreateModel))
	mux.HandleFunc("PUT /api/v1/models/{model_id}", server.requireAuth(server.handleUpdateModel))
	mux.HandleFunc("DELETE /api/v1/models/{model_id}", server.requireAuth(server.handleDeleteModel))
	mux.HandleFunc("POST /api/v1/models/{model_id}/decode", server.requireAuth(server.handleTestModelDecode))
	mux.HandleFunc("GET /api/v1/uplinks", server.requireAuth(server.handleListUplinks))

	// Event history: what the network heard and what it refused. Read-only, but
	// per-device, which is exactly what the unauthenticated status endpoint
	// declines to carry — so it is here, behind the token, with everything else.
	mux.HandleFunc("GET /api/v1/events", server.requireAuth(server.handleListEvents))

	// Integration destinations. Writes reload the live sinks before responding,
	// so a 2xx means events will go there, not merely that a row was written.
	mux.HandleFunc("GET /api/v1/integrations", server.requireAuth(server.handleListIntegrations))
	mux.HandleFunc("POST /api/v1/integrations", server.requireAuth(server.handleCreateIntegration))
	mux.HandleFunc("GET /api/v1/integrations/{id}", server.requireAuth(server.handleGetIntegration))
	mux.HandleFunc("PUT /api/v1/integrations/{id}", server.requireAuth(server.handleUpdateIntegration))
	mux.HandleFunc("DELETE /api/v1/integrations/{id}", server.requireAuth(server.handleDeleteIntegration))
	mux.HandleFunc("POST /api/v1/integrations/{id}/test", server.requireAuth(server.handleTestIntegration))

	server.http = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return server
}

// Start binds the port and serves in the background. Binding synchronously means
// a port clash fails the process at startup rather than silently leaving it
// without an API.
func (s *apiServer) Start() error {
	ln, err := net.Listen("tcp", s.http.Addr)
	if err != nil {
		return fmt.Errorf("listen for API requests on %s: %w", s.http.Addr, err)
	}
	go func() {
		if err := s.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("API server stopped", "err", err)
		}
	}()
	return nil
}

func (s *apiServer) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }

// requireAuth checks the bearer token. subtle.ConstantTimeCompare returns 0 for
// differing lengths without branching early, so there is no length leak worth
// defending against here.
func (s *apiServer) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		got := r.Header.Get("Authorization")
		if !strings.HasPrefix(got, prefix) {
			unauthorized(w, "an Authorization: Bearer <token> header is required")
			return
		}
		if subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(got, prefix)), s.token) != 1 {
			unauthorized(w, "invalid token")
			return
		}
		next(w, r)
	}
}

// downlinkRequest is the body of POST /api/v1/devices/{dev_eui}/downlink.
type downlinkRequest struct {
	// Data is the application payload, hex encoded. Hex rather than base64
	// because these payloads get copied out of datasheets and serial traces,
	// which are written in hex.
	Data string `json:"data"`
	// FPort selects the application port. A pointer so "absent" is
	// distinguishable from 0; 0 is refused anyway, since it means an MAC-only
	// frame that no application wants to write by hand.
	FPort *int  `json:"f_port"`
	Ack   *bool `json:"confirmed,omitempty"`
}

func (s *apiServer) handleEnqueueDownlink(w http.ResponseWriter, r *http.Request) {
	devEUI, ok := pathDevEUI(w, r)
	if !ok {
		return
	}
	var req downlinkRequest
	// 4 KiB is generous for a 51-byte payload in hex; the cap is what stops a
	// client from making this process allocate for a request that will be refused.
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil {
		badRequest(w, "the body must be a JSON object: "+err.Error())
		return
	}
	if req.FPort == nil {
		badRequest(w, "f_port is required (1..223)")
		return
	}
	if *req.FPort < 1 || *req.FPort > 223 {
		badRequest(w, fmt.Sprintf("f_port %d is outside 1..223", *req.FPort))
		return
	}
	payload, err := hex.DecodeString(req.Data)
	if err != nil {
		badRequest(w, "data must be a hex string")
		return
	}
	if len(payload) == 0 {
		badRequest(w, "data must not be empty")
		return
	}
	if len(payload) > maxDownlinkBytes {
		badRequest(w, fmt.Sprintf("data is %d bytes; the limit is %d", len(payload), maxDownlinkBytes))
		return
	}

	confirmed := req.Ack != nil && *req.Ack
	if err := s.eng.EnqueueDownlink(r.Context(), devEUI, byte(*req.FPort), payload, confirmed); err != nil {
		s.writeEngineError(w, devEUI, err)
		return
	}
	s.log.Info("downlink queued", "dev_eui", devEUI, "f_port", *req.FPort,
		"bytes", len(payload), "confirmed", confirmed)

	// 202, not 200: the frame is accepted for the next window, not transmitted.
	writeJSON(w, http.StatusAccepted, downlinkQueuedResponse{
		DevEUI:      devEUI.String(),
		Queued:      true,
		FPort:       int(*req.FPort),
		Confirmed:   confirmed,
		Explanation: queuedExplanation,
	})
}

func (s *apiServer) handleCancelDownlink(w http.ResponseWriter, r *http.Request) {
	devEUI, ok := pathDevEUI(w, r)
	if !ok {
		return
	}
	if err := s.eng.CancelPendingDownlink(r.Context(), devEUI); err != nil {
		s.writeEngineError(w, devEUI, err)
		return
	}
	writeJSON(w, http.StatusOK, downlinkCancelledResponse{
		DevEUI:    devEUI.String(),
		Cancelled: true,
	})
}

func (s *apiServer) handleDeviceStatus(w http.ResponseWriter, r *http.Request) {
	devEUI, ok := pathDevEUI(w, r)
	if !ok {
		return
	}
	if err := s.eng.RequestDeviceStatus(r.Context(), devEUI); err != nil {
		s.writeEngineError(w, devEUI, err)
		return
	}
	writeJSON(w, http.StatusAccepted, deviceStatusScheduledResponse{
		DevEUI:    devEUI.String(),
		Scheduled: true,
	})
}

// handleSession reports what the network currently believes about a device's
// radio parameters. It is read-only and deliberately excludes the session keys:
// an operator debugging a link needs data rates and counters, and a reader of
// this endpoint is not automatically trusted with the keys that authenticate the
// device.
func (s *apiServer) handleSession(w http.ResponseWriter, r *http.Request) {
	devEUI, ok := pathDevEUI(w, r)
	if !ok {
		return
	}
	session, err := s.eng.Session(r.Context(), devEUI)
	if err != nil {
		s.writeEngineError(w, devEUI, err)
		return
	}
	view := sessionResponse{
		DevEUI:         devEUI.String(),
		DevAddr:        session.DevAddr.String(),
		Region:         session.Region,
		FCntUp:         session.FCntUp,
		FCntDown:       session.FCntDown,
		DataRate:       session.DataRate,
		TxPowerIndex:   session.TxPower,
		NbTrans:        session.NbTrans,
		ADR:            session.LastADRBit,
		Rx1Delay:       session.Rx1Delay,
		Rx1DROffset:    session.Rx1DROffset,
		Rx2FrequencyHz: session.Rx2FrequencyHz,
		Rx2DataRate:    session.Rx2DataRate,
		DutyCycle:      session.DutyCycle,
		LastRSSI:       session.LastRSSI,
		LastSNR:        session.LastSNR,
	}
	if !session.LastUplinkAt.IsZero() {
		at := session.LastUplinkAt
		view.LastUplinkAt = &at
	}
	if session.HasDeviceStatus {
		battery, margin := int(session.Battery), int(session.Margin)
		view.Battery, view.MarginDB = &battery, &margin
	}
	if session.Pending != nil {
		pending := pendingDownlinkResponse{Confirmed: session.Pending.Confirmed}
		if session.Pending.FPort != nil {
			fPort := int(*session.Pending.FPort)
			pending.FPort = &fPort
		}
		if len(session.Pending.FRMPayload) > 0 {
			pending.Data = hex.EncodeToString(session.Pending.FRMPayload)
		}
		pending.MACCommands = len(session.Pending.MACCommands)
		view.Pending = &pending
	}
	writeJSON(w, http.StatusOK, view)
}
