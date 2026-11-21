package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/codec"
	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/pgstore"
)

// Device-model API: what a sensor of one kind looks like, and how its bytes are
// read.
//
// This is the endpoint an operator pastes vendor-supplied JavaScript into, and
// two rules shape the file:
//
//   - A script is validated before it is stored. A formatter that fails to
//     compile is refused here, with the parser's own message, rather than
//     accepted and then failing on the device's first frame at three in the
//     morning where the only evidence is a decode-error counter nobody watches.
//   - The script is returned on read. It is not a secret — it came from a vendor's
//     datasheet — and an operator who cannot see what a fleet is running cannot
//     audit it. The list endpoint omits scripts, because a fleet's worth of them
//     is hundreds of kilobytes and the picker needs names.

// modelIDPattern is the slug rule from the schema, enforced here too so a bad
// identifier is a 400 with an explanation rather than a constraint violation.
var modelIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,63}$`)

// maxCodecScript caps a stored formatter. The codec refuses to run anything
// larger, so accepting it here would only defer the failure to first use.
const maxCodecScript = 64 << 10

// modelView is the read shape of a device model.
type modelView struct {
	ModelID     string `json:"model_id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	CodecKind   string `json:"codec_kind"`
	CodecScript string `json:"codec_script,omitempty"`
	// CodecPresent lets a list response say "this model decodes" without shipping
	// every script in the fleet in one payload.
	CodecPresent bool   `json:"codec_present"`
	CreatedAt    string `json:"created_at,omitempty"`
	UpdatedAt    string `json:"updated_at,omitempty"`
}

func newModelView(model *engine.DeviceModel, withScript bool) modelView {
	v := modelView{
		ModelID:      model.ModelID,
		Name:         model.Name,
		Description:  model.Description,
		CodecKind:    model.CodecKind,
		CodecPresent: model.CodecKind == string(codec.KindJS) && model.CodecScript != "",
	}
	if withScript {
		v.CodecScript = model.CodecScript
	}
	if !model.CreatedAt.IsZero() {
		v.CreatedAt = model.CreatedAt.UTC().Format(time.RFC3339)
	}
	if !model.UpdatedAt.IsZero() {
		v.UpdatedAt = model.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return v
}

// saveModelRequest is the write shape. A name is required: a model with no name
// is a slug nobody recognises in a dropdown a month later.
type saveModelRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	CodecKind   string `json:"codec_kind"`
	CodecScript string `json:"codec_script"`
}

