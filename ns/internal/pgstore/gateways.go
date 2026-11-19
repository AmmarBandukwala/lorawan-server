package pgstore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
)

// GatewayObservation is one gateway's presence and health as the transport saw
// it. The transport's registry is the live authority — it knows the UDP address
// a gateway is speaking from right now — and this is the durable shadow of it.
//
// It is defined here rather than taken from the transport package so that
// storage does not depend on a particular gateway implementation: cmd/ns maps
// whichever transport it started into this shape.
type GatewayObservation struct {
	EUI      lorawan.EUI64
	LastSeen time.Time
	Region   string
	// ProtocolVersion is the Semtech packet-forwarder protocol version, and 0
	// for a transport that has no such number. Protocol and Transport are the
	// transport-neutral spelling, added once the server spoke two protocols.
	ProtocolVersion byte
	Transport       string
	Protocol        string
	Firmware        string
	Model           string
	Addr            string

	Packets        uint64
	RxReceived     uint32
	RxOK           uint32
	TxReceived     uint32
	DownlinksSent  uint64
	DownlinkErrors uint64
	LastTxAckError string
	Latitude       float64
	Longitude      float64
	Altitude       int
}

// gatewayColumns is the INSERT column list, in the order buildArgs emits them.
// Keeping the two in one place is what stops a added column from silently
// shifting every value after it into the wrong field.
var gatewayColumns = []string{
	"gateway_id", "last_seen", "region", "protocol_version", "transport",
	"protocol", "station_firmware", "station_model", "last_addr",
	"packets", "rx_received", "rx_ok", "tx_received",
	"downlinks_sent", "downlink_errors", "last_tx_ack_error",
	"latitude", "longitude", "altitude",
}

// args returns the row's values in gatewayColumns order.
func (o GatewayObservation) args() []any {
	return []any{
		euiText(o.EUI),
		o.LastSeen,
		o.Region,
		int16(o.ProtocolVersion),
		o.Transport,
		o.Protocol,
		o.Firmware,
		o.Model,
		o.Addr,
		int64(o.Packets),
		int64(o.RxReceived),
		int64(o.RxOK),
		int64(o.TxReceived),
		int64(o.DownlinksSent),
		int64(o.DownlinkErrors),
		o.LastTxAckError,
		o.Latitude,
		o.Longitude,
		o.Altitude,
	}
}

// RecordGatewayObservations upserts a batch of gateway records.
//
// One statement per batch, called on a timer rather than per packet: a forwarder
// sends a stat every 30 seconds and dozens of rxpk between them, and none of that
// traffic needs Postgres to answer whether the gateway is up.
//
// first_seen is written only on insert. Overwriting it on every flush would turn
// "when did we first see this gateway" into "when did we last flush", which
// makes the column useless for the one question it is asked — is this a new
// gateway or one that has been here for months?
func (s *Store) RecordGatewayObservations(ctx context.Context, obs []GatewayObservation) error {
	if len(obs) == 0 {
		return nil
	}

	perRow := len(gatewayColumns)
	rows := make([]string, 0, len(obs))
	args := make([]any, 0, len(obs)*perRow)
	for i, o := range obs {
		placeholders := make([]string, perRow)
		for c := 0; c < perRow; c++ {
			placeholders[c] = fmt.Sprintf("$%d", i*perRow+c+1)
		}
		rows = append(rows, "("+strings.Join(placeholders, ",")+")")
		args = append(args, o.args()...)
	}

	set := make([]string, 0, len(gatewayColumns)-1)
	for _, col := range gatewayColumns[1:] { // gateway_id is the key, never updated
		set = append(set, fmt.Sprintf("%s = EXCLUDED.%s", col, col))
	}

	query := fmt.Sprintf(`
		INSERT INTO ns_gateways (%s) VALUES %s
		ON CONFLICT (gateway_id) DO UPDATE SET
			%s,
			updated_at = now()`,
		strings.Join(gatewayColumns, ", "),
		strings.Join(rows, ", "),
		strings.Join(set, ", "))

	if _, err := s.pool.Exec(ctx, query, args...); err != nil {
		return fmt.Errorf("pgstore: record gateway observations: %w", err)
	}
	return nil
}
