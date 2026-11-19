package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
)

// Device provisioning API: the endpoint an operator's console calls to put a
// device on the network.
//
// The security rule that shapes this whole file: keys go in and never come out.
// A provisioning request carries an AppKey or the ABP session keys, and every
// response omits them. That is not paranoia about this endpoint's token — the
// response gets pasted into tickets and logged by proxies, and a session key in
// a log line means an attacker can forge downlinks to that device forever. The
// list endpoint is the one an operator uses most, so it is the one that must
// provably not leak.

// deviceView is the read shape of a device. Keys are absent by construction:
// there is no field to forget to clear.
type deviceView struct {
	DevEUI  string `json:"dev_eui"`
	JoinEUI string `json:"join_eui"`
	Name    string `json:"name,omitempty"`
	Enabled bool   `json:"enabled"`
	// Activation tells an operator which credentials the device was given
	// without revealing them.
	Activation  string `json:"activation"`
	DevAddr     string `json:"dev_addr,omitempty"` // ABP only
	Group       string `json:"group,omitempty"`
	DisableADR  bool   `json:"disable_adr"`
	SupportsC   bool   `json:"supports_class_c"`
	MaxDataRate int    `json:"max_data_rate"`
	MinDataRate int    `json:"min_data_rate"`
	MaxTxPower  int    `json:"max_tx_power"`
}

func newDeviceView(d *engine.Device) deviceView {
	v := deviceView{
		DevEUI:      d.DevEUI.String(),
		JoinEUI:     d.JoinEUI.String(),
		Name:        d.Name,
		Enabled:     d.Enabled,
		Activation:  "otaa",
		Group:       d.Group,
		DisableADR:  d.DisableADR,
		SupportsC:   d.SupportsClassC,
		MaxDataRate: d.MaxDataRate,
		MinDataRate: d.MinDataRate,
		MaxTxPower:  d.MaxTxPower,
	}
	if !d.OTAA {
		v.Activation = "abp"
		v.DevAddr = d.DevAddr.String()
	}
	return v
}

// provisionDeviceRequest is the body of POST and PUT /api/v1/devices.
//
// Exactly one credential set must be present. Accepting "whatever was provided"
// would let a half-filled ABP request silently become an OTAA device, which
// then never joins and looks like a radio problem.
type provisionDeviceRequest struct {
	DevEUI  string `json:"dev_eui"`
	JoinEUI string `json:"join_eui"`
	Name    string `json:"name"`

	// OTAA credentials.
	AppKey string `json:"app_key"`

	// ABP credentials; all three are required together.
	DevAddr string `json:"dev_addr"`
	NwkSKey string `json:"nwk_s_key"`
	AppSKey string `json:"app_s_key"`

	// Enabled defaults to true; sending false stores a device that is known but
	// not yet allowed on the air, which is how a staged rollout is provisioned.
	Enabled        *bool `json:"enabled"`
	DisableADR     bool  `json:"disable_adr"`
	SupportsClassC bool  `json:"supports_class_c"`

	// Group is a dashboard label. The engine ignores it, but it belongs to the
	// device row so a device is not split across two tables.
	Group string `json:"group"`
}

// maxNameLen bounds the free-text label. It is display data, but an unbounded
// string in a column that also feeds a dashboard layout is how a UI gets broken
// by one bad record.
const maxNameLen = 120