func (s *apiServer) handleListModels(w http.ResponseWriter, r *http.Request) {
	if s.models == nil {
		s.noModelsStore(w)
		return
	}
	models, err := s.models.ListModels(r.Context())
	if err != nil {
		s.log.Error("cannot list device models", "err", err)
		writeError(w, http.StatusInternalServerError, msgModelListFailed)
		return
	}
	views := make([]modelView, 0, len(models))
	for _, m := range models {
		views = append(views, newModelView(m, false))
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": views})
}

func (s *apiServer) handleGetModel(w http.ResponseWriter, r *http.Request) {
	if s.models == nil {
		s.noModelsStore(w)
		return
	}
	id := r.PathValue("model_id")
	m, err := s.models.ModelByID(r.Context(), id)
	if err != nil {
		s.writeModelError(w, id, err)
		return
	}
	writeJSON(w, http.StatusOK, newModelView(m, true))
}

// msgModel* are this file's 5xx sentences, named so the same failure always reads
// the same way.
const (
	msgModelListFailed   = "device model list unavailable"
	msgModelSaveFailed   = "device model could not be saved"
	msgModelDeleteFailed = "device model could not be deleted"
	msgUplinkListFailed  = "uplink history unavailable"
	msgNoModelStore      = "device models are not available with this database configuration"
)

// noModelsStore reports that the configured store cannot hold models. The
// in-memory store is a bench mode with no persistence, so refusing is honest
// where a 500 would be confusing.
func (s *apiServer) noModelsStore(w http.ResponseWriter) {
	writeError(w, http.StatusServiceUnavailable, msgNoModelStore)
}

// handleCreateModel installs a new model. The slug comes from the path because an
// opaque generated id would be pasted into gateway configs and tickets, and
// "cold-chain-probe-v2" survives that journey.
func (s *apiServer) handleCreateModel(w http.ResponseWriter, r *http.Request) {
	if s.models == nil {
		s.noModelsStore(w)
		return
	}
	id := r.PathValue("model_id")
	if !modelIDPattern.MatchString(id) {
		badRequest(w, "a model id must be 2 to 64 characters of lower-case letters, "+
			"digits, '-' or '_', starting with a letter or digit")
		return
	}
	req, ok := readModelRequest(w, r)
	if !ok {
		return
	}
	// A create that would overwrite is refused: an operator who means to replace a
	// formatter uses PUT, and a typo in the slug must not silently rewrite a model
	// a fleet depends on.
	existing, err := s.models.ModelByID(r.Context(), id)
	if err != nil && !errors.Is(err, engine.ErrNotFound) {
		s.writeModelError(w, id, err)
		return
	}
	if err == nil && existing != nil {
		writeError(w, http.StatusConflict,
			fmt.Sprintf("a device model named %q already exists; use PUT to replace it", id))
		return
	}
	m, ok := s.buildModel(w, id, req)
	if !ok {
		return
	}
	if err := s.models.SaveModel(r.Context(), m); err != nil {
		s.log.Error("cannot save a device model", "model_id", id, "err", err)
		writeError(w, http.StatusInternalServerError, msgModelSaveFailed)
		return
	}
	s.invalidateModel(id)
	writeJSON(w, http.StatusCreated, newModelView(m, true))
}

// handleUpdateModel is an upsert. A model describes a physical thing, so PUT to a
// known slug and PUT to a new one are both ordinary acts; a 404 on the second
// would only encourage storing models somewhere else.
func (s *apiServer) handleUpdateModel(w http.ResponseWriter, r *http.Request) {
	if s.models == nil {
		s.noModelsStore(w)
		return
	}
	id := r.PathValue("model_id")
	if !modelIDPattern.MatchString(id) {
		badRequest(w, "a model id must be 2 to 64 characters of lower-case letters, "+
			"digits, '-' or '_', starting with a letter or digit")
		return
	}
	req, ok := readModelRequest(w, r)
	if !ok {
		return
	}
	m, ok := s.buildModel(w, id, req)
	if !ok {
		return
	}
	if err := s.models.SaveModel(r.Context(), m); err != nil {
		s.log.Error("cannot save a device model", "model_id", id, "err", err)
		writeError(w, http.StatusInternalServerError, msgModelSaveFailed)
		return
	}
	s.invalidateModel(id)
	// Re-read so the timestamps in the response are the stored ones rather than
	// the zero values this process never set.
	if stored, err := s.models.ModelByID(r.Context(), id); err == nil {
		writeJSON(w, http.StatusOK, newModelView(stored, true))
		return
	}
	writeJSON(w, http.StatusOK, newModelView(m, true))
}

// readModelRequest decodes a model body. The cap is the script limit plus room
// for the surrounding JSON, so an oversized formatter is refused by the clearer
// message in buildModel rather than by a reader error.
func readModelRequest(w http.ResponseWriter, r *http.Request) (saveModelRequest, bool) {
	var req saveModelRequest
	body := io.LimitReader(r.Body, int64(maxCodecScript)+4096)
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		badRequest(w, "the request body is not valid JSON")
		return req, false
	}
	return req, true
}

// writeModelError reports a failed model read.
func (s *apiServer) writeModelError(w http.ResponseWriter, id string, err error) {
	if errors.Is(err, engine.ErrNotFound) {
		notFound(w, fmt.Sprintf("no device model named %q", id))
		return
	}
	s.log.Error("cannot read a device model", "model_id", id, "err", err)
	writeError(w, http.StatusInternalServerError, msgModelListFailed)
}

