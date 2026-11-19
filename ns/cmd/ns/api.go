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

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
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
	log     *slog.Logger
	token   []byte
	http    *http.Server
}

// maxDownlinkBytes is the largest application payload accepted. LoRaWAN 1.0.x
// allows 243 bytes of FRMPayload at the highest data rates and far less at the
// low ones, so anything near this cap is a mistake; the engine would reject it
// per window anyway, and a client gets a clearer answer here.
const maxDownlinkBytes = 51

func newAPIServer(addr, token string, eng *engine.Engine, devices engine.DeviceRepo, log *slog.Logger) *apiServer {
	s := &apiServer{eng: eng, devices: devices, log: log, token: []byte(token)}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/devices/{dev_eui}/downlink", s.requireAuth(s.handleEnqueueDownlink))
	mux.HandleFunc("DELETE /api/v1/devices/{dev_eui}/downlink", s.requireAuth(s.handleCancelDownlink))
	mux.HandleFunc("POST /api/v1/devices/{dev_eui}/devstatus", s.requireAuth(s.handleDeviceStatus))
	mux.HandleFunc("GET /api/v1/devices/{dev_eui}/session", s.requireAuth(s.handleSession))

	// Provisioning. These sit behind the same token as the downlink endpoints:
	// being able to install keys is strictly more dangerous than being able to
	// send a frame with keys that are already installed.
	mux.HandleFunc("GET /api/v1/devices", s.requireAuth(s.handleListDevices))
	mux.HandleFunc("POST /api/v1/devices", s.requireAuth(s.handleCreateDevice))
	mux.HandleFunc("GET /api/v1/devices/{dev_eui}", s.requireAuth(s.handleGetDevice))
	mux.HandleFunc("PUT /api/v1/devices/{dev_eui}", s.requireAuth(s.handleUpdateDevice))
	mux.HandleFunc("DELETE /api/v1/devices/{dev_eui}", s.requireAuth(s.handleDeleteDevice))

	s.http = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return s
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
	writeJSON(w, http.StatusAccepted, map[string]any{
		"dev_eui":     devEUI.String(),
		"queued":      true,
		"f_port":      *req.FPort,
		"confirmed":   confirmed,
		"explanation": "the frame goes out in the device's next RX window",
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
	writeJSON(w, http.StatusOK, map[string]any{"dev_eui": devEUI.String(), "cancelled": true})
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
	writeJSON(w, http.StatusAccepted, map[string]any{
		"dev_eui": devEUI.String(), "scheduled": true,
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
	ses, err := s.eng.Session(r.Context(), devEUI)
	if err != nil {
		s.writeEngineError(w, devEUI, err)
		return
	}
	view := map[string]any{
		"dev_eui":          devEUI.String(),
		"dev_addr":         ses.DevAddr.String(),
		"region":           ses.Region,
		"f_cnt_up":         ses.FCntUp,
		"f_cnt_down":       ses.FCntDown,
		"data_rate":        ses.DataRate,
		"tx_power_index":   ses.TxPower,
		"nb_trans":         ses.NbTrans,
		"adr":              ses.LastADRBit,
		"rx1_delay":        ses.Rx1Delay,
		"rx1_dr_offset":    ses.Rx1DROffset,
		"rx2_frequency_hz": ses.Rx2FrequencyHz,
		"rx2_data_rate":    ses.Rx2DataRate,
		"duty_cycle":       ses.DutyCycle,
		"last_rssi":        ses.LastRSSI,
		"last_snr":         ses.LastSNR,
	}
	if !ses.LastUplinkAt.IsZero() {
		view["last_uplink_at"] = ses.LastUplinkAt
	}
	if ses.HasDeviceStatus {
		view["battery"] = ses.Battery
		view["margin_db"] = ses.Margin
	}
	if ses.Pending != nil {
		pending := map[string]any{"confirmed": ses.Pending.Confirmed}
		if ses.Pending.FPort != nil {
			pending["f_port"] = int(*ses.Pending.FPort)
		}
		if len(ses.Pending.FRMPayload) > 0 {
			pending["data"] = hex.EncodeToString(ses.Pending.FRMPayload)
		}
		if n := len(ses.Pending.MACCommands); n > 0 {
			pending["mac_commands"] = n
		}
		view["pending_downlink"] = pending
	}
	writeJSON(w, http.StatusOK, view)
}
