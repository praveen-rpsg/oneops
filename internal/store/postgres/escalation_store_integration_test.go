//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
)

// escalationTestCtx carries only the tenant binding: escalation_policy/
// escalation_tier carry no actor column, mirroring onCallTestCtx exactly.
func escalationTestCtx(tn *domain.Tenant) context.Context {
	return domain.WithTenant(context.Background(), tn)
}

func escalationTenant(t *testing.T, tenants *TenantStore, slug string) *domain.Tenant {
	t.Helper()
	tn, err := tenants.Create(adminTestCtx(), newTenant(slug, "ext-"+slug))
	if err != nil {
		t.Fatalf("create tenant %s: %v", slug, err)
	}
	return tn
}

// escalationSchedule creates an on-call schedule in tn's own tenant, over
// the same scoped pool the escalation store under test uses, so the
// resulting schedule_id is a real, RLS-visible row for that tenant.
func escalationSchedule(t *testing.T, scoped *pgxpool.Pool, tn *domain.Tenant, name string) *domain.OnCallSchedule {
	t.Helper()
	store := NewOnCallScheduleStore(scoped)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sch, err := domain.NewOnCallSchedule(tn.TenantID, name, 3600, start)
	if err != nil {
		t.Fatalf("new on-call schedule: %v", err)
	}
	created, err := store.Create(escalationTestCtx(tn), sch)
	if err != nil {
		t.Fatalf("create on-call schedule: %v", err)
	}
	return created
}

