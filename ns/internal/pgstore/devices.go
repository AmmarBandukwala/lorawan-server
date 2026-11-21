package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
)

const deviceColumns = `dev_eui, join_eui, name, enabled, activation_mode, app_key,
	COALESCE(dev_addr, ''), nwk_s_key, app_s_key, next_join_nonce, used_dev_nonces,
	max_data_rate, min_data_rate, max_tx_power, rx1_delay, rx2_data_rate,
	rx2_frequency_hz, supports_class_c, disable_adr, device_group,
	COALESCE(model_id, ''), attributes, latitude, longitude, altitude`

// DeviceByDevEUI implements engine.DeviceRepo.
func (s *Store) DeviceByDevEUI(ctx context.Context, devEUI lorawan.EUI64) (*engine.Device, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+deviceColumns+` FROM ns_devices WHERE dev_eui = $1`, euiText(devEUI))
	return scanDevice(row)
}

// DeviceByDevAddr looks up a device by its current address, for ABP devices
// that never join and for operator tooling.
func (s *Store) DeviceByDevAddr(ctx context.Context, devAddr lorawan.DevAddr) (*engine.Device, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+deviceColumns+` FROM ns_devices WHERE dev_addr = $1`, devAddrText(devAddr))
	return scanDevice(row)
}

func scanDevice(row pgx.Row) (*engine.Device, error) {
	var (
		device           engine.Device
		devEUI, joinEUI  string
		activationMode   string
		appKey, nwkSKey  string
		appSKey, devAddr string
		nonces           []byte
		attrs            []byte
		rx2FrequencyHz   int64
		nextJoinNonce    int64
		latitude         *float64
		longitude        *float64
		altitude         *int32
		err              error
	)

	err = row.Scan(
		&devEUI, &joinEUI, &device.Name, &device.Enabled, &activationMode, &appKey,
		&devAddr, &nwkSKey, &appSKey, &nextJoinNonce, &nonces,
		&device.MaxDataRate, &device.MinDataRate, &device.MaxTxPower, &device.Rx1Delay, &device.Rx2DataRate,
		&rx2FrequencyHz, &device.SupportsClassC, &device.DisableADR, &device.Group, &device.ModelID,
		&attrs, &latitude, &longitude, &altitude,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, engine.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("pgstore: device query: %w", err)
	}

	if device.DevEUI, err = parseEUI(devEUI); err != nil {
		return nil, err
	}
	if device.JoinEUI, err = parseEUI(joinEUI); err != nil {
		return nil, err
	}
	if device.AppKey, err = parseKey(appKey); err != nil {
		return nil, err
	}
	if device.NwkSKey, err = parseKey(nwkSKey); err != nil {
		return nil, err
	}
	if device.AppSKey, err = parseKey(appSKey); err != nil {
		return nil, err
	}
	if device.DevAddr, err = parseDevAddr(devAddr); err != nil {
		return nil, err
	}
	if device.UsedDevNonces, err = devNoncesFromJSON(nonces); err != nil {
		return nil, err
	}
	if device.Attributes, err = attributesFromJSON(attrs); err != nil {
		return nil, err
	}
	// A position is only assembled when both coordinates are present. The
	// migration permits a latitude without an altitude but not a latitude
	// without a longitude; reading a half-written row as a position would place
	// a device somewhere it is not.
	if latitude != nil && longitude != nil {
		loc := engine.Location{Latitude: *latitude, Longitude: *longitude}
		if altitude != nil {
			loc.Altitude = int(*altitude)
		}
		device.Location = &loc
	}
	device.OTAA = activationMode == "OTAA"
	device.NextJoinNonce = uint32(nextJoinNonce)
	device.Rx2FrequencyHz = uint32(rx2FrequencyHz)
	return &device, nil
}

