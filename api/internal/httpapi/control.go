package httpapi

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fieldnode/lorawan-server/api/internal/nsclient"
)

// The wire value that means "no device-specific limit" for max_data_rate,
// min_data_rate and max_tx_power.
//
// The network server calls the same value engine.UseRegionDefault. The two
// modules share no package and no test can import both, so nothing can assert
// they are equal. What can be done is the next best thing, and it is done in two
// places: this package pins the literal in TestRegionDefaultMatchesTheNetworkServer,
// and ns/internal/engine pins the same literal against its own constant. Change
// one and its own suite fails with a message naming the other, which is the
// strongest cross-module guarantee available without merging the modules.
const regionDefault = -1

// Controller sends commands to the network server and reads the state it holds
// in memory rather than in a table.
//
// It is a third port beside Repository and Provisioner because it is the only one
// that makes a device radiate. Reads come from the database, provisioning writes
// configuration, and this asks the radio to do something — a distinction worth
// keeping in the type system, since it is also the distinction between "the
// dashboard is slow" and "a device is about to transmit".
//
// A nil Controller is allowed and means the console has no network server
// configured; those endpoints then answer 503 rather than reporting success.
type Controller interface {
	EnqueueDownlink(ctx context.Context, devEUI string, fPort int, dataHex string, confirmed bool) (json.RawMessage, error)
	CancelDownlink(ctx context.Context, devEUI string) (json.RawMessage, error)
	RequestDeviceStatus(ctx context.Context, devEUI string) (json.RawMessage, error)
	Session(ctx context.Context, devEUI string) (json.RawMessage, error)
	Device(ctx context.Context, devEUI string) (json.RawMessage, error)
	Status(ctx context.Context) (json.RawMessage, error)
	PatchDevice(ctx context.Context, devEUI string, p nsclient.DevicePatch) (json.RawMessage, error)
	DeleteDevice(ctx context.Context, devEUI string) (json.RawMessage, error)
}

// maxDownlinkBytes mirrors the network server's own limit. Enforcing it here too
// turns "the console accepted 4 KB and the network server refused it" into a
// rejection that names the field before a round trip.
const maxDownlinkBytes = 51

// controlTimeout bounds a control call. It is shorter than the provisioning
// timeout on purpose: a downlink that cannot be queued in a few seconds is not
// going to be queued, and an operator who pressed send wants to know now.
const controlTimeout = 5 * time.Second

type enqueueDownlinkRequest struct {
	// Data is the payload as hex, matching the network server so an operator can
	// paste the same string into both. Hex rather than base64 because these
	// values come out of datasheets and serial traces written in hex.
	Data string `json:"data"`
	// FPort is the application port. Absent means 10, the common default; 0 is
	// refused because it means "MAC only", which is not what a console sends.
	FPort     *int `json:"f_port"`
	Confirmed bool `json:"confirmed"`
}

func (s *Server) enqueueDownlink(w http.ResponseWriter, r *http.Request) {
	devEUI, ok := pathDevEUI(w, r)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBytes)
	var input enqueueDownlinkRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidJSON)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, ErrInvalidJSON)
		return
	}

	fPort := 10
	if input.FPort != nil {
		fPort = *input.FPort
	}
	if fPort < 1 || fPort > 223 {
		// 0 is reserved for MAC-only frames and above 223 is reserved; a console
		// has no business choosing either.
		writeError(w, http.StatusBadRequest, ErrFPortOutOfRange)
		return
	}

	data := normalizeHex(input.Data)
	if data == "" {
		writeError(w, http.StatusBadRequest, ErrPayloadRequired)
		return
	}
	raw, err := hex.DecodeString(data)
	if err != nil {
		writeError(w, http.StatusBadRequest, ErrPayloadHex)
		return
	}
	if len(raw) > maxDownlinkBytes {
		// Its own code rather than sharing the hex one: "payload_must_be_hex"
		// would send an operator to check characters that are fine.
		writeError(w, http.StatusBadRequest, ErrPayloadTooLong)
		return
	}

	if s.controller == nil {
		writeError(w, http.StatusServiceUnavailable, ErrControllerNotConfigured)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), controlTimeout)
	defer cancel()

	resp, err := s.controller.EnqueueDownlink(ctx, devEUI, fPort, strings.ToUpper(data), input.Confirmed)
	if err != nil {
		writeUpstreamError(w, "enqueue downlink", devEUI, err, ErrDownlinkQueueFailed)
		return
	}
	// 202, not 200: the frame is queued, not sent. A class-A device listens for
	// two seconds after it speaks, so nothing has happened on the air yet.
	writeRaw(w, http.StatusAccepted, resp)
}

