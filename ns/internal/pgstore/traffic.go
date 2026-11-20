package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
)

// Compile-time proof that *Store satisfies every port the engine needs. Without
// these, a missing method only surfaces where the ports are wired together.
var (
	_ engine.DeviceRepo  = (*Store)(nil)
	_ engine.SessionRepo = (*Store)(nil)
	_ engine.Recorder    = (*Store)(nil)
)

// RecordUplink stores an accepted uplink and every gateway that heard it.
//
// The engine also records *retransmissions* — a device that did not hear our
// answer repeats the same counter — so the natural key legitimately repeats.
// The existing row is refreshed and its retransmission tally bumped instead of
// the insert being rejected.
//
// Both writes are one transaction. A reception set without its uplink, or the
// reverse, would leave the coverage table disagreeing with the traffic table, and
// the disagreement would be invisible until someone trusted the wrong one.
func (s *Store) RecordUplink(ctx context.Context, r engine.UplinkRecord) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pgstore: begin uplink: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var uplinkID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO ns_uplinks (
			received_at, dev_eui, dev_addr, key_epoch, f_cnt_up, f_port,
			frm_payload, raw_phy_payload, gateway_id, frequency_hz,
			data_rate_index, rssi, snr, confirmed, retransmission
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
		ON CONFLICT (dev_eui, key_epoch, f_cnt_up) DO UPDATE SET
			received_at     = EXCLUDED.received_at,
			frm_payload     = EXCLUDED.frm_payload,
			raw_phy_payload = EXCLUDED.raw_phy_payload,
			gateway_id      = EXCLUDED.gateway_id,
			frequency_hz    = EXCLUDED.frequency_hz,
			data_rate_index = EXCLUDED.data_rate_index,
			rssi            = EXCLUDED.rssi,
			snr             = EXCLUDED.snr,
			confirmed       = EXCLUDED.confirmed,
			retransmission  = TRUE,
			retransmissions = ns_uplinks.retransmissions + 1
		RETURNING uplink_id`,
		r.At,
		euiText(r.DevEUI),
		devAddrText(r.DevAddr),
		int64(r.KeyEpoch),
		int64(r.FCntUp),
		int16(r.FPort),
		nullableBytes(r.FRMPayload),
		r.RawPHYPayload,
		nullableEUI(r.GatewayID),
		int64(r.FrequencyHz),
		r.DataRateIndex,
		float32(r.RSSI),
		float32(r.SNR),
		r.Confirmed,
		r.Retransmission,
	).Scan(&uplinkID)
	if err != nil {
		return fmt.Errorf("pgstore: record uplink: %w", err)
	}

	if err := recordReceptions(ctx, tx, uplinkID, r); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// recordReceptions writes one row per gateway that heard the frame.
//
// Re-running it for the same uplink is safe: the key is (uplink, gateway) and the
// measurement is refreshed rather than duplicated, so a retransmission heard by a
// different set of gateways ends up with an accurate picture instead of a
// accumulation of stale ones.
func recordReceptions(ctx context.Context, tx pgx.Tx, uplinkID int64, r engine.UplinkRecord) error {
	for i, rec := range r.Receptions {
		// The engine puts the chosen copy first; anything else that marks a
		// reception as chosen is honoured too, so the flag cannot silently go
		// missing if the ordering convention changes.
		chosen := i == 0
		if _, err := tx.Exec(ctx, `
			INSERT INTO ns_uplink_gateways (
				uplink_id, gateway_id, frequency_hz, data_rate_index,
				rssi, snr, received_at, chosen
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (uplink_id, gateway_id) DO UPDATE SET
				frequency_hz    = EXCLUDED.frequency_hz,
				data_rate_index = EXCLUDED.data_rate_index,
				rssi            = EXCLUDED.rssi,
				snr             = EXCLUDED.snr,
				received_at     = EXCLUDED.received_at,
				chosen          = EXCLUDED.chosen`,
			uplinkID,
			euiText(rec.GatewayID),
			int64(rec.FrequencyHz),
			rec.DataRateIndex,
			float32(rec.RSSI),
			float32(rec.SNR),
			rec.ReceivedAt,
			chosen,
		); err != nil {
			return fmt.Errorf("pgstore: record uplink reception for %s: %w", rec.GatewayID, err)
		}
	}
	return nil
}

// RecordDownlink stores one scheduled transmission. The engine records a row
// per RX window it plans, so a frame sent in both RX1 and RX2 appears twice —
// which is what the radio actually did.
func (s *Store) RecordDownlink(ctx context.Context, r engine.DownlinkRecord) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO ns_downlinks (
			created_at, dev_eui, dev_addr, gateway_id, rx_window, frequency_hz,
			data_rate_index, tx_power_dbm, f_cnt_down, f_port,
			frm_payload, raw_phy_payload, ack
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		r.At,
		euiText(r.DevEUI),
		devAddrText(r.DevAddr),
		nullableEUI(r.GatewayID),
		r.Window,
		int64(r.FrequencyHz),
		r.DataRateIndex,
		int16(r.TxPowerdBm),
		int64(r.FCntDown),
		int16(r.FPort),
		nullableBytes(r.FRMPayload),
		r.RawPHYPayload,
		r.ACK,
	)
	if err != nil {
		return fmt.Errorf("pgstore: record downlink: %w", err)
	}
	return nil
}

// RecordEvent appends a trace entry for the dashboard's event feed.
func (s *Store) RecordEvent(ctx context.Context, ev engine.Event) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO ns_events (
			occurred_at, kind, reason, detail, dev_eui, dev_addr, gateway_id,
			f_cnt_up, f_port
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		ev.At,
		ev.Kind,
		nullableText(ev.Reason),
		nullableText(ev.Detail),
		nullableEUI(ev.DevEUI),
		nullableDevAddr(ev.DevAddr),
		nullableEUI(ev.GatewayID),
		int64(ev.FCntUp),
		int16(ev.FPort),
	)
	if err != nil {
		return fmt.Errorf("pgstore: record event: %w", err)
	}
	return nil
}

// --- Decoded payloads -------------------------------------------------------

// UpdateUplinkDecoded stores the payload formatter's reading of a frame that was
// already written.
//
// The two writes are separate because decoding happens off the radio path: the
// uplink is persisted the moment it is authenticated, and a formatter that takes
// its time or throws cannot delay or damage that. Keying on the natural key
// rather than a returned surrogate id keeps the Recorder interface unchanged.
//
// Exactly one of decoded/decodeError is non-empty, matching the schema's
// ns_uplinks_decode_outcome check. Both empty is refused: it would clear a
// previous reading and record nothing, which is what a caller that ignored the
// formatter's error would do.
func (s *Store) UpdateUplinkDecoded(ctx context.Context, devEUI lorawan.EUI64,
	keyEpoch, fCntUp uint32, decoded []byte, decodeError string) error {

	if len(decoded) == 0 && decodeError == "" {
		return errors.New("pgstore: nothing to record as a decode outcome")
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE ns_uplinks SET decoded = $4::jsonb, decode_error = $5
		WHERE dev_eui = $1 AND key_epoch = $2 AND f_cnt_up = $3`,
		euiText(devEUI), int64(keyEpoch), int64(fCntUp),
		nullableText(string(decoded)), nullableText(decodeError))
	if err != nil {
		return fmt.Errorf("pgstore: record decode outcome: %w", err)
	}
	return nil
}

