package pgstore_test

import (
	"encoding/json"
	"testing"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
)

// testKeyStr is the AppKey used by the device rows these tests create.
const testKeyStr = "2b7e151628aed2a6abf7158809cf4f3c"

// TestModelRoundTrip exercises the device-model table for real: the slug CHECK,
// the codec-kind CHECK, the foreign key from ns_devices, and the ON DELETE SET
// NULL that decides what happens to a fleet when a model disappears.
func TestModelRoundTrip(t *testing.T) {
	ctx, store := openStore(t)

	const modelID = "cold-chain-probe"
	devEUI := mustEUI(t, "0f0e0d0c0b0a0908")
	t.Cleanup(func() {
		_, _ = store.Pool().Exec(ctx, `DELETE FROM ns_devices WHERE dev_eui = $1`, devEUI.String())
		_, _ = store.Pool().Exec(ctx, `DELETE FROM ns_device_models WHERE model_id = $1`, modelID)
	})

	m := &engine.DeviceModel{
		ModelID: modelID, Name: "Cold-chain probe v2",
		Description: "DS18B20 on port 10",
		CodecKind:   "JS",
		CodecScript: "function decodeUplink(input) { return { data: {} }; }",
	}
	if err := store.SaveModel(ctx, m); err != nil {
		t.Fatalf("SaveModel: %v", err)
	}

	got, err := store.ModelByID(ctx, modelID)
	if err != nil {
		t.Fatalf("ModelByID: %v", err)
	}
	if got.Name != m.Name || got.CodecKind != "JS" || got.CodecScript != m.CodecScript {
		t.Errorf("round trip = %+v", got)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Error("the timestamps must come back from the database")
	}

	// Update in place. The new script is held in a separate variable: comparing
	// against the mutated struct would compare a field with itself and could never
	// fail, which is the kind of assertion that looks like coverage and is not.
	const updatedScript = "function decodeUplink(i) { return { data: {x:1} }; }"
	m.Name = "Renamed"
	m.CodecScript = updatedScript
	if err := store.SaveModel(ctx, m); err != nil {
		t.Fatalf("SaveModel (update): %v", err)
	}
	got, _ = store.ModelByID(ctx, modelID)
	if got.Name != "Renamed" {
		t.Errorf("name after update = %q, want Renamed", got.Name)
	}
	if got.CodecScript != updatedScript {
		t.Errorf("script after update = %q, want the new one", got.CodecScript)
	}

	list, err := store.ListModels(ctx)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	var found bool
	for _, item := range list {
		if item.ModelID == modelID {
			found = true
		}
	}
	if !found {
		t.Errorf("ListModels did not include %q", modelID)
	}

	// A device can reference it, and the count says so.
	dev := engine.NewOTAADevice(devEUI, mustEUI(t, "0102030405060708"), mustKey(t, testKeyStr))
	dev.ModelID = modelID
	if err := store.SaveDevice(ctx, dev); err != nil {
		t.Fatalf("SaveDevice with a model: %v", err)
	}
	stored, err := store.DeviceByDevEUI(ctx, devEUI)
	if err != nil {
		t.Fatalf("DeviceByDevEUI: %v", err)
	}
	if stored.ModelID != modelID {
		t.Errorf("device model_id = %q, want %q", stored.ModelID, modelID)
	}
	n, err := store.DevicesOnModel(ctx, modelID)
	if err != nil {
		t.Fatalf("DevicesOnModel: %v", err)
	}
	if n != 1 {
		t.Errorf("DevicesOnModel = %d, want 1", n)
	}

	// Deleting the model must keep the device and drop the association. That is
	// the difference between "the sensors go quiet" and "the sensors stop being
	// decoded", and the schema is what decides it.
	if err := store.DeleteModel(ctx, modelID); err != nil {
		t.Fatalf("DeleteModel: %v", err)
	}
	if _, err := store.ModelByID(ctx, modelID); err != engine.ErrNotFound {
		t.Errorf("ModelByID after delete = %v, want ErrNotFound", err)
	}
	stored, err = store.DeviceByDevEUI(ctx, devEUI)
	if err != nil {
		t.Fatalf("the device should survive its model: %v", err)
	}
	if stored.ModelID != "" {
		t.Errorf("the device still references a deleted model: %q", stored.ModelID)
	}

	if err := store.DeleteModel(ctx, modelID); err != engine.ErrNotFound {
		t.Errorf("deleting twice = %v, want ErrNotFound the second time", err)
	}
}

