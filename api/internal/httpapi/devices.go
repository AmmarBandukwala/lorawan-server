package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fieldnode/lorawan-server/api/internal/nsclient"
)

// Device endpoints.
//
// Reading devices is a database read. Writing one is not: it is a call to the
// network server, which owns the rules about what a usable device is. The
// previous version inserted an inventory row into a table nothing read, so
// "Register device" produced a record that could never join and a dashboard that
// kept showing the device as absent.

type createDeviceRequest struct {
	Name    string `json:"name"`
	DevEUI  string `json:"devEui"`
	JoinEUI string `json:"joinEui"`
	Group   string `json:"group"`
	// AppKey is the OTAA root key, entered as it appears on the device label.
	// It is accepted here and forwarded once; it is never stored by this service
	// and never returned, so there is nothing here to leak.
	AppKey string `json:"appKey"`
}

type deviceResponse struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	EUI      string `json:"eui"`
	Group    string `json:"group"`
	Gateway  string `json:"gateway"`
	LastSeen string `json:"lastSeen"`
	Battery  int16  `json:"battery"`
	State    string `json:"state"`
	RSSI     int16  `json:"rssi"`
}

var (
	devEUIPattern = regexp.MustCompile(`^[0-9A-F]{16}$`)
	appKeyPattern = regexp.MustCompile(`^[0-9A-F]{32}$`)
)

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := s.repository.ListDevices(r.Context())
	if err != nil {
		slog.Error("list devices", "err", err)
		writeError(w, http.StatusInternalServerError, "device_query_failed")
		return
	}

	now := time.Now()
	response := make([]deviceResponse, 0, len(devices))
	for _, device := range devices {
		response = append(response, deviceResponse{
			ID: device.DevEUI, Name: device.Name, EUI: device.DevEUI,
			Group: device.Group, Gateway: device.Gateway,
			LastSeen: relativeTime(now, device.LastSeen), Battery: device.Battery,
			State: device.State, RSSI: device.RSSI,
		})
	}
	writeJSON(w, http.StatusOK, response)
}

// normalizeHex strips the separators a datasheet uses ("AA-BB-CC-…" or
// "aa:bb:…") and upper-cases, so the value that reaches the network server is
// the one its CHECK constraint expects.
func normalizeHex(value string) string {
	return strings.ToUpper(strings.NewReplacer("-", "", ":", "", " ", "").Replace(strings.TrimSpace(value)))
}

// NSProvisioner adapts a network-server client to the Provisioner port.
//
// The adapter exists so the handler depends on a two-method interface it owns
// rather than on a concrete client: that is what lets the handler be tested with
// a fake, and what keeps nsclient free of dashboard vocabulary.
type NSProvisioner struct{ client *nsclient.Client }

func NewNSProvisioner(client *nsclient.Client) *NSProvisioner {
	return &NSProvisioner{client: client}
}

// CreateDevice provisions an OTAA device. Only OTAA is offered here: ABP needs a
// DevAddr and two session keys, which is a network-engineering decision rather
// than something to guess from a registration form, and the ns API accepts it
// for operators who need it.
func (p *NSProvisioner) CreateDevice(ctx context.Context, d Provisioning) (ProvisionedDevice, error) {
	created, err := p.client.CreateDevice(ctx, nsclient.Device{
		DevEUI:  d.DevEUI,
		JoinEUI: d.JoinEUI,
		Name:    d.Name,
		Group:   d.Group,
		AppKey:  d.AppKey,
	})
	if err != nil {
		return ProvisionedDevice{}, err
	}
	return ProvisionedDevice{
		DevEUI: created.DevEUI, Name: created.Name,
		Group: created.Group, Activation: created.Activation,
	}, nil
}

func (s *Server) createDevice(w http.ResponseWriter, r *http.Request) {
	// A provisioning body is a handful of identifiers; anything larger is not a
	// device registration.
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	var input createDeviceRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_json")
		return
	}

	name := strings.TrimSpace(input.Name)
	// Counted in runes, not bytes: a facility name with non-Latin characters is
	// legitimate, and a byte count would reject it at a length that looks
	// arbitrary to whoever typed it.
	if nameLen := utf8.RuneCountInString(name); nameLen == 0 || nameLen > 120 {
		writeError(w, http.StatusBadRequest, "name_must_be_1_to_120_characters")
		return
	}

	devEUI := normalizeHex(input.DevEUI)
	if !devEUIPattern.MatchString(devEUI) {
		writeError(w, http.StatusBadRequest, "deveui_must_be_16_hex_characters")
		return
	}
	appKey := normalizeHex(input.AppKey)
	if !appKeyPattern.MatchString(appKey) {
		writeError(w, http.StatusBadRequest, "appkey_must_be_32_hex_characters")
		return
	}
	// JoinEUI is optional: an operator reading a label that prints only a DevEUI
	// and AppKey should not be blocked. Leaving it empty stores the zero JoinEUI,
	// which the engine treats as "accept any" rather than comparing it, so the
	// device can join regardless of what it puts in its join-request.
	joinEUI := normalizeHex(input.JoinEUI)
	if joinEUI != "" && !devEUIPattern.MatchString(joinEUI) {
		writeError(w, http.StatusBadRequest, "joineui_must_be_16_hex_characters")
		return
	}

	group := strings.TrimSpace(input.Group)
	if group == "" {
		group = "Unassigned"
	}
	if utf8.RuneCountInString(group) > 60 {
		writeError(w, http.StatusBadRequest, "group_must_be_at_most_60_characters")
		return
	}

	if s.provisioner == nil {
		// Configured, not requested: this service cannot provision without a
		// network server to talk to, and saying so beats a 500 or a row that
		// does nothing.
		writeError(w, http.StatusServiceUnavailable, "provisioning_not_configured")
		return
	}

	// The network server is a separate process, so this call needs a deadline of
	// its own. Without one, an ns that has stopped answering holds the request,
	// and the browser's own timeout leaves the operator guessing whether the
	// device was created.
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	created, err := s.provisioner.CreateDevice(ctx, Provisioning{
		DevEUI: devEUI, JoinEUI: joinEUI, Name: name, Group: group, AppKey: appKey,
	})
	if err != nil {
		var conflict *nsclient.ConflictError
		switch {
		case errors.As(err, &conflict):
			writeError(w, http.StatusConflict, "device_already_registered")
		case errors.Is(err, context.DeadlineExceeded):
			slog.Error("provision device timed out", "err", err)
			writeError(w, http.StatusGatewayTimeout, "network_server_timed_out")
		default:
			slog.Error("provision device", "err", err)
			writeError(w, http.StatusBadGateway, "network_server_unreachable")
		}
		return
	}

	// The response is the freshly provisioned device as the dashboard renders it,
	// so the new row appears without waiting for the next refresh cycle.
	writeJSON(w, http.StatusCreated, deviceResponse{
		ID: created.DevEUI, Name: created.Name, EUI: created.DevEUI,
		Group: created.Group, Gateway: "Not joined", LastSeen: "Never",
		Battery: 0, State: "Offline", RSSI: 0,
	})
}
