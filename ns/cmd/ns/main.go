// Command ns runs the Fieldnode network server: it listens for gateways speaking
// the Semtech UDP packet-forwarder protocol, joins and authenticates end-devices,
// maintains their sessions and transmits downlinks in the RX windows.
//
// It needs no gateway bridge, MQTT broker or separate cache: one process speaks
// the forwarder protocol directly and keeps protocol state in PostgreSQL (or in
// memory for a bench run):
//
//	cd ns && NS_NET_ID=010203 NS_DATABASE_URL=postgres://... go run ./cmd/ns
//
// Configuration is environment driven; see config.go and ns/README.md. Without
// NS_DATABASE_URL the server runs with in-memory state, which is fine for a bench
// test and wrong for anything else: sessions and join-nonce replay protection are
// lost on restart.
//
// The binary targets Linux and macOS (it traps SIGTERM).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fieldnode/lorawan-server/ns/internal/codec"
	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/gateway"
	"github.com/fieldnode/lorawan-server/ns/internal/gateway/lbs"
	"github.com/fieldnode/lorawan-server/ns/internal/gateway/semtech"
	"github.com/fieldnode/lorawan-server/ns/internal/integration"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
	"github.com/fieldnode/lorawan-server/ns/internal/pgstore"
)

func main() {
	// The probe runs before configuration is validated: a container health check
	// must not fail because of an unrelated environment change.
	healthz := flag.Bool("healthz", false,
		"ask the status endpoint of a running ns and exit 0/1 (container health check)")
	flag.Parse()
	if *healthz {
		os.Exit(runHealthz(envString("NS_STATUS_ADDR", ":8090"), 3*time.Second))
	}

	// SIGTERM is what `docker stop` sends; without trapping it the process is
	// killed mid-frame and in-flight uplinks are lost.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ns: bad configuration: %v\n", err)
		os.Exit(1)
	}

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.logLevel}))
	if err := run(ctx, log, cfg); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("ns exited with an error", "err", err)
		os.Exit(1)
	}
}