func (s *Server) cancelDownlink(w http.ResponseWriter, r *http.Request) {
	devEUI, ok := pathDevEUI(w, r)
	if !ok {
		return
	}
	if s.controller == nil {
		writeError(w, http.StatusServiceUnavailable, ErrControllerNotConfigured)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), controlTimeout)
	defer cancel()

	resp, err := s.controller.CancelDownlink(ctx, devEUI)
	if err != nil {
		writeUpstreamError(w, "cancel downlink", devEUI, err, ErrDownlinkQueueFailed)
		return
	}
	writeRaw(w, http.StatusOK, resp)
}

// requestDeviceStatus schedules a DevStatusReq.
//
// The dashboard's battery column depends on this: a device reports its battery
// and link margin only when asked, so without a way to ask, the column would be
// permanently empty for every device that does not volunteer one.
func (s *Server) requestDeviceStatus(w http.ResponseWriter, r *http.Request) {
	devEUI, ok := pathDevEUI(w, r)
	if !ok {
		return
	}
	if s.controller == nil {
		writeError(w, http.StatusServiceUnavailable, ErrControllerNotConfigured)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), controlTimeout)
	defer cancel()

	resp, err := s.controller.RequestDeviceStatus(ctx, devEUI)
	if err != nil {
		writeUpstreamError(w, "schedule device status", devEUI, err, ErrDeviceStatusFailed)
		return
	}
	writeRaw(w, http.StatusAccepted, resp)
}

// getSession returns the network's view of one device's radio parameters.
//
// This is the one read that deliberately does not come from the database. The
// session the engine is running is in its memory; the row is what it last
// persisted, and after a restart or during a burst the two differ. A panel that
// claims "what the device is doing now" has to ask the thing that knows.
func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	devEUI, ok := pathDevEUI(w, r)
	if !ok {
		return
	}
	if s.controller == nil {
		writeError(w, http.StatusServiceUnavailable, ErrControllerNotConfigured)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), controlTimeout)
	defer cancel()

	resp, err := s.controller.Session(ctx, devEUI)
	if err != nil {
		writeUpstreamError(w, "read session", devEUI, err, ErrSessionQueryFailed)
		return
	}
	writeRaw(w, http.StatusOK, resp)
}

// getStatus relays the network server's health: gateway presence, duty-cycle
// occupancy and integration counters.
//
// Relay rather than re-derive: "online" depends on when a gateway last sent
// PULL_DATA, and the drop counters are in memory. Reading ns_gateways would
// produce a confident answer to a different question.
func (s *Server) getStatus(w http.ResponseWriter, r *http.Request) {
	if s.controller == nil {
		writeError(w, http.StatusServiceUnavailable, ErrControllerNotConfigured)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), controlTimeout)
	defer cancel()

	resp, err := s.controller.Status(ctx)
	if err != nil {
		writeUpstreamError(w, "read status", "", err, ErrStatusQueryFailed)
		return
	}
	writeRaw(w, http.StatusOK, resp)
}

