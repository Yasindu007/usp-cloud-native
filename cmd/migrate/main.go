// migrate is the database migration CLI for the URL Shortener Platform.
// It wraps golang-migrate to provide a simple command-line interface.
//
// Usage:
//
//	go run ./cmd/migrate up          # Apply all pending migrations
//	go run ./cmd/migrate down 1      # Roll back 1 migration
//	go run ./cmd/migrate down        # Roll back all migrations
//	go run ./cmd/migrate status      # Print current migration version
//	go run ./cmd/migrate force 3     # Force migration version (use carefully)
//
// The migrations directory is resolved relative to the working directory.
// Always run from the project root: cd url-shortener && go run ./cmd/migrate up
//
// golang-migrate is chosen over Atlas, Flyway, and Liquibase because:
//   - Pure Go: no JVM, no separate binary to install
//   - Integrates directly with pgx
//   - Plain SQL files: readable, reviewable, tooling-agnostic
//   - Reversible: every up migration must have a corresponding down migration
package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/urlshortener/platform/internal/config"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	if err := config.LoadDotEnv(); err != nil {
		fmt.Fprintf(os.Stderr, "error: loading .env: %v\n", err)
		os.Exit(1)
	}

	dsn := os.Getenv("DB_PRIMARY_DSN")
	if dsn == "" {
		dsn = "postgresql://urlshortener:secret@localhost:5432/urlshortener?sslmode=disable"
		fmt.Println("DB_PRIMARY_DSN not set, using default:", dsn)
	}

	// golang-migrate requires the postgres:// scheme (not postgresql://).
	// pgx uses postgresql:// — we normalize here.
	if len(dsn) > 12 && dsn[:12] == "postgresql:/" {
		dsn = "postgres:/" + dsn[12:]
	}

	migrationsPath := "file://deployments/migrations"

	m, err := migrate.New(migrationsPath, dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: creating migrate instance: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		srcErr, dbErr := m.Close()
		if srcErr != nil {
			fmt.Fprintf(os.Stderr, "migrate source close error: %v\n", srcErr)
		}
		if dbErr != nil {
			fmt.Fprintf(os.Stderr, "migrate db close error: %v\n", dbErr)
		}
	}()

	command := os.Args[1]

	switch command {
	case "up":
		fmt.Println("Running all pending migrations...")
		if err := m.Up(); err != nil {
			if errors.Is(err, migrate.ErrNoChange) {
				fmt.Println("No migrations to apply — database is up to date.")
				return
			}
			fmt.Fprintf(os.Stderr, "error: migration up failed: %v\n", err)
			os.Exit(1)
		}
		v, _, _ := m.Version()
		fmt.Printf("✅ Migrations applied. Current version: %d\n", v)

	case "down":
		steps := 1
		if len(os.Args) >= 3 {
			n, err := strconv.Atoi(os.Args[2])
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: invalid steps argument %q: %v\n", os.Args[2], err)
				os.Exit(1)
			}
			steps = n
		}
		fmt.Printf("Rolling back %d migration(s)...\n", steps)
		if err := m.Steps(-steps); err != nil {
			if errors.Is(err, migrate.ErrNoChange) {
				fmt.Println("No migrations to roll back.")
				return
			}
			fmt.Fprintf(os.Stderr, "error: migration down failed: %v\n", err)
			os.Exit(1)
		}
		v, _, _ := m.Version()
		fmt.Printf("✅ Rollback complete. Current version: %d\n", v)

	case "status":
		v, dirty, err := m.Version()
		if err != nil {
			if errors.Is(err, migrate.ErrNilVersion) {
				fmt.Println("No migrations have been applied yet.")
				return
			}
			fmt.Fprintf(os.Stderr, "error: getting version: %v\n", err)
			os.Exit(1)
		}
		dirtyStr := ""
		if dirty {
			dirtyStr = " ⚠️  DIRTY (a previous migration failed partway through)"
		}
		fmt.Printf("Current version: %d%s\n", v, dirtyStr)

	case "force":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "error: force requires a version number")
			os.Exit(1)
		}
		v, err := strconv.Atoi(os.Args[2])
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: invalid version %q: %v\n", os.Args[2], err)
			os.Exit(1)
		}
		fmt.Printf("⚠️  Forcing version to %d (use only to recover from a dirty state)\n", v)
		if err := m.Force(v); err != nil {
			fmt.Fprintf(os.Stderr, "error: force failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✅ Version forced to %d\n", v)

	default:
		fmt.Fprintf(os.Stderr, "error: unknown command %q\n", command)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`URL Shortener — Database Migration Tool

Usage:
  go run ./cmd/migrate <command> [args]

Commands:
  up            Apply all pending migrations
  down [N]      Roll back N migrations (default: 1)
  status        Show current migration version
  force <N>     Force version to N (recovery only — use carefully)

Environment:
  DB_PRIMARY_DSN  PostgreSQL DSN (default: local dev DSN)

Examples:
  go run ./cmd/migrate up
  go run ./cmd/migrate down 1
  go run ./cmd/migrate status`)
}
