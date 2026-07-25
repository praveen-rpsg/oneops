// Package migrate applies embedded, versioned SQL migrations at startup and in
// tests. It records applied versions in a schema_migrations table so runs are
// idempotent. The same SQL files are validated by Atlas in CI.
package migrate

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed sql/*.sql
var migrationsFS embed.FS

// Up applies all pending forward migrations in lexical order. It is idempotent.
func Up(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    text PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	files, err := forwardFiles()
	if err != nil {
		return err
	}

	for _, name := range files {
		version := strings.TrimSuffix(name, ".sql")

		var exists bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = $1)`, version,
		).Scan(&exists); err != nil {
			return fmt.Errorf("check migration %s: %w", version, err)
		}
		if exists {
			continue
		}

		body, err := migrationsFS.ReadFile("sql/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if err := applyOne(ctx, pool, version, string(body)); err != nil {
			return err
		}
	}
	return nil
}

// Latest returns the version of the newest embedded migration — the schema
// version this binary expects — or "" when there are none. It is a read-only
// helper for operational diagnostics; it touches no database and applies nothing.
func Latest() (string, error) {
	files, err := forwardFiles()
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", nil
	}
	return strings.TrimSuffix(files[len(files)-1], ".sql"), nil
}

// Pending returns the embedded migration versions this binary carries that are
// not yet recorded in schema_migrations. It is read-only and applies nothing.
//
// A non-empty result means the running binary expects schema that the database
// does not have — a rolling deployment that put the new binary ahead of the
// migrations, or a migration interrupted partway. Startup uses it to refuse
// rather than run against a schema it was not built for.
func Pending(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	files, err := forwardFiles()
	if err != nil {
		return nil, err
	}
	// If the table does not exist yet, everything is pending.
	rows, err := pool.Query(ctx, `SELECT version FROM schema_migrations`)
	applied := map[string]bool{}
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				return nil, fmt.Errorf("scan applied migration: %w", err)
			}
			applied[v] = true
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("read applied migrations: %w", err)
		}
	}
	var pending []string
	for _, name := range files {
		version := strings.TrimSuffix(name, ".sql")
		if !applied[version] {
			pending = append(pending, version)
		}
	}
	return pending, nil
}

func applyOne(ctx context.Context, pool *pgxpool.Pool, version, body string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", version, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, body); err != nil {
		return fmt.Errorf("apply migration %s: %w", version, err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO schema_migrations (version) VALUES ($1)`, version,
	); err != nil {
		return fmt.Errorf("record migration %s: %w", version, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migration %s: %w", version, err)
	}
	return nil
}

func forwardFiles() ([]string, error) {
	entries, err := fs.ReadDir(migrationsFS, "sql")
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if !strings.HasSuffix(n, ".sql") {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}