// getDevice relays the network server's view of one device: the settings an
// edit form has to show before it can change them.
//
// It is distinct from GET /api/v1/devices, which is the dashboard's inventory
// with live health. That view carries no settings, because the panels do not
// need them and joining them in would cost a query per row.
func (s *Server) getDevice(w http.ResponseWriter, r *http.Request) {
	devEUI, ok := pathDevEUI(w, r)
	if !ok {
		return
	}
	if s.controller == nil {
		writeError(w, http.StatusServiceUnavailable, ErrControllerNotConfigured)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), controlTimeout)
	defer cancel()

	resp, err := s.controller.Device(ctx, devEUI)
	if err != nil {
		writeUpstreamError(w, "read device", devEUI, err, ErrDeviceQueryFailed)
		return
	}
	writeRaw(w, http.StatusOK, resp)
}

// editDeviceRequest is the body of PATCH /api/v1/devices/{dev_eui}.
//
// It mirrors the network server's patch shape rather than accepting a whole
// device, because the console cannot supply credentials: the network server never
// returns them. A replace-based edit would therefore be unusable from this UI,
// which is exactly the gap this endpoint closes.
type editDeviceRequest struct {
	Name           *string `json:"name"`
	Group          *string `json:"group"`
	Enabled        *bool   `json:"enabled"`
	DisableADR     *bool   `json:"disable_adr"`
	SupportsClassC *bool   `json:"supports_class_c"`
	MaxDataRate    *int    `json:"max_data_rate"`
	MinDataRate    *int    `json:"min_data_rate"`
	MaxTxPower     *int    `json:"max_tx_power"`
}

// updateDevice relays a metadata edit to the network server, which owns the
// device row the dashboard reads.
//
// There is no second write here: the console's inventory view is built from the
// network server's tables, so a local copy of the name would be a second source
// of truth that can disagree with the one the engine acts on.
func (s *Server) updateDevice(w http.ResponseWriter, r *http.Request) {
	devEUI, ok := pathDevEUI(w, r)
	if !ok {
		return
	}
	if s.controller == nil {
		writeError(w, http.StatusServiceUnavailable, ErrControllerNotConfigured)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBytes)
	var input editDeviceRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidJSON)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, ErrInvalidJSON)
		return
	}

	if input.Name == nil && input.Group == nil && input.Enabled == nil &&
		input.DisableADR == nil && input.SupportsClassC == nil &&
		input.MaxDataRate == nil && input.MinDataRate == nil && input.MaxTxPower == nil {
		writeError(w, http.StatusBadRequest, ErrNoFieldsToChange)
		return
	}
	// Length and range checks mirror the network server's and the create path's.
	// Repeating them costs nothing and turns "the console accepted it and the ns
	// refused it" into a rejection that names the field before a round trip.
	if input.Name != nil {
		name := strings.TrimSpace(*input.Name)
		// An empty name is refused here but not by the network server, which
		// stores whatever it is given. A device with no label is unusable in the
		// console's own list, so the field this service owns is the one that
		// has to keep it non-blank.
		if utf8.RuneCountInString(name) == 0 || utf8.RuneCountInString(name) > MaxNameLen {
			writeError(w, http.StatusBadRequest, ErrNameRequired)
			return
		}
	}
	if input.Group != nil && utf8.RuneCountInString(strings.TrimSpace(*input.Group)) > MaxGroupLen {
		writeError(w, http.StatusBadRequest, ErrGroupTooLong)
		return
	}
	for _, limit := range []struct {
		value *int
	}{{input.MaxDataRate}, {input.MinDataRate}, {input.MaxTxPower}} {
		if limit.value != nil && (*limit.value < -1 || *limit.value > 15) {
			writeError(w, http.StatusBadRequest, ErrDataRateOutOfRange)
			return
		}
	}
	if input.MinDataRate != nil && input.MaxDataRate != nil &&
		*input.MinDataRate != regionDefault && *input.MaxDataRate != regionDefault &&
		*input.MinDataRate > *input.MaxDataRate {
		writeError(w, http.StatusBadRequest, ErrDataRateOrder)
		return
	}

	patch := nsclient.DevicePatch{
		Name:           trimOrNil(input.Name),
		Group:          trimOrNil(input.Group),
		Enabled:        input.Enabled,
		DisableADR:     input.DisableADR,
		SupportsClassC: input.SupportsClassC,
		MaxDataRate:    input.MaxDataRate,
		MinDataRate:    input.MinDataRate,
		MaxTxPower:     input.MaxTxPower,
	}

	ctx, cancel := context.WithTimeout(r.Context(), controlTimeout)
	defer cancel()

	resp, err := s.controller.PatchDevice(ctx, devEUI, patch)
	if err != nil {
		writeUpstreamError(w, "edit device", devEUI, err, ErrDeviceUpdateFailed)
		return
	}
	writeRaw(w, http.StatusOK, resp)
}

