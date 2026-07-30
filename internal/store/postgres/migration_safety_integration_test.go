//go:build integration

package postgres

import (
	"strings"
	"testing"

	"github.com/rpsg/oneops/internal/store/migrate"
)

// A binary running ahead of its migrations — a rolling deployment that shipped
// the new binary before the migration ran, or a migration interrupted partway —
// must be detected. migrate.Pending reports the gap, and the schema validator
// refuses startup on it.
func TestMigration_PendingIsDetected(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()

	// Simulate an un-recorded migration by forgetting the latest applied
	// version, as an interrupted run or a partial schema_migrations restore does.
	var forgotten string
	if err := pool.QueryRow(ctx,
		`SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&forgotten); err != nil {
		t.Fatalf("read applied: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM schema_migrations WHERE version=$1`, forgotten); err != nil {
		t.Fatalf("forget migration: %v", err)
	}
	defer func() {
		if _, err := pool.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, forgotten); err != nil {
			t.Fatalf("restore migration record: %v", err)
		}
	}()

	pending, err := migrate.Pending(ctx, pool)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	found := false
	for _, v := range pending {
		if v == forgotten {
			found = true
		}
	}
	if !found {
		t.Fatalf("pending = %v, want it to include the forgotten %s", pending, forgotten)
	}

	problems, err := NewSchemaValidator(pool).Validate(ctx)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !strings.Contains(strings.Join(problems, "\n"), "not applied") {
		t.Errorf("schema validator did not flag the pending migration; problems: %v", problems)
	}
}
