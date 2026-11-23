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

	// Provisioning and the control plane are optional in the sense that the
	// dashboard is still useful without them — the fleet, traffic and health all
	// keep working — but neither may fail silently, hence the warning and the
	// fatal on a half-configured client rather than a quiet nil.
	var provisioner httpapi.Provisioner
	var controller httpapi.Controller
	if nsAPI := os.Getenv("NS_API_URL"); nsAPI != "" {
		client, err := nsclient.New(nsAPI, os.Getenv("NS_API_TOKEN"),
			// The network server serves its read-only health endpoint on its own
			// port; without this the console would ask the token-protected API
			// port for a route that is not served there.
			nsclient.WithStatusURL(os.Getenv("NS_STATUS_URL")),
		)
		if err != nil {
			// A half-configured integration is a misconfiguration worth dying
			// over: starting anyway would leave an operator clicking a Register
			// button that cannot work.
			log.Fatalf("network server API: %v", err)
		}
		provisioner = httpapi.NewNSProvisioner(client)
		// The same client serves both: it already holds the base URL and token,
		// and its control methods match the Controller port exactly.
		controller = client
		log.Printf("provisioning and downlinks through the network server at %s", nsAPI)
	} else {
		log.Print("NS_API_URL is not set: device registration and downlinks will answer 503")
	}

	address := os.Getenv("HTTP_ADDR")
	if address == "" {
		address = ":8080"
	}
	app := httpapi.New(postgres.New(pool), provisioner, controller,
		// The console checks whether an MCP listener is there so the Agents page
		// can say so truthfully. Unset means no listener, which the page reports
		// as its own state rather than as a failure — see httpapi.AgentAccess.
		httpapi.WithAgentAccess(httpapi.NewAgentAccess(os.Getenv("MCP_URL"))))
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