// decodeDeviceRequest reads and validates a provisioning body, returning the
// assembled device. Every rejection names the field, because these requests are
// typed out of a datasheet and "invalid request" is no help at 2am.
func decodeDeviceRequest(w http.ResponseWriter, r *http.Request) (*engine.Device, bool) {
	// 4 KiB is generous for this body and stops a large upload being buffered.
	body := http.MaxBytesReader(w, r.Body, 4096)
	var req provisionDeviceRequest
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		badRequest(w, "body must be a JSON object with the fields documented for this endpoint: "+err.Error())
		return nil, false
	}
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		badRequest(w, "body must contain exactly one JSON object")
		return nil, false
	}

	devEUI, err := lorawan.ParseEUI64(strings.TrimSpace(req.DevEUI))
	if err != nil {
		badRequest(w, "dev_eui must be 16 hex digits")
		return nil, false
	}
	// An absent JoinEUI means "match any", which is the engine's existing rule for
	// a device stored with the zero JoinEUI (see engine.join: a zero JoinEUI is
	// never compared against the join-request). It is worth supporting here
	// because plenty of devices ship with a JoinEUI the operator cannot read off
	// a label, and forcing a wrong value in would produce join_eui_mismatch drops
	// that look like a radio problem.
	joinEUI := lorawan.EUI64{}
	if raw := strings.TrimSpace(req.JoinEUI); raw != "" {
		joinEUI, err = lorawan.ParseEUI64(raw)
		if err != nil {
			badRequest(w, "join_eui must be 16 hex digits, or omitted to accept any")
			return nil, false
		}
	}
	name := strings.TrimSpace(req.Name)
	if len(name) > maxNameLen {
		badRequest(w, "name must be at most 120 characters")
		return nil, false
	}
	group := strings.TrimSpace(req.Group)
	if len(group) > maxNameLen {
		badRequest(w, "group must be at most 120 characters")
		return nil, false
	}

	otaaFields := strings.TrimSpace(req.AppKey) != ""
	abpFields := strings.TrimSpace(req.DevAddr) != "" ||
		strings.TrimSpace(req.NwkSKey) != "" || strings.TrimSpace(req.AppSKey) != ""

	var dev *engine.Device
	switch {
	case otaaFields && abpFields:
		badRequest(w, "provide either app_key (OTAA) or dev_addr with nwk_s_key and app_s_key (ABP), not both")
		return nil, false
	case otaaFields:
		appKey, err := lorawan.ParseAES128Key(strings.TrimSpace(req.AppKey))
		if err != nil {
			badRequest(w, "app_key must be 32 hex digits")
			return nil, false
		}
		dev = engine.NewOTAADevice(devEUI, joinEUI, appKey)
	case abpFields:
		devAddr, err := lorawan.ParseDevAddr(strings.TrimSpace(req.DevAddr))
		if err != nil {
			badRequest(w, "dev_addr must be 8 hex digits")
			return nil, false
		}
		nwkSKey, err := lorawan.ParseAES128Key(strings.TrimSpace(req.NwkSKey))
		if err != nil {
			badRequest(w, "nwk_s_key must be 32 hex digits")
			return nil, false
		}
		appSKey, err := lorawan.ParseAES128Key(strings.TrimSpace(req.AppSKey))
		if err != nil {
			badRequest(w, "app_s_key must be 32 hex digits")
			return nil, false
		}
		dev = engine.NewABPDevice(devEUI, joinEUI, devAddr, nwkSKey, appSKey)
	default:
		badRequest(w, "credentials are required: app_key for OTAA, or dev_addr with nwk_s_key and app_s_key for ABP")
		return nil, false
	}

	dev.Name = name
	dev.Group = group
	dev.Enabled = true
	if req.Enabled != nil {
		dev.Enabled = *req.Enabled
	}
	dev.DisableADR = req.DisableADR
	dev.SupportsClassC = req.SupportsClassC
	return dev, true
}

// handleListDevices serves GET /api/v1/devices.
//
// The whole inventory is returned because a Fieldnode deployment is a facility,
// not a country: thousands of devices is the expected ceiling, and a paginated
// contract would be paid for in complexity long before it earned its keep.
func (s *apiServer) handleListDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := s.devices.ListDevices(r.Context())
	if err != nil {
		s.log.Error("list devices", "err", err)
		writeError(w, http.StatusInternalServerError, msgDeviceListFailed)
		return
	}
	views := make([]deviceView, 0, len(devices))
	for _, d := range devices {
		views = append(views, newDeviceView(d))
	}
	writeJSON(w, http.StatusOK, views)
}

