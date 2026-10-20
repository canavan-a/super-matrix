// Package migrations owns supermatrix's SQLite schema via goose: plain
// numbered .sql files (goose's "+goose Up"/"+goose Down" markers) embedded
// into the binary, applied with goose's Provider API rather than gorm's
// AutoMigrate, so schema changes are explicit, reviewable diffs.
package migrations

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
)

//go:embed sql/*.sql
var migrationFiles embed.FS

// Run applies any pending migrations in sql/ to db.
func Run(db *sql.DB) error {
	migrationsFS, err := fs.Sub(migrationFiles, "sql")
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}

	provider, err := goose.NewProvider(goose.DialectSQLite3, db, migrationsFS)
	if err != nil {
		return fmt.Errorf("create goose provider: %w", err)
	}

	if _, err := provider.Up(context.Background()); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	return nil
}

// UpTo applies migrations up to and including version (tests use it to
// build a database as an older release left it).
func UpTo(db *sql.DB, version int64) error {
	migrationsFS, err := fs.Sub(migrationFiles, "sql")
	if err != nil {
		return err
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, migrationsFS)
	if err != nil {
		return err
	}
	_, err = provider.UpTo(context.Background(), version)
	return err
}
