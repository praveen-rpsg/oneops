// Package postgres implements the domain repositories on PostgreSQL via pgx.
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
)

// NewPool builds a configured pgx connection pool.
//
// This pool keeps the connecting role, which owns the tables and therefore
// bypasses row-level security. It is the pool for background workers, which
// process every tenant from one process, and for migrations. Request-scoped
// work must use NewTenantScopedPool instead.
func NewPool(ctx context.Context, dsn string, maxConns int) (*pgxpool.Pool, error) {
	cfg, err := poolConfig(dsn, maxConns)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	return pool, nil
}

// NewTenantScopedPool builds a pool whose every connection is bound to the
// tenant resolved for the request that acquired it, and which runs as
// oneops_app rather than as the table owner.
//
// This is where database-enforced isolation is actually switched on
// (ADR-TENANCY-001 §3). Both steps are required and neither is sufficient:
//
//   - SET ROLE drops the owner's BYPASSRLS, so the policies are evaluated at
//     all. Without it FORCE ROW LEVEL SECURITY still would not apply, because
//     the connecting role is a superuser in development and the table owner in
//     production.
//   - set_config supplies the tenant the policies compare against. Without it
//     current_setting yields NULL, every policy is false, and the connection
//     correctly sees nothing.
//
// Doing this in PrepareConn rather than in each repository is deliberate: it
// is one place instead of thirty-seven, and a repository added later is
// isolated by construction rather than by its author remembering. pgxpool
// passes the acquiring caller's context, which is what makes it possible.
func NewTenantScopedPool(ctx context.Context, dsn string, maxConns int) (*pgxpool.Pool, error) {
	cfg, err := poolConfig(dsn, maxConns)
	if err != nil {
		return nil, err
	}

	cfg.PrepareConn = func(ctx context.Context, conn *pgx.Conn) (bool, error) {
		// A connection that cannot be bound to a tenant must not serve the
		// request. Returning false destroys it rather than handing back a
		// connection that would silently read as the owner, and the error is
		// propagated so the failure is visible rather than appearing as an
		// empty result set.
		if _, err := conn.Exec(ctx, "SET ROLE oneops_app"); err != nil {
			return false, fmt.Errorf("assume oneops_app: %w", err)
		}
		if _, err := conn.Exec(ctx,
			"SELECT set_config('app.tenant_id', $1, false)", domain.TenantIDFrom(ctx)); err != nil {
			return false, fmt.Errorf("bind tenant: %w", err)
		}
		return true, nil
	}

	// Defence in depth. PrepareConn always overwrites both settings, so this
	// is not required for correctness; it exists so a connection sitting idle
	// in the pool does not hold a usable tenant binding, and so any future path
	// that reaches a connection without going through PrepareConn fails
	// closed instead of inheriting whoever used it last.
	cfg.AfterRelease = func(conn *pgx.Conn) bool {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(ctx, "RESET ROLE"); err != nil {
			return false
		}
		_, err := conn.Exec(ctx, "SELECT set_config('app.tenant_id', '', false)")
		return err == nil
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create tenant-scoped pool: %w", err)
	}
	return pool, nil
}

func poolConfig(dsn string, maxConns int) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	if maxConns > 0 {
		cfg.MaxConns = int32(maxConns)
	}
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 30 * time.Minute
	cfg.HealthCheckPeriod = time.Minute
	return cfg, nil
}

// WaitForDB blocks until the database accepts connections or the budget elapses.
func WaitForDB(ctx context.Context, pool *pgxpool.Pool, budget time.Duration) error {
	deadline := time.Now().Add(budget)
	for {
		err := pool.Ping(ctx)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("database not ready within %s: %w", budget, err)
		}
		time.Sleep(time.Second)
	}
}