// handleGetDevice serves GET /api/v1/devices/{dev_eui}.
//
// It exists because the list endpoint is the wrong tool for "what is this one
// device configured as": a dashboard or a script that has a DevEUI should not
// have to fetch and scan the whole inventory, and a 404 here is the fastest way
// for an operator to learn a device was never provisioned.
func (s *apiServer) handleGetDevice(w http.ResponseWriter, r *http.Request) {
	devEUI, ok := pathDevEUI(w, r)
	if !ok {
		return
	}
	d, err := s.devices.DeviceByDevEUI(r.Context(), devEUI)
	if errors.Is(err, engine.ErrNotFound) {
		notFound(w, "dev_eui "+devEUI.String()+" is not provisioned")
		return
	}
	if err != nil {
		s.log.Error("read a device", "dev_eui", devEUI, "err", err)
		writeError(w, http.StatusInternalServerError, msgStoreUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, newDeviceView(d))
}

// handleCreateDevice serves POST /api/v1/devices.
//
// Creating over an existing device is refused rather than applied: an upsert
// here means a typo'd DevEUI silently rewrites a live device's keys, after which
// the real device stops joining and nothing in the logs says why. PUT is the
// endpoint that means "replace".
func (s *apiServer) handleCreateDevice(w http.ResponseWriter, r *http.Request) {
	dev, ok := decodeDeviceRequest(w, r)
	if !ok {
		return
	}
	existing, err := s.devices.DeviceByDevEUI(r.Context(), dev.DevEUI)
	if err != nil && !errors.Is(err, engine.ErrNotFound) {
		s.log.Error("look up a device before creating it", "dev_eui", dev.DevEUI, "err", err)
		writeError(w, http.StatusInternalServerError, msgStoreUnavailable)
		return
	}
	if existing != nil {
		writeError(w, http.StatusConflict,
			"dev_eui "+dev.DevEUI.String()+" is already provisioned; use PUT to replace it")
		return
	}
	if err := s.devices.SaveDevice(r.Context(), dev); err != nil {
		s.log.Error("save a new device", "dev_eui", dev.DevEUI, "err", err)
		writeError(w, http.StatusInternalServerError, msgDeviceSaveFailed)
		return
	}
	s.log.Info("device provisioned", "dev_eui", dev.DevEUI, "name", dev.Name,
		"activation", newDeviceView(dev).Activation, "enabled", dev.Enabled)
	writeJSON(w, http.StatusCreated, newDeviceView(dev))
}

// handleUpdateDevice serves PUT /api/v1/devices/{dev_eui}.
//
// A replace, not a patch: the body must carry credentials again, because a
// device record without keys is not a valid state and a partial update that
// drops them would leave the engine unable to decrypt anything. The path wins
// over the body for DevEUI so a device cannot be renamed into another one.
func (s *apiServer) handleUpdateDevice(w http.ResponseWriter, r *http.Request) {
	pathEUI, ok := pathDevEUI(w, r)
	if !ok {
		return
	}
	dev, ok := decodeDeviceRequest(w, r)
	if !ok {
		return
	}
	// decodeDeviceRequest already requires a valid dev_eui, so a mismatch can
	// only be a mistake; treating the path as authoritative anyway would let a
	// request aimed at one device overwrite another.
	if dev.DevEUI != pathEUI {
		badRequest(w, "dev_eui in the body must match the address in the path")
		return
	}

	existing, err := s.devices.DeviceByDevEUI(r.Context(), pathEUI)
	if errors.Is(err, engine.ErrNotFound) {
		notFound(w, "dev_eui "+pathEUI.String()+" is not provisioned")
		return
	}
	if err != nil {
		s.log.Error("look up a device before updating it", "dev_eui", pathEUI, "err", err)
		writeError(w, http.StatusInternalServerError, msgStoreUnavailable)
		return
	}

	// The join-nonce counter and the DevNonce replay log are engine state, not
	// operator input, and a replace that reset them would let a captured
	// join-accept from months ago become valid again. Carry them over.
	dev.NextJoinNonce = existing.NextJoinNonce
	dev.UsedDevNonces = existing.UsedDevNonces

	if err := s.devices.SaveDevice(r.Context(), dev); err != nil {
		s.log.Error("update a device", "dev_eui", pathEUI, "err", err)
		writeError(w, http.StatusInternalServerError, msgDeviceSaveFailed)
		return
	}
	s.log.Info("device updated", "dev_eui", pathEUI, "name", dev.Name, "enabled", dev.Enabled)
	writeJSON(w, http.StatusOK, newDeviceView(dev))
}

// handleDeleteDevice serves DELETE /api/v1/devices/{dev_eui}.
//
// Deleting removes the session too, which is the point: revoking a device has to
// stop it talking, and a surviving session would let it keep exchanging frames
// with keys the operator just withdrew.
func (s *apiServer) handleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	devEUI, ok := pathDevEUI(w, r)
	if !ok {
		return
	}
	if err := s.devices.DeleteDevice(r.Context(), devEUI); err != nil {
		if errors.Is(err, engine.ErrNotFound) {
			notFound(w, "dev_eui "+devEUI.String()+" is not provisioned")
			return
		}
		s.log.Error("delete a device", "dev_eui", devEUI, "err", err)
		writeError(w, http.StatusInternalServerError, msgDeviceDeleteFail)
		return
	}
	s.log.Info("device deleted", "dev_eui", devEUI)
	w.WriteHeader(http.StatusNoContent)
}