// TestModelSchemaRejectsNonsense pins the column constraints, because the API's
// validation and the schema's must agree or one of them is decorative.
func TestModelSchemaRejectsNonsense(t *testing.T) {
	ctx, store := openStore(t)

	cases := []struct {
		name  string
		id    string
		kind  string
		valid bool
	}{
		{"a slug is fine", "probe_v2-x", "JS", true},
		{"one character is too short", "x", "NONE", false},
		{"upper case is not a slug", "Probe", "NONE", false},
		{"a leading dash is not a slug", "-probe", "NONE", false},
		{"a space is not a slug", "probe v2", "NONE", false},
		{"CAYENNE_LPP is not a codec we ship", "probe-v2", "CAYENNE_LPP", false},
		{"an empty codec kind is not a codec", "probe-v2", "", false},
	}
	for _, tc := range cases {
		_, err := store.Pool().Exec(ctx, `
			INSERT INTO ns_device_models (model_id, name, codec_kind) VALUES ($1, 'Any', $2)`,
			tc.id, tc.kind)
		switch {
		case tc.valid && err != nil:
			t.Errorf("%s: the schema rejected a legal row: %v", tc.name, err)
		case !tc.valid && err == nil:
			t.Errorf("%s: the schema accepted it", tc.name)
		}
		if err == nil {
			_, _ = store.Pool().Exec(ctx,
				`DELETE FROM ns_device_models WHERE model_id = $1`, tc.id)
		}
	}

	// A model cannot be named after nothing.
	if _, err := store.Pool().Exec(ctx,
		`INSERT INTO ns_device_models (model_id, name, codec_kind) VALUES ('probe-x', '', 'NONE')`); err == nil {
		t.Error("the schema accepted a model with no name")
		_, _ = store.Pool().Exec(ctx, `DELETE FROM ns_device_models WHERE model_id = 'probe-x'`)
	}
}

// TestSaveModelRejectsJSWithoutAScript is the store's share of "a half-configured
// formatter should be visible", so the check exists at both layers.
func TestSaveModelRejectsJSWithoutAScript(t *testing.T) {
	ctx, store := openStore(t)
	err := store.SaveModel(ctx, &engine.DeviceModel{
		ModelID: "half-configured", Name: "Half", CodecKind: "JS",
	})
	if err == nil {
		t.Fatal("a JS model with no script must be refused")
		_, _ = store.Pool().Exec(ctx, `DELETE FROM ns_device_models WHERE model_id = 'half-configured'`)
	}
}

