package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fieldnode/lorawan-server/api/internal/domain"
)

type createDeviceRequest struct {
	Name   string `json:"name"`
	DevEUI string `json:"devEui"`
	Group  string `json:"group"`
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

var devEUIPattern = regexp.MustCompile(`^[0-9A-F]{16}$`)

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := s.repository.ListDevices(r.Context())
	if err != nil {
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

func (s *Server) createDevice(w http.ResponseWriter, r *http.Request) {
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

	input.Name = strings.TrimSpace(input.Name)
	input.DevEUI = strings.ToUpper(strings.TrimSpace(input.DevEUI))
	input.Group = strings.TrimSpace(input.Group)
	if utf8.RuneCountInString(input.Name) == 0 || utf8.RuneCountInString(input.Name) > 120 || !devEUIPattern.MatchString(input.DevEUI) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_device")
		return
	}
	if input.Group == "" {
		input.Group = "Unassigned"
	}

	create := domain.CreateDevice{DevEUI: input.DevEUI, Name: input.Name, Group: input.Group}
	if err := s.repository.CreateDevice(r.Context(), create); err != nil {
		if errors.Is(err, domain.ErrDeviceAlreadyExists) {
			writeError(w, http.StatusConflict, "device_already_exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "device_create_failed")
		return
	}
	writeJSON(w, http.StatusCreated, deviceResponse{
		ID: input.DevEUI, EUI: input.DevEUI, Name: input.Name, Group: input.Group,
		Gateway: "Not joined", LastSeen: "Never", State: "Offline",
	})
}