// handleDeleteModel removes a model. The devices that referenced it survive: the
// schema's foreign key is ON DELETE SET NULL, so they keep reporting and simply
// stop being decoded. The count is reported because that consequence is invisible
// otherwise.
func (s *apiServer) handleDeleteModel(w http.ResponseWriter, r *http.Request) {
	if s.models == nil {
		s.noModelsStore(w)
		return
	}
	id := r.PathValue("model_id")

	var devices int
	if counter, ok := s.models.(modelDeviceCounter); ok {
		n, err := counter.DevicesOnModel(r.Context(), id)
		if err != nil {
			s.log.Error("cannot count the devices on a model", "model_id", id, "err", err)
			writeError(w, http.StatusInternalServerError, msgModelListFailed)
			return
		}
		devices = n
	}
	if err := s.models.DeleteModel(r.Context(), id); err != nil {
		if errors.Is(err, engine.ErrNotFound) {
			notFound(w, fmt.Sprintf("no device model named %q", id))
			return
		}
		s.log.Error("cannot delete a device model", "model_id", id, "err", err)
		writeError(w, http.StatusInternalServerError, msgModelDeleteFailed)
		return
	}
	s.invalidateModel(id)
	writeJSON(w, http.StatusOK, map[string]any{
		"deleted":              id,
		"devices_unassociated": devices,
		"note":                 "those devices still report; they are simply no longer decoded",
	})
}

// modelDeviceCounter is the optional capability that reports how many devices
// reference a model. The in-memory store does not implement it, and deleting
// still works without it.
type modelDeviceCounter interface {
	DevicesOnModel(ctx context.Context, modelID string) (int, error)
}

// buildModel validates a request into a storable model.
//
// The codec is compiled here, at save time. That is the point of this function: a
// syntax error, a script with neither entry point, or a JS model with no script
// are all caught while the person who typed them is still looking at the screen.
func (s *apiServer) buildModel(w http.ResponseWriter, id string, req saveModelRequest,
) (*engine.DeviceModel, bool) {

	name := strings.TrimSpace(req.Name)
	if name == "" {
		badRequest(w, "a device model needs a name")
		return nil, false
	}
	if len(name) > maxNameLen {
		badRequest(w, fmt.Sprintf("a model name must be at most %d characters", maxNameLen))
		return nil, false
	}
	kind := strings.ToUpper(strings.TrimSpace(req.CodecKind))
	if kind == "" {
		kind = string(codec.KindNone)
	}
	if kind != string(codec.KindNone) && kind != string(codec.KindJS) {
		badRequest(w, fmt.Sprintf("codec kind %q is not one of NONE or JS", req.CodecKind))
		return nil, false
	}
	script := req.CodecScript
	if len(script) > maxCodecScript {
		badRequest(w, fmt.Sprintf("a payload formatter must be under %d bytes", maxCodecScript))
		return nil, false
	}
	if kind == string(codec.KindJS) {
		if strings.TrimSpace(script) == "" {
			badRequest(w, "a JS model needs a payload formatter; use NONE to leave payloads undecoded")
			return nil, false
		}
		if _, err := codec.NewJSWithOptions(script, s.codecOptions()); err != nil {
			// The parser's message goes to the client verbatim: it names the line
			// and the token, which is the difference between a two-minute fix and
			// a bug report about the console.
			badRequest(w, fmt.Sprintf("the payload formatter cannot be compiled: %v", err))
			return nil, false
		}
	}
	return &engine.DeviceModel{
		ModelID:     id,
		Name:        name,
		Description: strings.TrimSpace(req.Description),
		CodecKind:   kind,
		CodecScript: script,
	}, true
}

// checkModelReference rejects a device that points at a model which does not
// exist. The database would refuse it with a foreign-key violation, which arrives
// as an opaque 500; checking here names the model the operator mistyped.
//
// An empty modelID is always allowed: no model is the normal state for a device
// nobody has described yet.
func (s *apiServer) checkModelReference(ctx context.Context, modelID string) string {
	if modelID == "" {
		return ""
	}
	if s.models == nil {
		return "device models are not available with this database configuration"
	}
	if _, err := s.models.ModelByID(ctx, modelID); err != nil {
		if errors.Is(err, engine.ErrNotFound) {
			return fmt.Sprintf("no device model named %q; create it first or leave model_id empty", modelID)
		}
		return "the device model could not be read"
	}
	return ""
}