// run wires the three moving parts — store, engine, transport — serves until the
// context is cancelled, and shuts them down in the order that stops new work
// first and releases resources last.
func run(ctx context.Context, log *slog.Logger, cfg config) error {
	engCfg := engine.DefaultConfig()
	engCfg.NetID = cfg.netID
	engCfg.Region = cfg.region
	engCfg.DownlinkPowerdBm = cfg.downlinkPowerdBm
	engCfg.DeviceStatusEvery = cfg.deviceStatusEvery
	engCfg.DisableDutyCycleEnforcement = cfg.disableDutyCycle
	engCfg.DutyCycleWindow = cfg.dutyCycleWindow
	if cfg.aggregationDelay >= 0 {
		engCfg.AggregationDelay = cfg.aggregationDelay
	}

	store, gatewayStore, integrationStore, closeStore, err := openStore(ctx, log, cfg.databaseURL)
	if err != nil {
		return err
	}
	defer closeStore()

	if err := seedDevices(ctx, log, store, cfg.seedDevices); err != nil {
		return err
	}

	// Integrations wrap the store rather than replacing it: the dispatcher is the
	// Recorder the engine sees, and it writes through before fanning out. With no
	// endpoints configured this is a pass-through that costs nothing.
	recorder, integrations, supervisor, closeIntegrations, err := newRecorder(store, integrationStore, cfg, log)
	if err != nil {
		return err
	}
	defer closeIntegrations()

	// The engine needs a transmitter at construction and the transport needs the
	// engine as its uplink handler, so the transport is installed afterwards.
	lazy := &gateway.Lazy{}
	eng, err := engine.New(engCfg, recorder, lazy, engine.WithLogger(log.With("component", "engine")))
	if err != nil {
		return fmt.Errorf("build the engine: %w", err)
	}

	// Reload the duty-cycle ledger before the first gateway can be heard from.
	// A server that starts with an empty book would treat an allowance it had
	// already spent as untouched, which is the one thing the enforcement was
	// added to prevent. Failing to read it is not fatal: enforcing from an empty
	// ledger is still stricter than not enforcing.
	if restored, err := eng.RestoreAirtime(ctx); err != nil {
		log.Warn("could not reload the duty-cycle ledger; enforcing from an empty one",
			"err", err)
	} else if restored > 0 {
		log.Info("reloaded the duty-cycle ledger", "bookings", restored,
			"window", eng.AirtimeWindow())
	}

	srv, err := semtech.New(semtech.Config{
		Addr:           cfg.listenAddr,
		Region:         cfg.region,
		Handler:        eng,
		Logger:         log.With("component", "gateway"),
		GatewayTimeout: cfg.gatewayTimeout,
	})
	if err != nil {
		return fmt.Errorf("build the gateway server: %w", err)
	}
	if err := srv.Start(ctx); err != nil {
		return err
	}

	// Both transports need the engine as their uplink handler, and the engine
	// needs a transmitter, so the router is built from what exists and installed
	// afterwards. Registering UDP first matters only as a tie-break: when neither
	// transport recognises a gateway the router tries them in this order.
	router := gateway.NewRouter(gateway.NamedTransport{Name: "semtech-udp", Gateway: srv})

	// Basics Station is a second route to the same engine. A station cannot be
	// reached over UDP at all, so this is not an optimisation: without it the
	// MultiTech, Kerlink, Rise-LR and Microchip-module gateways a field
	// deployment actually contains are unusable.
	var station *lbs.Server
	if cfg.lbsAddr != "" {
		station, err = lbs.New(lbs.Config{
			Addr:                   cfg.lbsAddr,
			Region:                 cfg.region,
			NetID:                  cfg.netID,
			JoinEUIs:               cfg.lbsJoinEUIs,
			Token:                  cfg.lbsToken,
			RadioPlanPath:          cfg.lbsRadioPlanPath,
			AllowUnlimitedTransmit: cfg.lbsUnlimitedTransmit,
			Handler:                eng,
			Logger:                 log.With("component", "station"),
		})
		if err != nil {
			_ = srv.Close()
			return fmt.Errorf("build the basic station server: %w", err)
		}
		if err := station.Start(ctx); err != nil {
			_ = srv.Close()
			return err
		}
		router.Add("basic-station", station)
	}
	lazy.Set(router)

	// What the console shows someone who is standing in front of a gateway with
	// nothing configured yet. The UDP transport is reported as unsecured because
	// it genuinely is: the protocol carries no credential, so anyone on a network
	// that can reach the port can claim a gateway EUI and inject uplinks. Saying
	// so is more useful than letting it be discovered as a mystery.
	listeners := []listenerView{
		newListenerView("semtech-udp", cfg.listenAddr, false),
	}
	if station != nil {
		listeners = append(listeners, newListenerView("basic-station", cfg.lbsAddr, cfg.lbsToken != ""))
	}

	// Gateway presence is persisted on a timer so the dashboard can answer
	// "which gateways do we have, and are they up" from the database. Stopping
	// it is ordered before the pool closes, because its final flush writes.
	stopGatewayPersister := startGatewayPersister(ctx, log.With("component", "gateway-store"),
		router, gatewayStore, cfg.region, cfg.gatewayFlushInterval)

	// The duty-cycle ledger and the frame de-duplication book are persisted for
	// the same reason presence is: the in-memory version is a claim about the
	// radio that a restart voids. Stopping it is ordered before the pool closes.
	stopLedgerPersister := startLedgerPersister(ctx, log.With("component", "ledger-store"),
		eng, ledgerSinkOf(store), cfg.ledgerFlushInterval)

	var status *statusServer
	if cfg.statusAddr != "" && !strings.EqualFold(cfg.statusAddr, "off") {
		status = newStatusServer(cfg.statusAddr, log.With("component", "status"), router, eng, integrations, listeners)
		if err := status.Start(); err != nil {
			_ = srv.Close()
			if station != nil {
				_ = station.Close()
			}
			return err
		}
	}

	// The application API is opt-in by token: without NS_API_TOKEN it is not
	// created at all, rather than created and open.
	var api *apiServer
	if cfg.apiToken != "" {
		api = newAPIServer(cfg.apiAddr, cfg.apiToken, eng, store.Devices,
			log.With("component", "api"))
		// Device models and decoded traffic come from the same store, when it can
		// serve them. The in-memory store can hold models but has no decoded
		// columns, so the endpoints report 503 rather than returning empty lists
		// that look like a quiet network.
		api.models = store.Models
		if reader, ok := gatewayStore.(TrafficReader); ok {
			api.traffic = reader
		}
		api.codecOpts = codec.Options{Timeout: cfg.codecExecution}
		api.decodeTimeout = cfg.codecTimeout
		if integrations != nil {
			api.invalidate = integrations.InvalidateDecoder
		}
		// Integration CRUD needs both halves: somewhere to keep a destination and
		// something to apply it to the live sinks. Without a store the endpoints
		// answer 503, which is honest, rather than accepting a write that a
		// restart would forget.
		if integrationStore != nil && supervisor != nil {
			api.integrations = integrationStore
			api.reloadIntegrations = supervisor
			api.environmentSinkNames = supervisor.EnvironmentSinks()
		}
		if err := api.Start(); err != nil {
			_ = srv.Close()
			if station != nil {
				_ = station.Close()
			}
			if status != nil {
				_ = status.Shutdown(context.Background())
			}
			return err
		}
	}

	log.Info("fieldnode network server started",
		"listen", srv.ListenAddr(), "region", cfg.region,
		"net_id", cfg.netID.String(), "database", cfg.databaseURL != "",
		"basic_station", station != nil,
		"status", cfg.statusAddr, "api", api != nil,
		"integrations", len(cfg.webhookURLs))

	<-ctx.Done()
	log.Info("shutting down")

	// Order matters. The radio stops first so nothing new is in flight, then the
	// HTTP surfaces drain their requests, then integrations flush what they have
	// already accepted, and only then does the pool go away — an integration
	// worker that outlived the pool would fail its final writes for nothing.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var errs []error
	if err := srv.Close(); err != nil {
		errs = append(errs, fmt.Errorf("close the gateway server: %w", err))
	}
	// Both radio transports stop before anything drains, so no downlink is
	// dispatched into a transport that is already gone.
	if station != nil {
		if err := station.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close the basic station server: %w", err))
		}
	}
	if status != nil {
		if err := status.Shutdown(shutdownCtx); err != nil {
			errs = append(errs, fmt.Errorf("close the status server: %w", err))
		}
	}
	if api != nil {
		if err := api.Shutdown(shutdownCtx); err != nil {
			errs = append(errs, fmt.Errorf("close the API server: %w", err))
		}
	}
	closeIntegrations()
	// Before the pool: the final flush is a write.
	stopLedgerPersister()
	stopGatewayPersister()
	closeStore()
	return errors.Join(errs...)
}