// TestDecodedUplinkRoundTrip covers the two decoded columns and the separate
// write that fills them, which is how a formatter is kept off the radio path.
func TestDecodedUplinkRoundTrip(t *testing.T) {
	ctx, store := openStore(t)

	devEUI := mustEUI(t, "0f0e0d0c0b0a0707")
	t.Cleanup(func() {
		_, _ = store.Pool().Exec(ctx, `DELETE FROM ns_uplinks WHERE dev_eui = $1`, devEUI.String())
		_, _ = store.Pool().Exec(ctx, `DELETE FROM ns_devices WHERE dev_eui = $1`, devEUI.String())
	})
	dev := engine.NewOTAADevice(devEUI, mustEUI(t, "0102030405060708"), mustKey(t, testKeyStr))
	if err := store.SaveDevice(ctx, dev); err != nil {
		t.Fatalf("SaveDevice: %v", err)
	}

	rec := engine.UplinkRecord{
		DevEUI: devEUI, DevAddr: 0x01020304, FCntUp: 11, FPort: 10,
		FRMPayload: []byte{0x64, 0x00}, RawPHYPayload: []byte{0x40, 0x01},
		GatewayID: mustEUI(t, "aa55555555555555"), FrequencyHz: 868100000,
		DataRateIndex: 5, RSSI: -71.5, SNR: 9.2,
	}
	// Written first, with no reading: the frame is durable before any vendor code
	// has run.
	if err := store.RecordUplink(ctx, rec); err != nil {
		t.Fatalf("RecordUplink: %v", err)
	}
	rows, err := store.RecentUplinks(ctx, devEUI.String(), 10)
	if err != nil {
		t.Fatalf("RecentUplinks: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("RecentUplinks returned %d rows, want 1", len(rows))
	}
	if len(rows[0].Decoded) != 0 || rows[0].DecodeError != "" {
		t.Errorf("a freshly written uplink should carry no decode outcome: %+v", rows[0])
	}

	// Then the formatter's reading arrives.
	if err := store.UpdateUplinkDecoded(ctx, devEUI, rec.KeyEpoch, rec.FCntUp,
		[]byte(`{"temperature":10.0}`), ""); err != nil {
		t.Fatalf("UpdateUplinkDecoded: %v", err)
	}
	rows, _ = store.RecentUplinks(ctx, devEUI.String(), 10)
	var back map[string]any
	if err := json.Unmarshal(rows[0].Decoded, &back); err != nil {
		t.Fatalf("the stored JSON is not parseable: %s (%v)", rows[0].Decoded, err)
	}
	if back["temperature"] != 10.0 {
		t.Errorf("decoded values = %v, want temperature 10.0", back)
	}
	// The raw payload is untouched: the reading is a projection, not a rewrite.
	if len(rows[0].FRMPayload) != 2 || rows[0].FRMPayload[0] != 0x64 {
		t.Errorf("the raw payload changed: %x", rows[0].FRMPayload)
	}

	// A failure on a different frame, and the three states stay distinguishable.
	rec.FCntUp = 12
	if err := store.RecordUplink(ctx, rec); err != nil {
		t.Fatalf("RecordUplink (second frame): %v", err)
	}
	if err := store.UpdateUplinkDecoded(ctx, devEUI, rec.KeyEpoch, rec.FCntUp,
		nil, "codec: payload formatter reported: bad frame"); err != nil {
		t.Fatalf("UpdateUplinkDecoded (failure): %v", err)
	}
	rows, _ = store.RecentUplinks(ctx, devEUI.String(), 10)
	if len(rows) != 2 {
		t.Fatalf("RecentUplinks returned %d rows, want 2", len(rows))
	}
	// Newest first.
	failed, decoded := rows[0], rows[1]
	if failed.FCntUp != 12 || failed.DecodeError == "" || len(failed.Decoded) != 0 {
		t.Errorf("the failed frame = %+v", failed)
	}
	if decoded.FCntUp != 11 || decoded.DecodeError != "" || len(decoded.Decoded) == 0 {
		t.Errorf("the decoded frame = %+v", decoded)
	}

	// And the device filter works, including the empty filter meaning "all".
	only, err := store.RecentUplinks(ctx, devEUI.String(), 1)
	if err != nil {
		t.Fatalf("RecentUplinks with a limit: %v", err)
	}
	if len(only) != 1 {
		t.Errorf("limit 1 returned %d rows", len(only))
	}
	all, err := store.RecentUplinks(ctx, "", 100)
	if err != nil {
		t.Fatalf("RecentUplinks without a filter: %v", err)
	}
	if len(all) < 2 {
		t.Errorf("an empty filter returned %d rows, want at least 2", len(all))
	}
}

// TestUpdateUplinkDecodedRefusesAnEmptyOutcome: writing neither a reading nor an
// error would clear a previous one and record nothing, which is what a caller
// that ignored the formatter's error would do.
func TestUpdateUplinkDecodedRefusesAnEmptyOutcome(t *testing.T) {
	ctx, store := openStore(t)
	err := store.UpdateUplinkDecoded(ctx, mustEUI(t, "0000000000000000"), 0, 0, nil, "")
	if err == nil {
		t.Error("an outcome with nothing in it must be refused")
	}
}

// TestUplinkDecodeOutcomeConstraint pins the schema rule that a row cannot both
// claim a reading and report a failure, which is what makes the console's three
// states trustworthy.
func TestUplinkDecodeOutcomeConstraint(t *testing.T) {
	ctx, store := openStore(t)

	devEUI := mustEUI(t, "0f0e0d0c0b0a0606")
	t.Cleanup(func() {
		_, _ = store.Pool().Exec(ctx, `DELETE FROM ns_uplinks WHERE dev_eui = $1`, devEUI.String())
		_, _ = store.Pool().Exec(ctx, `DELETE FROM ns_devices WHERE dev_eui = $1`, devEUI.String())
	})
	dev := engine.NewOTAADevice(devEUI, mustEUI(t, "0102030405060708"), mustKey(t, testKeyStr))
	if err := store.SaveDevice(ctx, dev); err != nil {
		t.Fatalf("SaveDevice: %v", err)
	}
	if err := store.RecordUplink(ctx, engine.UplinkRecord{
		DevEUI: devEUI, FCntUp: 3, FPort: 10, FRMPayload: []byte{1},
		RawPHYPayload: []byte{0x40, 0x01},
	}); err != nil {
		t.Fatalf("RecordUplink: %v", err)
	}

	_, err := store.Pool().Exec(ctx, `
		UPDATE ns_uplinks SET decoded = '{}'::jsonb, decode_error = 'also failed'
		WHERE dev_eui = $1 AND f_cnt_up = 3`, devEUI.String())
	if err == nil {
		t.Error("the schema accepted a row that both decoded and failed")
	}
}