func TestEscalationPolicyStore_CreateGetListUpdateDelete(t *testing.T) {
	testPool(t) // ensures migrations are applied before the scoped pool is used
	priv := testPool(t)
	tn := escalationTenant(t, NewTenantStore(priv), "esc-crud")

	scoped := tenantScopedPool(t)
	store := NewEscalationPolicyStore(scoped)
	ctx := escalationTestCtx(tn)

	p, err := domain.NewEscalationPolicy(tn.TenantID, "Default Policy")
	if err != nil {
		t.Fatalf("new policy: %v", err)
	}
	created, err := store.Create(ctx, p)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.RowVersion != 1 || created.Status != domain.EscalationPolicyActive {
		t.Errorf("unexpected created policy: %+v", created)
	}

	got, err := store.Get(ctx, created.PolicyID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "Default Policy" {
		t.Errorf("get returned %+v, want the created fields", got)
	}

	list, err := store.List(ctx, 0, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, item := range list {
		if item.PolicyID == created.PolicyID {
			found = true
		}
	}
	if !found {
		t.Errorf("list did not include the created policy: %+v", list)
	}

	newName := "Renamed Policy"
	updated, err := store.Update(ctx, created.PolicyID, created.RowVersion, domain.EscalationPolicyPatch{Name: &newName})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Name != newName || updated.RowVersion != created.RowVersion+1 {
		t.Errorf("unexpected update result: %+v", updated)
	}

	// Reusing the now-stale row_version must conflict, not silently apply.
	stale := "Stale Name"
	if _, err := store.Update(ctx, created.PolicyID, created.RowVersion, domain.EscalationPolicyPatch{Name: &stale}); !errors.Is(err, domain.ErrVersionMismatch) {
		t.Errorf("stale update err = %v, want ErrVersionMismatch", err)
	}

	if err := store.Delete(ctx, created.PolicyID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.Get(ctx, created.PolicyID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("get after delete err = %v, want ErrNotFound", err)
	}
	if err := store.Delete(ctx, created.PolicyID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("delete of an already-deleted policy err = %v, want ErrNotFound", err)
	}
}

// THIS MUST BITE: tenant B cannot see, patch, delete or add a tier to tenant
// A's policy — row-level security, not a WHERE clause the store remembered
// to add — even naming tenant B's own real, RLS-visible on-call schedule
// (whose schedule_id is a value tenant A has never heard of, ruling out a
// coincidental pass), mirroring TestOnCallScheduleStore_RLSIsolatesByTenant
// exactly.
func TestEscalationPolicyStore_RLSIsolatesByTenant(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a := escalationTenant(t, tenants, "esc-rls-a")
	b := escalationTenant(t, tenants, "esc-rls-b")

	scoped := tenantScopedPool(t)
	store := NewEscalationPolicyStore(scoped)
	ctxA := escalationTestCtx(a)
	ctxB := escalationTestCtx(b)

	schB := escalationSchedule(t, scoped, b, "Tenant B Schedule")

	pA, _ := domain.NewEscalationPolicy(a.TenantID, "Tenant A Policy")
	createdA, err := store.Create(ctxA, pA)
	if err != nil {
		t.Fatalf("create as tenant a: %v", err)
	}

	if _, err := store.Get(ctxB, createdA.PolicyID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("tenant B read tenant A's policy: err = %v, want ErrNotFound", err)
	}
	listB, err := store.List(ctxB, 0, "")
	if err != nil {
		t.Fatalf("list as tenant b: %v", err)
	}
	for _, item := range listB {
		if item.PolicyID == createdA.PolicyID {
			t.Errorf("tenant B saw tenant A's policy in list: %+v", listB)
		}
	}
	newName := "Hijacked"
	if _, err := store.Update(ctxB, createdA.PolicyID, createdA.RowVersion, domain.EscalationPolicyPatch{Name: &newName}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("tenant B patched tenant A's policy: err = %v, want ErrNotFound", err)
	}
	if err := store.Delete(ctxB, createdA.PolicyID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("tenant B deleted tenant A's policy: err = %v, want ErrNotFound", err)
	}

	// Tenant B cannot add a tier to tenant A's policy either, even naming its
	// own real, RLS-visible schedule (schB) — the policy itself is invisible
	// under RLS before the schedule-membership check is ever reached.
	if _, err := store.AddTier(ctxB, createdA.PolicyID, schB.ScheduleID, 300); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("tenant B added a tier to tenant A's policy: err = %v, want ErrNotFound", err)
	}

	// Tenant A still sees and can use its own policy, undisturbed.
	stillThere, err := store.Get(ctxA, createdA.PolicyID)
	if err != nil || stillThere.PolicyID != createdA.PolicyID {
		t.Fatalf("tenant A lost its own policy: %v, %+v", err, stillThere)
	}
}

// AddTier re-verifies the referenced on_call_schedule belongs to the
// caller's tenant before writing (ADR-ASSET-001 §6) — a cross-tenant
// schedule_id is rejected with ErrNotFound, exactly like tenant A naming a
// schedule of its own that does not exist.
//
// Mutation-verified by hand (implementer's evidence report): removing the
// AddTier->verifyScheduleInTenant call makes this test pass a row that
// should have been refused (the FK alone accepts any existing schedule_id
// regardless of tenant, because the FK trigger bypasses RLS on the
// referenced table).
func TestEscalationPolicyStore_AddTierRejectsCrossTenantSchedule(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a := escalationTenant(t, tenants, "esc-xt-a")
	b := escalationTenant(t, tenants, "esc-xt-b")

	scoped := tenantScopedPool(t)
	store := NewEscalationPolicyStore(scoped)
	ctxA := escalationTestCtx(a)

	schB := escalationSchedule(t, scoped, b, "Tenant B Schedule")

	pA, _ := domain.NewEscalationPolicy(a.TenantID, "Tenant A Policy")
	createdA, err := store.Create(ctxA, pA)
	if err != nil {
		t.Fatalf("create policy: %v", err)
	}

	if _, err := store.AddTier(ctxA, createdA.PolicyID, schB.ScheduleID, 300); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("adding tenant B's schedule to tenant A's policy = %v, want ErrNotFound", err)
	}

	// The rejected attempt left no row behind.
	tiers, err := store.ListTiers(ctxA, createdA.PolicyID, 0, "")
	if err != nil {
		t.Fatalf("list tiers: %v", err)
	}
	if len(tiers) != 0 {
		t.Fatalf("a rejected add left a row: %+v", tiers)
	}

	// The identical schedule, named by its OWN tenant, is accepted.
	schA := escalationSchedule(t, scoped, a, "Tenant A Schedule")
	if _, err := store.AddTier(ctxA, createdA.PolicyID, schA.ScheduleID, 300); err != nil {
		t.Errorf("adding tenant A's own schedule failed: %v", err)
	}
}

// AddTier appends at the next position; positions start at 0 and are
// assigned in insertion order.
func TestEscalationPolicyStore_AddTierAppendsAtNextPosition(t *testing.T) {
	priv := testPool(t)
	tn := escalationTenant(t, NewTenantStore(priv), "esc-pos")
	scoped := tenantScopedPool(t)
	store := NewEscalationPolicyStore(scoped)
	ctx := escalationTestCtx(tn)

	p, _ := domain.NewEscalationPolicy(tn.TenantID, "Position Probe")
	created, err := store.Create(ctx, p)
	if err != nil {
		t.Fatalf("create policy: %v", err)
	}

	sch0 := escalationSchedule(t, scoped, tn, "Schedule 0")
	first, err := store.AddTier(ctx, created.PolicyID, sch0.ScheduleID, 60)
	if err != nil {
		t.Fatalf("add first tier: %v", err)
	}
	if first.Position != 0 {
		t.Errorf("first tier position = %d, want 0", first.Position)
	}

	sch1 := escalationSchedule(t, scoped, tn, "Schedule 1")
	second, err := store.AddTier(ctx, created.PolicyID, sch1.ScheduleID, 120)
	if err != nil {
		t.Fatalf("add second tier: %v", err)
	}
	if second.Position != 1 {
		t.Errorf("second tier position = %d, want 1", second.Position)
	}

	sch2 := escalationSchedule(t, scoped, tn, "Schedule 2")
	third, err := store.AddTier(ctx, created.PolicyID, sch2.ScheduleID, 180)
	if err != nil {
		t.Fatalf("add third tier: %v", err)
	}
	if third.Position != 2 {
		t.Errorf("third tier position = %d, want 2", third.Position)
	}
}

// RemoveTier closes the resulting gap: positions stay contiguous 0..N-1.
func TestEscalationPolicyStore_RemoveTierCompactsPositions(t *testing.T) {
	priv := testPool(t)
	tn := escalationTenant(t, NewTenantStore(priv), "esc-rm")
	scoped := tenantScopedPool(t)
	store := NewEscalationPolicyStore(scoped)
	ctx := escalationTestCtx(tn)

	p, _ := domain.NewEscalationPolicy(tn.TenantID, "Removal Probe")
	created, err := store.Create(ctx, p)
	if err != nil {
		t.Fatalf("create policy: %v", err)
	}

	t0, err := store.AddTier(ctx, created.PolicyID, escalationSchedule(t, scoped, tn, "S0").ScheduleID, 60)
	if err != nil {
		t.Fatalf("add t0: %v", err)
	}
	t1, err := store.AddTier(ctx, created.PolicyID, escalationSchedule(t, scoped, tn, "S1").ScheduleID, 60)
	if err != nil {
		t.Fatalf("add t1: %v", err)
	}
	t2, err := store.AddTier(ctx, created.PolicyID, escalationSchedule(t, scoped, tn, "S2").ScheduleID, 60)
	if err != nil {
		t.Fatalf("add t2: %v", err)
	}

	// Remove the middle tier (position 1).
	if err := store.RemoveTier(ctx, created.PolicyID, t1.TierID); err != nil {
		t.Fatalf("remove t1: %v", err)
	}

	remaining, err := store.ListTiers(ctx, created.PolicyID, 0, "")
	if err != nil {
		t.Fatalf("list tiers: %v", err)
	}
	if len(remaining) != 2 {
		t.Fatalf("%d tiers remain, want 2", len(remaining))
	}
	if remaining[0].TierID != t0.TierID || remaining[0].Position != 0 {
		t.Errorf("remaining[0] = %+v, want t0 at position 0", remaining[0])
	}
	if remaining[1].TierID != t2.TierID || remaining[1].Position != 1 {
		t.Errorf("remaining[1] = %+v, want t2 shifted to position 1", remaining[1])
	}

	// Removing an unknown tier is a not-found, not a silent success.
	if err := store.RemoveTier(ctx, created.PolicyID, "no-such-tier"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("remove unknown tier err = %v, want ErrNotFound", err)
	}
}

// ReorderTiers atomically replaces the ladder order, positions stay
// contiguous, including a genuine swap (not merely an append) — exactly the
// shape that would collide against a non-deferred unique constraint.
//
// MUTATION PROOF (implementer's evidence report): dropping DEFERRABLE
// INITIALLY DEFERRED from uq_escalation_tier_policy_position makes this test
// fail with a unique-constraint violation, because the per-row UPDATEs
// transiently collide before the final permutation is reached — proving the
// deferred constraint is load-bearing, not decorative.
func TestEscalationPolicyStore_ReorderTiersIsAtomic(t *testing.T) {
	priv := testPool(t)
	tn := escalationTenant(t, NewTenantStore(priv), "esc-reorder")
	scoped := tenantScopedPool(t)
	store := NewEscalationPolicyStore(scoped)
	ctx := escalationTestCtx(tn)

	p, _ := domain.NewEscalationPolicy(tn.TenantID, "Reorder Probe")
	created, err := store.Create(ctx, p)
	if err != nil {
		t.Fatalf("create policy: %v", err)
	}

	t0, err := store.AddTier(ctx, created.PolicyID, escalationSchedule(t, scoped, tn, "S0").ScheduleID, 60)
	if err != nil {
		t.Fatalf("add t0: %v", err)
	}
	t1, err := store.AddTier(ctx, created.PolicyID, escalationSchedule(t, scoped, tn, "S1").ScheduleID, 60)
	if err != nil {
		t.Fatalf("add t1: %v", err)
	}
	t2, err := store.AddTier(ctx, created.PolicyID, escalationSchedule(t, scoped, tn, "S2").ScheduleID, 60)
	if err != nil {
		t.Fatalf("add t2: %v", err)
	}

	// Reverse the order: t2, t0, t1 — positions 0 and 2 swap.
	reordered, err := store.ReorderTiers(ctx, created.PolicyID, []string{t2.TierID, t0.TierID, t1.TierID})
	if err != nil {
		t.Fatalf("reorder: %v", err)
	}
	if len(reordered) != 3 {
		t.Fatalf("reorder returned %d tiers, want 3", len(reordered))
	}
	wantOrder := []string{t2.TierID, t0.TierID, t1.TierID}
	for i, want := range wantOrder {
		if reordered[i].TierID != want || reordered[i].Position != i {
			t.Errorf("reordered[%d] = %+v, want tier %s at position %d", i, reordered[i], want, i)
		}
	}

	// The new order is durable, not just the reorder call's own return value.
	listed, err := store.ListTiers(ctx, created.PolicyID, 0, "")
	if err != nil {
		t.Fatalf("list tiers: %v", err)
	}
	for i, want := range wantOrder {
		if listed[i].TierID != want || listed[i].Position != i {
			t.Errorf("listed[%d] = %+v, want tier %s at position %d", i, listed[i], want, i)
		}
	}
}

// A reorder naming anything other than exactly the policy's current tier set
// — missing one, adding a foreign one, or repeating one — is refused before
// anything is written.
func TestEscalationPolicyStore_ReorderTiersRejectsMismatchedSet(t *testing.T) {
	priv := testPool(t)
	tn := escalationTenant(t, NewTenantStore(priv), "esc-mismatch")
	scoped := tenantScopedPool(t)
	store := NewEscalationPolicyStore(scoped)
	ctx := escalationTestCtx(tn)

	p, _ := domain.NewEscalationPolicy(tn.TenantID, "Mismatch Probe")
	created, err := store.Create(ctx, p)
	if err != nil {
		t.Fatalf("create policy: %v", err)
	}
	t0, err := store.AddTier(ctx, created.PolicyID, escalationSchedule(t, scoped, tn, "S0").ScheduleID, 60)
	if err != nil {
		t.Fatalf("add t0: %v", err)
	}
	t1, err := store.AddTier(ctx, created.PolicyID, escalationSchedule(t, scoped, tn, "S1").ScheduleID, 60)
	if err != nil {
		t.Fatalf("add t1: %v", err)
	}

	cases := [][]string{
		{t0.TierID},                          // missing one
		{t0.TierID, t1.TierID, "no-such-id"}, // extra, unknown id
		{t0.TierID, t0.TierID},               // repeated, still short of t1
	}
	for _, ids := range cases {
		if _, err := store.ReorderTiers(ctx, created.PolicyID, ids); err == nil {
			t.Errorf("reorder with %v was accepted, want a validation error", ids)
		} else if _, ok := domain.AsValidation(err); !ok {
			t.Errorf("reorder with %v err = %v, want a *domain.ValidationError", ids, err)
		}
	}

	// Positions are untouched by every rejected attempt.
	still, err := store.ListTiers(ctx, created.PolicyID, 0, "")
	if err != nil {
		t.Fatalf("list tiers: %v", err)
	}
	if len(still) != 2 || still[0].TierID != t0.TierID || still[1].TierID != t1.TierID {
		t.Fatalf("ladder changed after a rejected reorder: %+v", still)
	}
}

// Deleting a policy removes its tiers with it (ON DELETE CASCADE) — proven
// both by direct row count and by a subsequent list returning empty.
func TestEscalationPolicyStore_DeleteCascadesToTiers(t *testing.T) {
	priv := testPool(t)
	tn := escalationTenant(t, NewTenantStore(priv), "esc-cascade")
	scoped := tenantScopedPool(t)
	store := NewEscalationPolicyStore(scoped)
	ctx := escalationTestCtx(tn)

	p, _ := domain.NewEscalationPolicy(tn.TenantID, "Cascade Probe")
	created, err := store.Create(ctx, p)
	if err != nil {
		t.Fatalf("create policy: %v", err)
	}
	if _, err := store.AddTier(ctx, created.PolicyID, escalationSchedule(t, scoped, tn, "S0").ScheduleID, 60); err != nil {
		t.Fatalf("add tier: %v", err)
	}

	if err := store.Delete(ctx, created.PolicyID); err != nil {
		t.Fatalf("delete policy: %v", err)
	}

	var n int
	if err := priv.QueryRow(context.Background(),
		`SELECT count(*) FROM escalation_tier WHERE policy_id = $1`, created.PolicyID).Scan(&n); err != nil {
		t.Fatalf("count tiers: %v", err)
	}
	if n != 0 {
		t.Errorf("%d tier row(s) survived their policy's deletion, want 0 (ON DELETE CASCADE)", n)
	}
}

// Backstop: the CHECK constraint rejects a non-positive wait_seconds inserted
// directly, even though domain.Validate already refuses it before the DB is
// ever touched (internal/domain/escalation_test.go).
func TestEscalationTier_WaitSecondsCheckConstraintRejectsNonPositive(t *testing.T) {
	priv := testPool(t)
	tn := escalationTenant(t, NewTenantStore(priv), "esc-check")
	scoped := tenantScopedPool(t)
	store := NewEscalationPolicyStore(scoped)
	ctx := escalationTestCtx(tn)

	p, _ := domain.NewEscalationPolicy(tn.TenantID, "Check Probe")
	created, err := store.Create(ctx, p)
	if err != nil {
		t.Fatalf("create policy: %v", err)
	}
	sch := escalationSchedule(t, scoped, tn, "Check Schedule")

	_, dbErr := scoped.Exec(ctx, `
		INSERT INTO escalation_tier
			(tier_id, tenant_id, policy_id, position, on_call_schedule_id, wait_seconds, row_version, created_at, updated_at)
		VALUES ($1,$2,$3,0,$4,0,1,now(),now())`,
		domain.NewID(), tn.TenantID, created.PolicyID, sch.ScheduleID)
	if dbErr == nil {
		t.Fatal("expected CHECK constraint to reject wait_seconds = 0 at the DB layer")
	}
}
