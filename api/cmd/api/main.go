package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fieldnode/lorawan-server/api/internal/httpapi"
	"github.com/fieldnode/lorawan-server/api/internal/storage/postgres"
)

func main() {
	ctx := context.Background()
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

	address := os.Getenv("HTTP_ADDR")
	if address == "" {
		address = ":8080"
	}
	app := httpapi.New(postgres.New(pool))
	server := &http.Server{
		Addr:              address,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Printf("Fieldnode API listening on %s", address)
	log.Fatal(server.ListenAndServe())
}
