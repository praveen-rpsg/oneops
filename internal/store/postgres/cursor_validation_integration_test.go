//go:build integration

package postgres

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Trust Register audit: entry 8 records "restore inconsistency (an inconsistent
// backup silently trusted)" as an eliminated class — recovery is a verification
// boundary and the platform refuses to trust an inconsistent restore
// (ADR-TENANCY-006).
//
// ADR-CONCURRENCY-004's dossier records a *known* instance that was never
// closed: "A cursor restored ahead of its log would skip the missing range; a
// periodic 'no cursor exceeds its chain head' check is noted future hardening."
// Nothing checked it, so the class had an open instance recorded in the
// programme's own documentation (ADR-TENANCY-011).
//
// A cursor ahead of its log is silent by construction: a watermark that is too
// high is indistinguishable from one that is up to date, and every event in
// between is never read.
func TestCursorValidator_DetectsACursorAheadOfItsLog(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	v := NewCursorValidator(pool)

	// Control: a consistent database reports nothing.
	if problems, err := v.Validate(ctx); err != nil {
		t.Fatalf("baseline validate: %v", err)
	} else if len(problems) != 0 {
		t.Fatalf("precondition: cursors already inconsistent: %v", problems)
	}

	tenants := NewTenantStore(pool)
	owner, err := tenants.Create(ctx, newTenant("cursor-restore", "ext-cursor-restore"))
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	chain := fmt.Sprintf("cursor-restore-%d", time.Now().UnixNano())
	if _, err := pool.Exec(ctx, `
		INSERT INTO audit_chain_head (chain_id, last_seq, last_hash, updated_at, tenant_id)
		VALUES ($1, 5, decode(repeat('00',32),'hex'), now(), $2)`,
		chain, owner.TenantID); err != nil {
		t.Fatalf("seed chain head: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM webhook_cursor WHERE chain_id=$1`, chain)
		_, _ = pool.Exec(context.Background(), `DELETE FROM audit_chain_head WHERE chain_id=$1`, chain)
	})

	// The restore case: the cursor table came from a newer snapshot than the log.
	if _, err := pool.Exec(ctx,
		`INSERT INTO webhook_cursor (chain_id, last_seq, updated_at) VALUES ($1, 99, now())`,
		chain); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}

	problems, err := v.Validate(ctx)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	t.Logf("cursor at seq 99, log head at seq 5 -> %d problem(s): %v", len(problems), problems)

	if len(problems) == 0 {
		t.Error("a cursor 94 events ahead of its log was not detected — the relay would skip " +
			"every event in between, silently, and nothing would report it (ADR-TENANCY-011)")
	}
	if len(problems) > 0 && !strings.Contains(problems[0], chain) {
		t.Errorf("the problem does not name the affected chain: %v", problems)
	}
}