// deleteDevice revokes a device through the network server, which removes its
// session in the same operation.
//
// The console does not delete anything itself. The row the dashboard shows comes
// from the network server's tables, so removing a local record would leave a
// device that vanished from the UI and is still on the air — the worst possible
// outcome for an operation whose purpose is to stop a device talking.
func (s *Server) deleteDevice(w http.ResponseWriter, r *http.Request) {
	devEUI, ok := pathDevEUI(w, r)
	if !ok {
		return
	}
	if s.controller == nil {
		writeError(w, http.StatusServiceUnavailable, ErrControllerNotConfigured)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), controlTimeout)
	defer cancel()

	if _, err := s.controller.DeleteDevice(ctx, devEUI); err != nil {
		writeUpstreamError(w, "delete device", devEUI, err, ErrDeviceDeleteFailed)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// trimOrNil trims a supplied string, passing an absent field through as nil so
// the network server leaves it untouched.
func trimOrNil(in *string) *string {
	if in == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*in)
	return &trimmed
}

// pathDevEUI extracts and validates the {dev_eui} path segment.
func pathDevEUI(w http.ResponseWriter, r *http.Request) (string, bool) {
	devEUI := normalizeHex(r.PathValue("dev_eui"))
	if !devEUIPattern.MatchString(devEUI) {
		writeError(w, http.StatusBadRequest, ErrDevEUIHex)
		return "", false
	}
	return devEUI, true
}

// writeRaw forwards the network server's body unchanged.
//
// Passing the bytes through rather than decoding into a local struct is the
// point: a field the console has not heard of still reaches the dashboard, so a
// network-server upgrade shows up as a new value rather than as a column that
// quietly went blank.
func writeRaw(w http.ResponseWriter, status int, body json.RawMessage) {
	if len(body) == 0 {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// writeUpstreamError translates a network-server failure into a status the
// dashboard can act on, keeping the server's own reason as detail.
//
// The distinction that matters is between "the network server is not there" and
// "the network server said no". They look identical in a generic 500 and call for
// completely different fixes.
func writeUpstreamError(w http.ResponseWriter, op, devEUI string, err error, fallback string) {
	var transport *nsclient.TransportError
	var apiErr *nsclient.APIError
	var conflict *nsclient.ConflictError

	switch {
	case errors.As(err, &conflict):
		writeErrorWithDetail(w, http.StatusConflict, ErrDeviceAlreadyRegistered, conflict.Detail)
	case errors.As(err, &apiErr):
		// The network server's rejection is authoritative, including the useful
		// case of 404 for a device that has never joined.
		status := apiErr.Status
		if status < 400 || status > 599 {
			status = http.StatusBadGateway
		}
		writeErrorWithDetail(w, status, fallback, apiErr.Detail)
	case errors.As(err, &transport):
		slog.Error(op+": network server unreachable", "dev_eui", devEUI, "err", err)
		writeError(w, http.StatusBadGateway, ErrNetworkServerUnreachable)
	case errors.Is(err, context.DeadlineExceeded):
		slog.Error(op+": network server timed out", "dev_eui", devEUI, "err", err)
		writeError(w, http.StatusGatewayTimeout, ErrNetworkServerTimeout)
	default:
		slog.Error(op, "dev_eui", devEUI, "err", err)
		writeError(w, http.StatusBadGateway, fallback)
	}
}

func writeErrorWithDetail(w http.ResponseWriter, status int, code, detail string) {
	body := map[string]string{"error": code}
	if detail != "" {
		body["detail"] = detail
	}
	writeJSON(w, status, body)
}