// invalidateModel tells the live decoder an edit happened, so the next frame uses
// the new script instead of the cached one.
func (s *apiServer) invalidateModel(id string) {
	if s.invalidate == nil {
		return
	}
	s.invalidate(id)
}

func (s *apiServer) codecOptions() codec.Options { return s.codecOpts }

func (s *apiServer) decodeDeadline() time.Duration {
	if s.decodeTimeout > 0 {
		return s.decodeTimeout
	}
	return 2 * time.Second
}

// decodeModelRequest is a test-decode request: the bytes a formatter should be
// shown, in the shape an operator actually has them.
type decodeModelRequest struct {
	// Payload is the decrypted FRMPayload as hex, which is what a vendor's
	// datasheet prints and what the console can copy out of a stored uplink.
	Payload string `json:"payload"`
	FPort   int    `json:"f_port"`
	// DevEUI is optional, for formatters that branch per device revision.
	DevEUI string `json:"dev_eui,omitempty"`
	// Script tests an unsaved formatter: what is in the editor right now, before
	// it reaches a fleet. Empty means use the stored model's.
	Script string `json:"script,omitempty"`
}

// handleTestModelDecode runs a formatter against bytes the operator supplies.
//
// It compiles fresh rather than using the live cache, so what gets tested is
// exactly what is in front of them, including a script that has never been saved.
// Nothing is stored and no device is touched, which is what makes it safe to offer
// against a production model.
func (s *apiServer) handleTestModelDecode(w http.ResponseWriter, r *http.Request) {
	if s.models == nil {
		s.noModelsStore(w)
		return
	}
	id := r.PathValue("model_id")
	var req decodeModelRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, int64(maxCodecScript)+4096)).Decode(&req); err != nil {
		badRequest(w, "the request body is not valid JSON")
		return
	}
	raw, err := hex.DecodeString(strings.TrimSpace(req.Payload))
	if err != nil {
		badRequest(w, "payload must be the frame's bytes as hex, for example 0A2BFF")
		return
	}
	if len(raw) == 0 {
		badRequest(w, "give the formatter some bytes to decode")
		return
	}
	if req.FPort < 0 || req.FPort > 255 {
		badRequest(w, "f_port must be between 0 and 255")
		return
	}
	if req.DevEUI != "" {
		if _, err := lorawan.ParseEUI64(req.DevEUI); err != nil {
			badRequest(w, "dev_eui must be 16 hexadecimal characters")
			return
		}
	}

	script := strings.TrimSpace(req.Script)
	if script == "" {
		m, err := s.models.ModelByID(r.Context(), id)
		if err != nil {
			s.writeModelError(w, id, err)
			return
		}
		if m.CodecKind != string(codec.KindJS) || strings.TrimSpace(m.CodecScript) == "" {
			badRequest(w, fmt.Sprintf("model %q has no payload formatter to test", id))
			return
		}
		script = m.CodecScript
	}
	if len(script) > maxCodecScript {
		badRequest(w, fmt.Sprintf("a payload formatter must be under %d bytes", maxCodecScript))
		return
	}

	dec, err := codec.NewJSWithOptions(script, s.codecOptions())
	if err != nil {
		// A compile failure is a 200 with ok:false: the request was well formed and
		// the answer to "does this script work" is "no, and here is why". A status
		// code would make a console treat it as a transport error.
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "error": fmt.Sprintf("the formatter cannot be compiled: %v", err),
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.decodeDeadline())
	defer cancel()
	start := time.Now()
	decoded, decErr := dec.Decode(ctx, codec.Input{
		Bytes: raw, FPort: req.FPort, ReceivedAt: time.Now(), DevEUI: req.DevEUI,
	})
	elapsed := time.Since(start)
	if decErr != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "error": decErr.Error(), "elapsed_ms": elapsed.Milliseconds(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "data": decoded, "elapsed_ms": elapsed.Milliseconds(),
	})
}