// newRecorder wraps the store's recorder with the integration dispatcher.
//
// With neither webhook endpoints nor device models it returns the store
// untouched, so a deployment that uses neither pays nothing: no goroutine, no
// channel, no indirection on the radio path.
// newRecorder wraps the store's recorder with the integration dispatcher.
//
// With no webhook endpoints, no device models and nowhere to store destinations
// it returns the store untouched, so a deployment that uses none of them pays
// nothing: no goroutine, no channel, no indirection on the radio path.
//
// A PostgreSQL store changes that test on its own. Destinations can be added at
// runtime, so "none configured right now" is not the same statement as "none is
// possible" -- skipping the dispatcher there would mean an integration saved
// from the panel silently does nothing until the process restarts.
func newRecorder(store engine.Store, integrationStore IntegrationStore, cfg config, log *slog.Logger) (
	engine.Store, *integration.Dispatcher, *integration.Supervisor, func(), error) {

	// Decoding needs somewhere to read devices and models from. A store without a
	// model repository cannot decode, which is the in-memory case.
	canDecode := store.Models != nil && store.Devices != nil
	if len(cfg.webhookURLs) == 0 && !canDecode && integrationStore == nil {
		return store, nil, nil, func() {}, nil
	}
	sinks := make([]integration.Sink, 0, len(cfg.webhookURLs))
	for _, raw := range cfg.webhookURLs {
		w, err := integration.NewWebhook(integration.WebhookConfig{
			URL:   raw,
			Token: cfg.webhookToken,
		})
		if err != nil {
			return engine.Store{}, nil, nil, nil, err
		}
		sinks = append(sinks, w)
	}
	dispatcher := integration.New(store.Recorder, sinks, integration.Config{
		Logger: log.With("component", "integration"),
	})

	// The supervisor owns the stored destinations and installs them alongside
	// the environment ones. It is built only when there is somewhere to store
	// them; without a store the panel gets a 503 rather than a form whose saves
	// would be forgotten.
	var supervisor *integration.Supervisor
	if integrationStore != nil {
		supervisor = integration.NewSupervisor(
			func(ctx context.Context) ([]integration.Destination, error) {
				stored, err := integrationStore.ListIntegrations(ctx)
				if err != nil {
					return nil, err
				}
				out := make([]integration.Destination, 0, len(stored))
				for _, in := range stored {
					out = append(out, in.Destination)
				}
				return out, nil
			},
			dispatcher.SetSinks,
			sinks,
			log,
		)
		// Load before the engine starts, so a saved destination is delivering from
		// the first frame rather than after somebody opens the panel.
		if err := supervisor.Reload(context.Background()); err != nil {
			// Not fatal: a database that is briefly unreadable should not stop the
			// network. The dispatcher keeps the environment sinks, and a later
			// write retries the load.
			log.Error("cannot load stored integrations; continuing with environment ones", "err", err)
		}
	}

	if canDecode {
		// The persister is the same store the dispatcher wraps, when it can write
		// decoded columns. The in-memory store cannot, and there decoding still
		// happens for the webhooks.
		persist, _ := store.Recorder.(integration.DecodePersister)
		decoder := integration.NewPayloadDecoder(modelSource{store.Devices, store.Models},
			integration.DecoderConfig{
				Timeout: cfg.codecTimeout,
				Logger:  log.With("component", "codec"),
				Options: codec.Options{Timeout: cfg.codecExecution},
			})
		dispatcher.EnableDecoding(decoder, persist)
		log.Info("payload formatters enabled", "models", canDecode)
	}
	wrapped := store
	wrapped.Recorder = dispatcher
	if len(dispatcher.Sinks()) > 0 {
		log.Info("integrations enabled", "sinks", dispatcher.Sinks())
	}
	// sync.OnceFunc so the deferred close and the explicit shutdown path cannot
	// both try to drain the queue.
	return wrapped, dispatcher, supervisor, sync.OnceFunc(func() { _ = dispatcher.Close() }), nil
}

