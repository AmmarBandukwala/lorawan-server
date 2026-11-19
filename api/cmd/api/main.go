package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fieldnode/lorawan-server/api/internal/httpapi"
	"github.com/fieldnode/lorawan-server/api/internal/nsclient"
	"github.com/fieldnode/lorawan-server/api/internal/storage/postgres"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL is required")
	}

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("database is not ready: %v", err)
	}
	if err := postgres.ApplyMigrations(ctx, pool); err != nil {
		log.Fatalf("apply database migrations: %v", err)
	}

	// Provisioning is optional in the sense that the dashboard is still useful
	// without it — the fleet, traffic and health all keep working — but it must
	// never fail silently, hence the warning rather than a quiet nil.
	var provisioner httpapi.Provisioner
	if nsAPI := os.Getenv("NS_API_URL"); nsAPI != "" {
		client, err := nsclient.New(nsAPI, os.Getenv("NS_API_TOKEN"))
		if err != nil {
			// A half-configured integration is a misconfiguration worth dying
			// over: starting anyway would leave an operator clicking a Register
			// button that cannot work.
			log.Fatalf("network server API: %v", err)
		}
		provisioner = httpapi.NewNSProvisioner(client)
		log.Printf("provisioning through the network server at %s", nsAPI)
	} else {
		log.Print("NS_API_URL is not set: device registration will answer 503")
	}

	address := os.Getenv("HTTP_ADDR")
	if address == "" {
		address = ":8080"
	}
	app := httpapi.New(postgres.New(pool), provisioner)
	server := &http.Server{
		Addr:              address,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		// A provisioning request waits up to 10 s for the network server, so the
		// write deadline has to clear that; anything shorter would cut off a
		// request that is legitimately still working.
		WriteTimeout: 15 * time.Second,
		ReadTimeout:  15 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("Fieldnode API listening on %s", address)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		log.Fatalf("serve: %v", err)
	case <-ctx.Done():
	}

	// Draining matters more here than in a stateless service: a request in flight
	// may be mid-provisioning, and killing it between the ns accepting the device
	// and this process reading the response leaves the operator with an error for
	// a device that exists.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}