// uplinkView is the read shape of a stored uplink.
type uplinkView struct {
	UplinkID    int64   `json:"uplink_id"`
	ReceivedAt  string  `json:"received_at"`
	DevEUI      string  `json:"dev_eui"`
	DevAddr     string  `json:"dev_addr,omitempty"`
	FCntUp      uint32  `json:"f_cnt_up"`
	FPort       int     `json:"f_port"`
	Data        string  `json:"data,omitempty"`
	RawPayload  string  `json:"raw_payload,omitempty"`
	GatewayID   string  `json:"gateway_id,omitempty"`
	FrequencyHz uint32  `json:"frequency_hz,omitempty"`
	DataRate    int     `json:"data_rate_index"`
	RSSI        float64 `json:"rssi"`
	SNR         float64 `json:"snr"`
	Retrans     bool    `json:"retransmission,omitempty"`

	// Decoded is the stored JSON passed through untouched rather than decoded into
	// a map and re-encoded. The reason is numeric fidelity: a Go round trip turns
	// every JSON number into float64, so a formatter that reported 41.2 as a
	// decimal string and one that reported 41.200000000000005 would render
	// identically, and an integer count would arrive as 7 rather than 7.0.
	//
	// What this does NOT preserve is key order or whitespace: the column is JSONB,
	// which reorders keys and normalises spacing on the way in. Anyone comparing a
	// reading against a vendor tool should compare values, not bytes.
	Decoded     json.RawMessage `json:"decoded,omitempty"`
	DecodeError string          `json:"decode_error,omitempty"`
	// HasCodec tells the console the difference between "nothing to show" and
	// "the decoder rejected this frame", which it cannot infer when both fields
	// above are absent.
	HasCodec bool `json:"has_codec"`
}

func newUplinkView(row pgstore.UplinkRow) uplinkView {
	v := uplinkView{
		UplinkID: row.UplinkID, ReceivedAt: row.ReceivedAt.UTC().Format(time.RFC3339Nano),
		DevEUI: row.DevEUI, DevAddr: row.DevAddr, FCntUp: row.FCntUp, FPort: row.FPort,
		Data:        base64.StdEncoding.EncodeToString(row.FRMPayload),
		RawPayload:  hex.EncodeToString(row.RawPayload),
		GatewayID:   row.GatewayID,
		FrequencyHz: row.FrequencyHz, DataRate: row.DataRate,
		RSSI: row.RSSI, SNR: row.SNR, Retrans: row.Retrans,
		DecodeError: row.DecodeError,
	}
	if len(row.Decoded) > 0 {
		v.Decoded = json.RawMessage(row.Decoded)
	}
	v.HasCodec = len(row.Decoded) > 0 || row.DecodeError != ""
	return v
}

// handleListUplinks returns recent uplinks with their decoded readings, which is
// what makes a device page more than a list of hex strings.
func (s *apiServer) handleListUplinks(w http.ResponseWriter, r *http.Request) {
	if s.traffic == nil {
		writeError(w, http.StatusServiceUnavailable,
			"uplink history is not available with this database configuration")
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			badRequest(w, "limit must be a positive number")
			return
		}
		limit = n
	}
	devEUI := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("dev_eui")))
	if devEUI != "" {
		if _, err := lorawan.ParseEUI64(devEUI); err != nil {
			badRequest(w, "dev_eui must be 16 hexadecimal characters")
			return
		}
	}

	rows, err := s.traffic.RecentUplinks(r.Context(), devEUI, limit)
	if err != nil {
		s.log.Error("cannot read recent uplinks", "err", err)
		writeError(w, http.StatusInternalServerError, msgUplinkListFailed)
		return
	}
	views := make([]uplinkView, 0, len(rows))
	for _, row := range rows {
		views = append(views, newUplinkView(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"uplinks": views})
}