// modelSource adapts the engine's two repositories to the narrower port the
// decoder reads. Defining it here keeps the integration package free of any
// assumption about which store it is given.
type modelSource struct {
	devices engine.DeviceRepo
	models  engine.ModelRepo
}

func (m modelSource) DeviceByDevEUI(ctx context.Context, eui lorawan.EUI64) (*engine.Device, error) {
	return m.devices.DeviceByDevEUI(ctx, eui)
}

func (m modelSource) ModelByID(ctx context.Context, id string) (*engine.DeviceModel, error) {
	return m.models.ModelByID(ctx, id)
}

// openStore returns the engine store plus its release function. An empty DSN
// selects in-memory state, which is announced loudly because it silently loses
// sessions across a restart.
func openStore(ctx context.Context, log *slog.Logger, dsn string) (engine.Store, gatewayPersister, IntegrationStore, func(), error) {
	if dsn == "" {
		log.Warn("NS_DATABASE_URL is not set: sessions, join-nonce replay " +
			"protection and gateway presence live in memory and are lost on restart")
		// No integration store either: an in-memory destination would accept a
		// save and lose it on restart, which is worse than refusing the write.
		return engine.NewMemStore().Store(), nil, nil, func() {}, nil
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return engine.Store{}, nil, nil, nil, fmt.Errorf("prepare the database pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return engine.Store{}, nil, nil, nil, fmt.Errorf("the database is not reachable: %w", err)
	}

	store := pgstore.New(pool)
	if err := store.Migrate(ctx); err != nil {
		pool.Close()
		return engine.Store{}, nil, nil, nil, fmt.Errorf("apply database migrations: %w", err)
	}
	// sync.OnceFunc so the deferred close and the explicit shutdown close cannot
	// both run against the same pool.
	return store.Store(), store, store, sync.OnceFunc(store.Close), nil
}
