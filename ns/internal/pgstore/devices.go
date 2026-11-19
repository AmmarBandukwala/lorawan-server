package pgstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
)

const deviceColumns = `dev_eui, join_eui, name, enabled, activation_mode, app_key,
	COALESCE(dev_addr, ''), nwk_s_key, app_s_key, next_join_nonce, used_dev_nonces,
	max_data_rate, min_data_rate, max_tx_power, rx1_delay, rx2_data_rate,
	rx2_frequency_hz, supports_class_c, disable_adr, device_group`

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
		d                engine.Device
		devEUI, joinEUI  string
		activationMode   string
		appKey, nwkSKey  string
		appSKey, devAddr string
		nonces           []byte
		rx2FrequencyHz   int64
		nextJoinNonce    int64
		err              error
	)

	err = row.Scan(
		&devEUI, &joinEUI, &d.Name, &d.Enabled, &activationMode, &appKey,
		&devAddr, &nwkSKey, &appSKey, &nextJoinNonce, &nonces,
		&d.MaxDataRate, &d.MinDataRate, &d.MaxTxPower, &d.Rx1Delay, &d.Rx2DataRate,
		&rx2FrequencyHz, &d.SupportsClassC, &d.DisableADR, &d.Group,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, engine.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("pgstore: device query: %w", err)
	}

	if d.DevEUI, err = parseEUI(devEUI); err != nil {
		return nil, err
	}
	if d.JoinEUI, err = parseEUI(joinEUI); err != nil {
		return nil, err
	}
	if d.AppKey, err = parseKey(appKey); err != nil {
		return nil, err
	}
	if d.NwkSKey, err = parseKey(nwkSKey); err != nil {
		return nil, err
	}
	if d.AppSKey, err = parseKey(appSKey); err != nil {
		return nil, err
	}
	if d.DevAddr, err = parseDevAddr(devAddr); err != nil {
		return nil, err
	}
	if d.UsedDevNonces, err = devNoncesFromJSON(nonces); err != nil {
		return nil, err
	}
	d.OTAA = activationMode == "OTAA"
	d.NextJoinNonce = uint32(nextJoinNonce)
	d.Rx2FrequencyHz = uint32(rx2FrequencyHz)
	return &d, nil
}

// SaveDevice implements engine.DeviceRepo, inserting or fully updating the row.
func (s *Store) SaveDevice(ctx context.Context, d *engine.Device) error {
	nonces, err := devNoncesJSON(d.UsedDevNonces)
	if err != nil {
		return err
	}

	activationMode := "ABP"
	if d.OTAA {
		activationMode = "OTAA"
	}
	// An OTAA device that has not joined yet has no address; storing NULL keeps
	// the UNIQUE constraint meaningful (several unjoined devices must not all
	// claim "00000000").
	var devAddr any
	if !d.OTAA || d.DevAddr != 0 {
		devAddr = devAddrText(d.DevAddr)
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO ns_devices (
			dev_eui, join_eui, name, enabled, activation_mode, app_key,
			dev_addr, nwk_s_key, app_s_key, next_join_nonce, used_dev_nonces,
			max_data_rate, min_data_rate, max_tx_power, rx1_delay, rx2_data_rate,
			rx2_frequency_hz, supports_class_c, disable_adr, device_group, updated_at)
		VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::jsonb,
			$12, $13, $14, $15, $16, $17, $18, $19, $20, now())
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
			updated_at        = now()`,
		euiText(d.DevEUI), euiText(d.JoinEUI), d.Name, d.Enabled, activationMode, keyText(d.AppKey),
		devAddr, keyText(d.NwkSKey), keyText(d.AppSKey), int64(d.NextJoinNonce), string(nonces),
		d.MaxDataRate, d.MinDataRate, d.MaxTxPower, d.Rx1Delay, d.Rx2DataRate,
		int64(d.Rx2FrequencyHz), d.SupportsClassC, d.DisableADR, d.Group,
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
