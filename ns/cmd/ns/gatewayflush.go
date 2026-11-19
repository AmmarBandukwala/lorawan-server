package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/gateway"
	"github.com/fieldnode/lorawan-server/ns/internal/pgstore"
)

// gatewayPersister stores the transport's gateway registry. Only the Postgres
// store implements it: with the in-memory store there is nothing to persist to,
// and the registry is already the only copy of the truth.
type gatewayPersister interface {
	RecordGatewayObservations(ctx context.Context, obs []pgstore.GatewayObservation) error
}

// gatewayRegistry is the part of the transport layer this reads. Both transports
// satisfy it through gateway.Reporter, so a deployment that speaks UDP, Basics
// Station, or both persists one gateway list without knowing which is which.
type gatewayRegistry interface {
	Presence() []gateway.Presence
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

	records := collapseByGateway(reg.Presence())
	if len(records) == 0 {
		return
	}
	obs := make([]pgstore.GatewayObservation, 0, len(records))
	for _, r := range records {
		obs = append(obs, pgstore.GatewayObservation{
			EUI:            r.EUI,
			LastSeen:       r.LastSeen,
			Region:         regionName,
			Transport:      r.Transport,
			Protocol:       r.Protocol,
			Firmware:       r.Firmware,
			Model:          r.Model,
			Addr:           r.Address,
			Packets:        r.Packets,
			RxReceived:     r.RxReceived,
			RxOK:           r.RxOk,
			TxReceived:     r.TxReceived,
			DownlinksSent:  r.DownlinksSent,
			DownlinkErrors: r.DownlinkErrors,
			LastTxAckError: r.LastTxError,
			Latitude:       r.Latitude,
			Longitude:      r.Longitude,
			Altitude:       r.Altitude,
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

// collapseByGateway reduces a per-transport presence list to one row per gateway.
//
// The status endpoint deliberately keeps both rows when a gateway appears on two
// transports — seeing which one is carrying traffic is the point during a
// protocol switch-over — but ns_gateways is keyed by gateway, and Postgres
// rejects an upsert batch that touches the same key twice. So the durable shadow
// keeps one row, preferring whichever transport the gateway is live on right now
// and, failing that, the most recently heard from.
func collapseByGateway(in []gateway.Presence) []gateway.Presence {
	best := make(map[string]gateway.Presence, len(in))
	order := make([]string, 0, len(in))
	for _, p := range in {
		key := p.EUI.String()
		current, seen := best[key]
		if !seen {
			order = append(order, key)
			best[key] = p
			continue
		}
		if betterPresence(p, current) {
			best[key] = p
		}
	}
	out := make([]gateway.Presence, 0, len(order))
	for _, key := range order {
		out = append(out, best[key])
	}
	return out
}

// betterPresence reports whether a should replace b.
func betterPresence(a, b gateway.Presence) bool {
	if a.Connected != b.Connected {
		return a.Connected
	}
	return a.LastSeen.After(b.LastSeen)
}
