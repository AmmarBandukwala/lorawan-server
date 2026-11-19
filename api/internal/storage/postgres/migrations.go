package postgres

import (
	"context"
	"embed"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

func ApplyMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		migration_id TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return err
	}

	migrations, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return err
	}
	for _, migration := range migrations {
		if migration.IsDir() {
			continue
		}
		transaction, err := pool.Begin(ctx)
		if err != nil {
			return err
		}

		var applied bool
		if err := transaction.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM schema_migrations WHERE migration_id = $1
		)`, migration.Name()).Scan(&applied); err != nil {
			_ = transaction.Rollback(ctx)
			return err
		}
		if applied {
			if err := transaction.Commit(ctx); err != nil {
				return err
			}
			continue
		}

		contents, err := migrationFiles.ReadFile("migrations/" + migration.Name())
		if err != nil {
			_ = transaction.Rollback(ctx)
			return err
		}
		if _, err := transaction.Exec(ctx, string(contents)); err != nil {
			_ = transaction.Rollback(ctx)
			return err
		}
		if _, err := transaction.Exec(ctx, `INSERT INTO schema_migrations (migration_id) VALUES ($1)`, migration.Name()); err != nil {
			_ = transaction.Rollback(ctx)
			return err
		}
		if err := transaction.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}
