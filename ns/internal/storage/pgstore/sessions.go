package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
)

const sessionColumns = `dev_eui, dev_addr, nwk_s_key, app_s_key, key_epoch,
	f_cnt_up, f_cnt_down, a_f_cnt_down,
	region, data_rate, tx_power, nb_trans, rx1_delay, rx1_dr_offset,
	rx2_data_rate, rx2_frequency_hz,
	awaiting_link_adr_ans, pending_adr, pending_rx2, pending_downlink,
	has_device_status, battery, margin,
	last_uplink_at, last_downlink_at, last_rssi, last_snr,
	COALESCE(last_gateway_id, ''), last_frequency_hz, last_data_rate_index,
	mac_state`

// SessionByDevEUI implements engine.SessionRepo.
func (s *Store) SessionByDevEUI(ctx context.Context, devEUI lorawan.EUI64) (*engine.Session, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+sessionColumns+` FROM ns_sessions WHERE dev_eui = $1`, euiText(devEUI))
	return scanSession(row)
}

// SessionByDevAddr implements engine.SessionRepo. This is the hot path of every
// data uplink and is indexed.
func (s *Store) SessionByDevAddr(ctx context.Context, devAddr lorawan.DevAddr) (*engine.Session, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+sessionColumns+` FROM ns_sessions WHERE dev_addr = $1`, devAddrText(devAddr))
	return scanSession(row)
}

func scanSession(row pgx.Row) (*engine.Session, error) {
	var (
		ses             engine.Session
		devEUI, devAddr string
		nwkSKey         string
		appSKey         string
		gatewayID       string
		keyEpoch        int64
		fCntUp          int64
		fCntDown        int64
		afCntDown       int64
		rx2FrequencyHz  int64
		lastFreqHz      int64
		battery         int16
		margin          int16
		lastUplinkAt    *time.Time
		lastDownlinkAt  *time.Time
		pendingADR      []byte
		pendingRX2      []byte
		pendingDownlink []byte
		macStateRaw     []byte
		err             error
	)

	err = row.Scan(
		&devEUI, &devAddr, &nwkSKey, &appSKey, &keyEpoch,
		&fCntUp, &fCntDown, &afCntDown,
		&ses.Region, &ses.DataRate, &ses.TxPower, &ses.NbTrans, &ses.Rx1Delay, &ses.Rx1DROffset,
		&ses.Rx2DataRate, &rx2FrequencyHz,
		&ses.AwaitingLinkADRAns, &pendingADR, &pendingRX2, &pendingDownlink,
		&ses.HasDeviceStatus, &battery, &margin,
		&lastUplinkAt, &lastDownlinkAt, &ses.LastRSSI, &ses.LastSNR,
		&gatewayID, &lastFreqHz, &ses.LastDataRateIndex, &macStateRaw,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, engine.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("pgstore: session query: %w", err)
	}

	if ses.DevEUI, err = parseEUI(devEUI); err != nil {
		return nil, err
	}
	if ses.DevAddr, err = parseDevAddr(devAddr); err != nil {
		return nil, err
	}
	if ses.NwkSKey, err = parseKey(nwkSKey); err != nil {
		return nil, err
	}
	if ses.AppSKey, err = parseKey(appSKey); err != nil {
		return nil, err
	}
	if gatewayID != "" {
		if ses.LastGatewayID, err = parseEUI(gatewayID); err != nil {
			return nil, err
		}
	}
	if len(pendingADR) > 0 {
		ses.PendingADR = &engine.ADRRequest{}
		if err := json.Unmarshal(pendingADR, ses.PendingADR); err != nil {
			return nil, fmt.Errorf("pgstore: pending_adr: %w", err)
		}
	}
	if len(pendingRX2) > 0 {
		ses.PendingRX2 = &engine.RX2Request{}
		if err := json.Unmarshal(pendingRX2, ses.PendingRX2); err != nil {
			return nil, fmt.Errorf("pgstore: pending_rx2: %w", err)
		}
	}
	if len(pendingDownlink) > 0 {
		ses.Pending = &engine.PendingDownlink{}
		if err := json.Unmarshal(pendingDownlink, ses.Pending); err != nil {
			return nil, fmt.Errorf("pgstore: pending_downlink: %w", err)
		}
	}

	if err := decodeMACState(macStateRaw, &ses); err != nil {
		return nil, err
	}

	ses.KeyEpoch = uint32(keyEpoch)
	ses.FCntUp = uint32(fCntUp)
	ses.FCntDown = uint32(fCntDown)
	ses.AFCntDown = uint32(afCntDown)
	ses.Rx2FrequencyHz = uint32(rx2FrequencyHz)
	ses.LastFrequencyHz = uint32(lastFreqHz)
	ses.Battery = byte(battery)
	ses.Margin = int8(margin)
	if lastUplinkAt != nil {
		ses.LastUplinkAt = *lastUplinkAt
	}
	if lastDownlinkAt != nil {
		ses.LastDownlinkAt = *lastDownlinkAt
	}
	return &ses, nil
}

