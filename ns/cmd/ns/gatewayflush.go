package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/gateway/semtech"
	"github.com/fieldnode/lorawan-server/ns/internal/storage/pgstore"
)

// gatewayPersister stores the transport's gateway registry. Only the Postgres
// store implements it: with the in-memory store there is nothing to persist to,
// and the registry is already the only copy of the truth.
type gatewayPersister interface {
	RecordGatewayObservations(ctx context.Context, obs []pgstore.GatewayObservation) error
}

// gatewayRegistry is the part of the transport this reads.
type gatewayRegistry interface {
	Gateways() []semtech.GatewayRecord
}

// startGatewayPersister keeps ns_gateways in step with the live registry.
//
// Why a timer and not a write per packet: a forwarder sends a stat every 30
// seconds and can push dozens of rxpk between them. Presence does not need that
// resolution — the dashboard's own freshness window is minutes — and tying a
// radio packet to a Postgres round trip would put the database on the critical
// path of receiving.
//
// The returned stop function flushes once more before returning, so a clean
// shutdown records the last thing we genuinely knew about each gateway rather
// than whatever the previous interval happened to capture.
func startGatewayPersister(ctx context.Context, log *slog.Logger, reg gatewayRegistry,
	persist gatewayPersister, regionName string, interval time.Duration) func() {

	if persist == nil {
		log.Debug("no gateway persistence configured; the registry stays in memory only")
		return func() {}
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}

	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				flushGateways(ctx, log, reg, persist, regionName)
			}
		}
	}()

	return func() {
		close(done)
		<-stopped
		// A fresh context: ctx is already cancelled by the time shutdown calls
		// this, and a cancelled context would make the final flush a no-op —
		// losing exactly the last-known state the flush exists to preserve.
		finalCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		flushGateways(finalCtx, log, reg, persist, regionName)
	}
}

func flushGateways(ctx context.Context, log *slog.Logger, reg gatewayRegistry,
	persist gatewayPersister, regionName string) {

	records := reg.Gateways()
	if len(records) == 0 {
		return
	}
	obs := make([]pgstore.GatewayObservation, 0, len(records))
	for _, r := range records {
		obs = append(obs, pgstore.GatewayObservation{
			EUI:             r.EUI,
			LastSeen:        r.LastSeen,
			Region:          regionName,
			ProtocolVersion: r.ProtocolVersion,
			Addr:            r.Addr,
			Packets:         r.Packets,
			RxReceived:      r.LastStats.RxReceived,
			RxOK:            r.LastStats.RxOK,
			TxReceived:      r.LastStats.TxReceived,
			DownlinksSent:   r.DownlinksSent,
			DownlinkErrors:  r.DownlinkErrors,
			LastTxAckError:  r.LastTxAckError,
			Latitude:        r.LastStats.Latitude,
			Longitude:       r.LastStats.Longitude,
			Altitude:        r.LastStats.Altitude,
		})
	}
	if err := persist.RecordGatewayObservations(ctx, obs); err != nil {
		// A failed flush is not worth an error log per interval: a database
		// outage already shows up as every gateway ageing out of "online", and a
		// warning here would be the same event reported twice.
		log.Warn("could not persist the gateway registry", "gateways", len(obs), "err", err)
		return
	}
	log.Debug("persisted the gateway registry", "gateways", len(obs))
}
