//go:build integration

package postgres

import (
	"errors"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/governance"
)

// ADR-GOV-005 — multi-approver approval quorum, against a real database.

func TestApprovalStore_RecordCountsAndListsDistinctApprovers(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	co := NewConfigObjectRepo(pool)
	cfg := mkObj(t, co, "gov-appr-count.md", domain.RetentionWorkingMaterial)
	store := NewApprovalStore(pool)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := store.Record(ctx, tx, domain.SystemTenantID, cfg, "alice"); err != nil {
		t.Fatalf("record alice: %v", err)
	}
	if n, err := store.CountDistinct(ctx, tx, cfg); err != nil || n != 1 {
		t.Fatalf("count after alice = %d, err=%v, want 1", n, err)
	}
	if err := store.Record(ctx, tx, domain.SystemTenantID, cfg, "bob"); err != nil {
		t.Fatalf("record bob: %v", err)
	}
	n, err := store.CountDistinct(ctx, tx, cfg)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("count = %d, want 2 distinct approvers", n)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	approvers, err := store.ListApprovers(ctx, cfg)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(approvers) != 2 {
		t.Fatalf("listed %d approvers, want 2", len(approvers))
	}
	if approvers[0].ApproverUserID != "alice" || approvers[1].ApproverUserID != "bob" {
		t.Fatalf("approvers = %v, want [alice bob] in recorded order", approvers)
	}
}

// The UNIQUE (tenant_id, governance_id, approver_user_id) constraint is the
// entire duplicate-approver defence — this proves it bites live, at the
// database, not merely in application logic a second code path could bypass.
func TestApprovalStore_DuplicateApproverRejectedByConstraint(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	co := NewConfigObjectRepo(pool)
	cfg := mkObj(t, co, "gov-appr-dup.md", domain.RetentionWorkingMaterial)
	store := NewApprovalStore(pool)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := store.Record(ctx, tx, domain.SystemTenantID, cfg, "alice"); err != nil {
		t.Fatalf("first record: %v", err)
	}
	if err := store.Record(ctx, tx, domain.SystemTenantID, cfg, "alice"); !errors.Is(err, governance.ErrAlreadyApproved) {
		t.Fatalf("expected governance.ErrAlreadyApproved, got %v", err)
	}
}

// RLS isolation on approval_record (ADR-TENANCY-001): a connection bound to
// tenant A must not see tenant B's recorded approvals, even though both name
// the same real governance object — proving isolation holds on the approval
// itself, not merely on the object it approves.
func TestApprovalStore_RLSIsolatesApprovalsByTenant(t *testing.T) {
	priv := testPool(t)
	ctx := adminTestCtx()

	tenants := NewTenantStore(priv)
	a, err := tenants.Create(ctx, newTenant("appr-alpha", "ext-appr-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(ctx, newTenant("appr-bravo", "ext-appr-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	co := NewConfigObjectRepo(priv)
	cfg := mkObj(t, co, "gov-appr-rls.md", domain.RetentionWorkingMaterial)

	privStore := NewApprovalStore(priv)
	tx, err := priv.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := privStore.Record(ctx, tx, a.TenantID, cfg, "alice"); err != nil {
		t.Fatalf("record for tenant a: %v", err)
	}
	if err := privStore.Record(ctx, tx, b.TenantID, cfg, "bob"); err != nil {
		t.Fatalf("record for tenant b: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	scoped := tenantScopedPool(t)
	scopedStore := NewApprovalStore(scoped)

	ctxA := domain.WithTenant(ctx, a)
	approversA, err := scopedStore.ListApprovers(ctxA, cfg)
	if err != nil {
		t.Fatalf("list as tenant a: %v", err)
	}
	if len(approversA) != 1 || approversA[0].ApproverUserID != "alice" {
		t.Fatalf("tenant A sees %v, want only its own approval (alice) — isolation is not enforced", approversA)
	}

	ctxB := domain.WithTenant(ctx, b)
	approversB, err := scopedStore.ListApprovers(ctxB, cfg)
	if err != nil {
		t.Fatalf("list as tenant b: %v", err)
	}
	if len(approversB) != 1 || approversB[0].ApproverUserID != "bob" {
		t.Fatalf("tenant B sees %v, want only its own approval (bob) — isolation is not enforced", approversB)
	}
}
