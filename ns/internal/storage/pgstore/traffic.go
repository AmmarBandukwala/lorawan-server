package pgstore

import (
	"context"
	"fmt"

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

// RecordUplink stores an accepted uplink.
//
// The engine also records *retransmissions* — a device that did not hear our
// answer repeats the same counter — so the natural key legitimately repeats.
// The existing row is refreshed and its retransmission tally bumped instead of
// the insert being rejected.
func (s *Store) RecordUplink(ctx context.Context, r engine.UplinkRecord) error {
	_, err := s.pool.Exec(ctx, `
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
			retransmissions = ns_uplinks.retransmissions + 1`,
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
	)
	if err != nil {
		return fmt.Errorf("pgstore: record uplink: %w", err)
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