// attributesFromJSON decodes the attributes column. An empty object becomes nil
// rather than an empty map, so "no attributes" has one representation and the API
// can omit the field instead of emitting `{}` for every device.
func attributesFromJSON(raw []byte) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var out map[string]string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("pgstore: device attributes: %w", err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// attributesJSON encodes the attributes column. nil becomes an empty object,
// which is what the column's default and CHECK expect.
func attributesJSON(attrs map[string]string) (string, error) {
	if len(attrs) == 0 {
		return "{}", nil
	}
	raw, err := json.Marshal(attrs)
	if err != nil {
		return "", fmt.Errorf("pgstore: encode device attributes: %w", err)
	}
	return string(raw), nil
}

// locationArgs returns the three position columns for a device, with nil meaning
// SQL NULL. Altitude is stored NULL when there is no position at all, so a row
// never carries an altitude that belongs to no place.
func locationArgs(loc *engine.Location) (lat, long, alt any) {
	if loc == nil {
		return nil, nil, nil
	}
	return loc.Latitude, loc.Longitude, loc.Altitude
}

// SaveDevice implements engine.DeviceRepo, inserting or fully updating the row.
func (s *Store) SaveDevice(ctx context.Context, device *engine.Device) error {
	nonces, err := devNoncesJSON(device.UsedDevNonces)
	if err != nil {
		return err
	}

	activationMode := "ABP"
	if device.OTAA {
		activationMode = "OTAA"
	}
	attrs, err := attributesJSON(device.Attributes)
	if err != nil {
		return err
	}
	lat, long, alt := locationArgs(device.Location)
	// An OTAA device that has not joined yet has no address; storing NULL keeps
	// the UNIQUE constraint meaningful (several unjoined devices must not all
	// claim "00000000").
	var devAddr any
	if !device.OTAA || device.DevAddr != 0 {
		devAddr = devAddrText(device.DevAddr)
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO ns_devices (
			dev_eui, join_eui, name, enabled, activation_mode, app_key,
			dev_addr, nwk_s_key, app_s_key, next_join_nonce, used_dev_nonces,
			max_data_rate, min_data_rate, max_tx_power, rx1_delay, rx2_data_rate,
			rx2_frequency_hz, supports_class_c, disable_adr, device_group,
			attributes, latitude, longitude, altitude, model_id, updated_at)
		VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::jsonb,
			$12, $13, $14, $15, $16, $17, $18, $19, $20,
			$21::jsonb, $22, $23, $24, NULLIF($25, ''), now())
		ON CONFLICT (dev_eui) DO UPDATE SET
			join_eui          = EXCLUDED.join_eui,
			name              = EXCLUDED.name,
			enabled           = EXCLUDED.enabled,
			activation_mode   = EXCLUDED.activation_mode,
			app_key           = EXCLUDED.app_key,
			dev_addr          = EXCLUDED.dev_addr,
			nwk_s_key         = EXCLUDED.nwk_s_key,
			app_s_key         = EXCLUDED.app_s_key,
			next_join_nonce   = EXCLUDED.next_join_nonce,
			used_dev_nonces   = EXCLUDED.used_dev_nonces,
			max_data_rate     = EXCLUDED.max_data_rate,
			min_data_rate     = EXCLUDED.min_data_rate,
			max_tx_power      = EXCLUDED.max_tx_power,
			rx1_delay         = EXCLUDED.rx1_delay,
			rx2_data_rate     = EXCLUDED.rx2_data_rate,
			rx2_frequency_hz  = EXCLUDED.rx2_frequency_hz,
			supports_class_c  = EXCLUDED.supports_class_c,
			disable_adr       = EXCLUDED.disable_adr,
			device_group      = EXCLUDED.device_group,
			attributes        = EXCLUDED.attributes,
			latitude          = EXCLUDED.latitude,
			longitude         = EXCLUDED.longitude,
			altitude          = EXCLUDED.altitude,
			model_id          = EXCLUDED.model_id,
			updated_at        = now()`,
		euiText(device.DevEUI), euiText(device.JoinEUI), device.Name, device.Enabled, activationMode, keyText(device.AppKey),
		devAddr, keyText(device.NwkSKey), keyText(device.AppSKey), int64(device.NextJoinNonce), string(nonces),
		device.MaxDataRate, device.MinDataRate, device.MaxTxPower, device.Rx1Delay, device.Rx2DataRate,
		int64(device.Rx2FrequencyHz), device.SupportsClassC, device.DisableADR, device.Group,
		attrs, lat, long, alt, device.ModelID,
	)
	if err != nil {
		return fmt.Errorf("pgstore: save device: %w", err)
	}
	return nil
}

// ListDevices implements engine.DeviceRepo. Provisioning screens and the CLI
// need the whole inventory, and a deployment of this size is hundreds of devices,
// not millions — so an ordered full read beats pagination complexity here. When
// that stops being true, this is the place to add a limit and a cursor.
func (s *Store) ListDevices(ctx context.Context) ([]*engine.Device, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+deviceColumns+` FROM ns_devices ORDER BY dev_eui`)
	if err != nil {
		return nil, fmt.Errorf("pgstore: list devices: %w", err)
	}
	defer rows.Close()

	var out []*engine.Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pgstore: list devices: %w", err)
	}
	return out, nil
}

// DeleteDevice implements engine.DeviceRepo.
//
// The session goes with it, in the same transaction. ns_sessions references
// ns_devices, so leaving the row would fail the delete — but even if the schema
// allowed it, keeping a live session after the operator revoked a device would
// let that device keep exchanging traffic with keys it should no longer have.
// Deleting both is the only behaviour that matches what "remove this device"
// means to the person who asked for it.
func (s *Store) DeleteDevice(ctx context.Context, devEUI lorawan.EUI64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pgstore: begin device delete: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM ns_sessions WHERE dev_eui = $1`, euiText(devEUI)); err != nil {
		return fmt.Errorf("pgstore: delete the session of %s: %w", devEUI, err)
	}
	tag, err := tx.Exec(ctx, `DELETE FROM ns_devices WHERE dev_eui = $1`, euiText(devEUI))
	if err != nil {
		return fmt.Errorf("pgstore: delete device %s: %w", devEUI, err)
	}
	if tag.RowsAffected() == 0 {
		return engine.ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("pgstore: commit device delete: %w", err)
	}
	return nil
}
