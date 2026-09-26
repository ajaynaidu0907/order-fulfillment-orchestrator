// Package store contains the PostgreSQL adapters: the schema migrations, the
// order repository, and the job queue. These form the stable persistence
// foundation that the rest of the service builds on.
package store

import (
	"database/sql"
	"embed"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib" // register the "pgx" database/sql driver
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate applies all pending schema migrations using the given URL-style DSN
// (see config.DBConfig.URL). It is safe to call on every startup; goose tracks
// applied versions in a goose_db_version table.
func Migrate(dsnURL string) error {
	db, err := sql.Open("pgx", dsnURL)
	if err != nil {
		return fmt.Errorf("open db for migration: %w", err)
	}
	defer db.Close()

	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}
	if err := goose.Up(db, "migrations"); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}