// UplinkRow is a stored uplink as the console reads it: the raw frame plus the
// formatter's reading of it, and the identifiers a device screen needs.
type UplinkRow struct {
	UplinkID    int64
	ReceivedAt  time.Time
	DevEUI      string
	DevAddr     string
	FCntUp      uint32
	FPort       int
	FRMPayload  []byte
	RawPayload  []byte
	GatewayID   string
	FrequencyHz uint32
	DataRate    int
	RSSI        float64
	SNR         float64
	Retrans     bool
	// Decoded is the formatter's output as JSON, and DecodeError why it refused.
	// Both empty means no formatter is configured, which the console renders
	// differently from a failure.
	Decoded     []byte
	DecodeError string
}

// RecentUplinks returns the newest uplinks, newest first, optionally for one
// device. The decoded column comes back as raw JSON rather than a map: the
// console renders it as-is, and re-encoding it through Go would reorder keys and
// turn numbers the formatter produced into float64.
func (s *Store) RecentUplinks(ctx context.Context, devEUI string, limit int) ([]UplinkRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT uplink_id, received_at, dev_eui, COALESCE(dev_addr, ''),
		       f_cnt_up, f_port, frm_payload, raw_phy_payload,
		       COALESCE(gateway_id, ''), frequency_hz, data_rate_index,
		       rssi, snr, retransmission,
		       COALESCE(decoded::text, ''), COALESCE(decode_error, '')
		FROM ns_uplinks
		WHERE ($1::text = '' OR dev_eui = $1)
		ORDER BY received_at DESC, uplink_id DESC
		LIMIT $2`, devEUI, limit)
	if err != nil {
		return nil, fmt.Errorf("pgstore: recent uplinks: %w", err)
	}
	defer rows.Close()

	var out []UplinkRow
	for rows.Next() {
		var r UplinkRow
		var fPort int16
		if err := rows.Scan(&r.UplinkID, &r.ReceivedAt, &r.DevEUI, &r.DevAddr,
			&r.FCntUp, &fPort, &r.FRMPayload, &r.RawPayload, &r.GatewayID,
			&r.FrequencyHz, &r.DataRate, &r.RSSI, &r.SNR, &r.Retrans,
			&r.Decoded, &r.DecodeError); err != nil {
			return nil, fmt.Errorf("pgstore: scan uplink: %w", err)
		}
		r.FPort = int(fPort)
		out = append(out, r)
	}
	return out, rows.Err()
}

// --- NULL mapping -----------------------------------------------------------
//
// The schema makes the optional columns nullable, and a zero-valued identifier
// is "absent" rather than "all zeroes": a frame received before any gateway was
// identified must not be stored as gateway 0000000000000000.

func nullableEUI(e lorawan.EUI64) any {
	if e == (lorawan.EUI64{}) {
		return nil
	}
	return euiText(e)
}

func nullableDevAddr(a lorawan.DevAddr) any {
	if a == 0 {
		return nil
	}
	return devAddrText(a)
}

func nullableText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}