// SaveSession implements engine.SessionRepo.
func (s *Store) SaveSession(ctx context.Context, ses *engine.Session) error {
	pendingADR, err := optionalJSON(ses.PendingADR)
	if err != nil {
		return fmt.Errorf("pgstore: pending_adr: %w", err)
	}
	pendingRX2, err := optionalJSON(ses.PendingRX2)
	if err != nil {
		return fmt.Errorf("pgstore: pending_rx2: %w", err)
	}
	pendingDownlink, err := optionalJSON(ses.Pending)
	if err != nil {
		return fmt.Errorf("pgstore: pending_downlink: %w", err)
	}

	macState, err := encodeMACState(ses)
	if err != nil {
		return err
	}

	var lastUplinkAt, lastDownlinkAt any
	if !ses.LastUplinkAt.IsZero() {
		lastUplinkAt = ses.LastUplinkAt
	}
	if !ses.LastDownlinkAt.IsZero() {
		lastDownlinkAt = ses.LastDownlinkAt
	}
	var gatewayID any
	if ses.LastGatewayID != (lorawan.EUI64{}) {
		gatewayID = euiText(ses.LastGatewayID)
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO ns_sessions (
			dev_eui, dev_addr, nwk_s_key, app_s_key, key_epoch,
			f_cnt_up, f_cnt_down, a_f_cnt_down,
			region, data_rate, tx_power, nb_trans, rx1_delay, rx1_dr_offset,
			rx2_data_rate, rx2_frequency_hz,
			awaiting_link_adr_ans, pending_adr, pending_rx2, pending_downlink,
			has_device_status, battery, margin,
			last_uplink_at, last_downlink_at, last_rssi, last_snr,
			last_gateway_id, last_frequency_hz, last_data_rate_index, mac_state, updated_at)
		VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16,
			$17, $18::jsonb, $19::jsonb, $20::jsonb, $21, $22, $23, $24, $25, $26,
			$27, $28, $29, $30, $31::jsonb, now())
		ON CONFLICT (dev_eui) DO UPDATE SET
			dev_addr              = EXCLUDED.dev_addr,
			nwk_s_key             = EXCLUDED.nwk_s_key,
			app_s_key             = EXCLUDED.app_s_key,
			key_epoch             = EXCLUDED.key_epoch,
			f_cnt_up              = EXCLUDED.f_cnt_up,
			f_cnt_down            = EXCLUDED.f_cnt_down,
			a_f_cnt_down          = EXCLUDED.a_f_cnt_down,
			region                = EXCLUDED.region,
			data_rate             = EXCLUDED.data_rate,
			tx_power              = EXCLUDED.tx_power,
			nb_trans              = EXCLUDED.nb_trans,
			rx1_delay             = EXCLUDED.rx1_delay,
			rx1_dr_offset         = EXCLUDED.rx1_dr_offset,
			rx2_data_rate         = EXCLUDED.rx2_data_rate,
			rx2_frequency_hz      = EXCLUDED.rx2_frequency_hz,
			awaiting_link_adr_ans = EXCLUDED.awaiting_link_adr_ans,
			pending_adr           = EXCLUDED.pending_adr,
			pending_rx2           = EXCLUDED.pending_rx2,
			pending_downlink      = EXCLUDED.pending_downlink,
			has_device_status     = EXCLUDED.has_device_status,
			battery               = EXCLUDED.battery,
			margin                = EXCLUDED.margin,
			last_uplink_at        = EXCLUDED.last_uplink_at,
			last_downlink_at      = EXCLUDED.last_downlink_at,
			last_rssi             = EXCLUDED.last_rssi,
			last_snr              = EXCLUDED.last_snr,
			last_gateway_id       = EXCLUDED.last_gateway_id,
			last_frequency_hz     = EXCLUDED.last_frequency_hz,
			last_data_rate_index  = EXCLUDED.last_data_rate_index,
			mac_state             = EXCLUDED.mac_state,
			updated_at            = now()`,
		euiText(ses.DevEUI), devAddrText(ses.DevAddr), keyText(ses.NwkSKey), keyText(ses.AppSKey),
		int64(ses.KeyEpoch), int64(ses.FCntUp), int64(ses.FCntDown), int64(ses.AFCntDown),
		ses.Region, ses.DataRate, ses.TxPower, ses.NbTrans, ses.Rx1Delay, ses.Rx1DROffset,
		ses.Rx2DataRate, int64(ses.Rx2FrequencyHz),
		ses.AwaitingLinkADRAns, pendingADR, pendingRX2, pendingDownlink,
		ses.HasDeviceStatus, int16(ses.Battery), int16(ses.Margin),
		lastUplinkAt, lastDownlinkAt, ses.LastRSSI, ses.LastSNR,
		gatewayID, int64(ses.LastFrequencyHz), ses.LastDataRateIndex, macState,
	)
	if err != nil {
		return fmt.Errorf("pgstore: save session: %w", err)
	}
	return nil
}

// DeleteSession implements engine.SessionRepo.
func (s *Store) DeleteSession(ctx context.Context, devEUI lorawan.EUI64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM ns_sessions WHERE dev_eui = $1`, euiText(devEUI))
	if err != nil {
		return fmt.Errorf("pgstore: delete session: %w", err)
	}
	return nil
}

// optionalJSON marshals an outstanding-request pointer into a JSONB parameter,
// mapping a nil pointer to SQL NULL. The typed switch keeps the set of
// persistable pending types explicit instead of accepting any value.
func optionalJSON(v any) (any, error) {
	switch t := v.(type) {
	case nil:
		return nil, nil
	case *engine.ADRRequest:
		if t == nil {
			return nil, nil
		}
	case *engine.RX2Request:
		if t == nil {
			return nil, nil
		}
	case *engine.PendingDownlink:
		if t == nil {
			return nil, nil
		}
	default:
		return nil, fmt.Errorf("pgstore: cannot persist %T", v)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}
