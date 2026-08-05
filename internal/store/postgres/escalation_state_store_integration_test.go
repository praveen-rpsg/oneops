//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/escalation"
)

// escalationEngineFixture is the scaffolding E5.2b-2's store tests need: a
// real tenant, a tenant-scoped AssetStore/IncidentStore/EscalationPolicyStore
// for setting up CIs, incidents and policies through the normal write paths,
// and the privileged EscalationStateStore itself — mirroring
// groupingFixture's shape (incident_grouping_store_integration_test.go).
type escalationEngineFixture struct {
	t        *testing.T
	tn       *domain.Tenant
	ctx      context.Context
	assets   *AssetStore
	policies *EscalationPolicyStore
	privInc  *IncidentStore // privileged pool: FindOrCreateOpenAlertIncident
	store    *EscalationStateStore
	priv     *pgxpool.Pool
}

func newEscalationEngineFixture(t *testing.T, slug string) *escalationEngineFixture {
	t.Helper()
	testPool(t)
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), slug)
	scoped := tenantScopedPool(t)
	return &escalationEngineFixture{
		t:        t,
		tn:       tn,
		ctx:      assetTestCtx(tn),
		assets:   NewAssetStore(scoped),
		policies: NewEscalationPolicyStore(scoped),
		privInc:  NewIncidentStore(priv),
		store:    NewEscalationStateStore(priv),
		priv:     priv,
	}
}

func (f *escalationEngineFixture) asset(name string) *domain.Asset {
	f.t.Helper()
	a, err := domain.NewAsset(f.tn.TenantID, "server", name, nil)
	if err != nil {
		f.t.Fatalf("new asset %s: %v", name, err)
	}
	created, err := f.assets.Create(f.ctx, a)
	if err != nil {
		f.t.Fatalf("create asset %s: %v", name, err)
	}
	return created
}

// openAlertIncident creates a real OPEN, alert-sourced incident via the same
// FindOrCreateOpenAlertIncident path the evaluator uses (E4.1).
func (f *escalationEngineFixture) openAlertIncident(asset *domain.Asset, title string) string {
	f.t.Helper()
	want, err := domain.NewAlertIncident(f.tn.TenantID, title, "", domain.IncidentSeverityCritical, asset.AssetID)
	if err != nil {
		f.t.Fatalf("new alert incident: %v", err)
	}
	id, err := f.privInc.FindOrCreateOpenAlertIncident(context.Background(), want, "test-actor", "alert fired")
	if err != nil {
		f.t.Fatalf("find-or-create alert incident: %v", err)
	}
	return id
}

// activePolicy creates a real, active escalation_policy for this tenant.
func (f *escalationEngineFixture) activePolicy(name string) *domain.EscalationPolicy {
	f.t.Helper()
	p, err := domain.NewEscalationPolicy(f.tn.TenantID, name)
	if err != nil {
		f.t.Fatalf("new policy %s: %v", name, err)
	}
	created, err := f.policies.Create(f.ctx, p)
	if err != nil {
		f.t.Fatalf("create policy %s: %v", name, err)
	}
	return created
}

// ---- Seed -------------------------------------------------------------

func TestEscalationStateStore_SeedEnrollsOpenUnackedAlertIncidentAtTenantDefaultPolicy(t *testing.T) {
	f := newEscalationEngineFixture(t, "esc-seed")
	pol := f.activePolicy("Default")
	inc := f.openAlertIncident(f.asset("host-1"), "cpu high")

	seeded, skipped, err := f.store.Seed(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if seeded < 1 {
		t.Fatalf("seeded = %d, want at least 1", seeded)
	}
	_ = skipped

	var (
		tenantID, policyID, status string
		tierIndex                  int
	)
	if err := f.priv.QueryRow(context.Background(),
		`SELECT tenant_id, policy_id, current_tier_index, status FROM escalation_state WHERE incident_id = $1`,
		inc).Scan(&tenantID, &policyID, &tierIndex, &status); err != nil {
		t.Fatalf("read seeded row: %v", err)
	}
	if tenantID != f.tn.TenantID {
		t.Errorf("tenant_id = %q, want %q", tenantID, f.tn.TenantID)
	}
	if policyID != pol.PolicyID {
		t.Errorf("policy_id = %q, want %q", policyID, pol.PolicyID)
	}
	if tierIndex != 0 {
		t.Errorf("current_tier_index = %d, want 0", tierIndex)
	}
	if status != "active" {
		t.Errorf("status = %q, want active", status)
	}

	// Idempotent: seeding again finds nothing new for this incident.
	seeded2, _, err := f.store.Seed(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if seeded2 != 0 {
		t.Errorf("second seed pass seeded %d rows, want 0 (already enrolled)", seeded2)
	}
}

func TestEscalationStateStore_SeedPicksEarliestActivePolicyWhenMultiple(t *testing.T) {
	f := newEscalationEngineFixture(t, "esc-seed-multi")
	older := f.activePolicy("Older")
	// Force a distinct, controllable created_at ordering rather than relying
	// on two now() calls landing in different instants under a fast test.
	if _, err := f.priv.Exec(context.Background(),
		`UPDATE escalation_policy SET created_at = created_at - interval '1 hour' WHERE policy_id = $1`, older.PolicyID); err != nil {
		t.Fatalf("backdate older policy: %v", err)
	}
	f.activePolicy("Newer")
	inc := f.openAlertIncident(f.asset("host-1"), "cpu high")

	if _, _, err := f.store.Seed(context.Background(), time.Now().UTC()); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var policyID string
	if err := f.priv.QueryRow(context.Background(),
		`SELECT policy_id FROM escalation_state WHERE incident_id = $1`, inc).Scan(&policyID); err != nil {
		t.Fatalf("read seeded row: %v", err)
	}
	if policyID != older.PolicyID {
		t.Errorf("policy_id = %q, want the earliest-created policy %q", policyID, older.PolicyID)
	}
}

func TestEscalationStateStore_SeedSkipsTenantWithNoActivePolicy(t *testing.T) {
	f := newEscalationEngineFixture(t, "esc-seed-nopolicy")
	inc := f.openAlertIncident(f.asset("host-1"), "cpu high") // no policy created at all

	seeded, skipped, err := f.store.Seed(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if seeded != 0 {
		t.Errorf("seeded = %d, want 0 (no active policy)", seeded)
	}
	if skipped < 1 {
		t.Errorf("skippedNoPolicy = %d, want at least 1", skipped)
	}

	var count int
	if err := f.priv.QueryRow(context.Background(),
		`SELECT count(*) FROM escalation_state WHERE incident_id = $1`, inc).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("a row was seeded for a tenant with no active policy: %d", count)
	}
}

func TestEscalationStateStore_SeedSkipsArchivedPolicy(t *testing.T) {
	f := newEscalationEngineFixture(t, "esc-seed-archived")
	p := f.activePolicy("Once Active")
	archived := domain.EscalationPolicyArchived
	if _, err := f.policies.Update(f.ctx, p.PolicyID, p.RowVersion, domain.EscalationPolicyPatch{Status: &archived}); err != nil {
		t.Fatalf("archive policy: %v", err)
	}
	f.openAlertIncident(f.asset("host-1"), "cpu high")

	seeded, skipped, err := f.store.Seed(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if seeded != 0 || skipped < 1 {
		t.Errorf("seeded=%d skipped=%d, want 0 / >=1 — an archived policy is not a default", seeded, skipped)
	}
}

func TestEscalationStateStore_SeedIgnoresManualAndAckedIncidents(t *testing.T) {
	f := newEscalationEngineFixture(t, "esc-seed-ignore")
	f.activePolicy("Default")

	// A manual incident: never seeded, regardless of policy.
	manual, err := domain.NewIncident(f.tn.TenantID, "manual issue", "", domain.IncidentSeverityLow, nil, nil)
	if err != nil {
		t.Fatalf("new manual incident: %v", err)
	}
	scoped := tenantScopedPool(t)
	manualCreated, err := NewIncidentStore(scoped).Create(domain.WithActor(f.ctx, "tester"), manual)
	if err != nil {
		t.Fatalf("create manual incident: %v", err)
	}

	// An already-acknowledged alert incident (simulate by transitioning it).
	ackedID := f.openAlertIncident(f.asset("host-2"), "disk full")
	if _, err := NewIncidentStore(scoped).SetStatus(domain.WithActor(f.ctx, "tester"), ackedID, 1, domain.IncidentAcknowledged); err != nil {
		t.Fatalf("acknowledge incident: %v", err)
	}

	if _, _, err := f.store.Seed(context.Background(), time.Now().UTC()); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var count int
	if err := f.priv.QueryRow(context.Background(),
		`SELECT count(*) FROM escalation_state WHERE incident_id = ANY($1)`,
		[]string{manualCreated.IncidentID, ackedID}).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("a manual or already-acknowledged incident was seeded: %d rows", count)
	}
}

// ---- ClaimDue / Advance / MarkTerminal, fenced -----------------------------

func TestEscalationStateStore_ClaimDueClaimsAndFencesTheOutcome(t *testing.T) {
	f := newEscalationEngineFixture(t, "esc-claim")
	f.activePolicy("Default")
	inc := f.openAlertIncident(f.asset("host-1"), "cpu high")
	if _, _, err := f.store.Seed(context.Background(), time.Now().UTC()); err != nil {
		t.Fatalf("seed: %v", err)
	}

	now := time.Now().UTC()
	claimed, err := f.store.ClaimDue(context.Background(), now, time.Minute, 10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	var mine *domain.EscalationState
	for _, c := range claimed {
		if c.IncidentID == inc {
			mine = c
		}
	}
	if mine == nil {
		t.Fatal("the freshly seeded row was not claimed")
	}
	if mine.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", mine.Attempts)
	}
	if mine.ClaimedAt.IsZero() {
		t.Error("claimed_at was not stamped")
	}

	// A second claim pass within the lease must not re-claim it.
	claimed2, err := f.store.ClaimDue(context.Background(), now, time.Minute, 10)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	for _, c := range claimed2 {
		if c.IncidentID == inc {
			t.Fatal("a row already claimed within its lease was claimed a second time")
		}
	}

	// A stale claim token must be fenced off.
	staleToken := mine.ClaimedAt.Add(-time.Hour)
	if err := f.store.Advance(context.Background(), mine.StateID, staleToken, 1, now.Add(time.Minute)); !errors.Is(err, escalation.ErrStaleClaim) {
		t.Errorf("advance with a stale token: err = %v, want ErrStaleClaim", err)
	}

	// The correct token advances the row and clears the claim.
	if err := f.store.Advance(context.Background(), mine.StateID, mine.ClaimedAt, 1, now.Add(time.Minute)); err != nil {
		t.Fatalf("advance: %v", err)
	}
	var tierIndex int
	var claimedAt *time.Time
	if err := f.priv.QueryRow(context.Background(),
		`SELECT current_tier_index, claimed_at FROM escalation_state WHERE state_id = $1`, mine.StateID,
	).Scan(&tierIndex, &claimedAt); err != nil {
		t.Fatalf("read: %v", err)
	}
	if tierIndex != 1 {
		t.Errorf("current_tier_index = %d, want 1", tierIndex)
	}
	if claimedAt != nil {
		t.Error("claimed_at was not cleared by Advance")
	}
}

func TestEscalationStateStore_MarkTerminal_FencedAndClearsClaim(t *testing.T) {
	f := newEscalationEngineFixture(t, "esc-terminal")
	f.activePolicy("Default")
	f.openAlertIncident(f.asset("host-1"), "cpu high")
	if _, _, err := f.store.Seed(context.Background(), time.Now().UTC()); err != nil {
		t.Fatalf("seed: %v", err)
	}

	claimed, err := f.store.ClaimDue(context.Background(), time.Now().UTC(), time.Minute, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %v, %d rows", err, len(claimed))
	}
	st := claimed[0]

	staleToken := st.ClaimedAt.Add(-time.Hour)
	if err := f.store.MarkTerminal(context.Background(), st.StateID, staleToken, domain.EscalationStateAcked); !errors.Is(err, escalation.ErrStaleClaim) {
		t.Errorf("mark terminal with a stale token: err = %v, want ErrStaleClaim", err)
	}

	if err := f.store.MarkTerminal(context.Background(), st.StateID, st.ClaimedAt, domain.EscalationStateAcked); err != nil {
		t.Fatalf("mark terminal: %v", err)
	}
	var status string
	var claimedAt *time.Time
	if err := f.priv.QueryRow(context.Background(),
		`SELECT status, claimed_at FROM escalation_state WHERE state_id = $1`, st.StateID,
	).Scan(&status, &claimedAt); err != nil {
		t.Fatalf("read: %v", err)
	}
	if status != "acked" {
		t.Errorf("status = %q, want acked", status)
	}
	if claimedAt != nil {
		t.Error("claimed_at was not cleared by MarkTerminal")
	}
}

// ---- tenant isolation (mutation-verified) ----------------------------------

// TestEscalationStateStore_RLSIsolatesByTenant proves FORCE ROW LEVEL
// SECURITY actually confines escalation_state, not merely that the schema
// declares it: a connection bound to tenant B must see NONE of tenant A's
// rows, even naming tenant A's real state_id directly.
func TestEscalationStateStore_RLSIsolatesByTenant(t *testing.T) {
	fa := newEscalationEngineFixture(t, "esc-eng-rls-a")
	fa.activePolicy("Default")
	incA := fa.openAlertIncident(fa.asset("host-a"), "cpu high")

	fb := newEscalationEngineFixture(t, "esc-eng-rls-b")
	fb.activePolicy("Default")
	fb.openAlertIncident(fb.asset("host-b"), "cpu high")

	if _, _, err := fa.store.Seed(context.Background(), time.Now().UTC()); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var stateIDA string
	if err := fa.priv.QueryRow(context.Background(),
		`SELECT state_id FROM escalation_state WHERE incident_id = $1`, incA).Scan(&stateIDA); err != nil {
		t.Fatalf("read tenant A's state_id: %v", err)
	}

	scopedB := tenantScopedPool(t)
	var count int
	if err := scopedB.QueryRow(domain.WithTenant(context.Background(), fb.tn),
		`SELECT count(*) FROM escalation_state WHERE state_id = $1`, stateIDA).Scan(&count); err != nil {
		t.Fatalf("query as tenant B: %v", err)
	}
	if count != 0 {
		t.Fatalf("tenant B's connection read tenant A's escalation_state row — RLS did not isolate it")
	}

	scopedA := tenantScopedPool(t)
	if err := scopedA.QueryRow(domain.WithTenant(context.Background(), fa.tn),
		`SELECT count(*) FROM escalation_state WHERE state_id = $1`, stateIDA).Scan(&count); err != nil {
		t.Fatalf("query as tenant A: %v", err)
	}
	if count != 1 {
		t.Fatalf("tenant A's own connection could not read its own row: count = %d", count)
	}
}

// TestEscalationStateStore_UniqueConstraintPreventsSecondRowPerIncident
// mutation-verifies the CTO-locked invariant directly against the database:
// at most one escalation_state row per incident.
func TestEscalationStateStore_UniqueConstraintPreventsSecondRowPerIncident(t *testing.T) {
	f := newEscalationEngineFixture(t, "esc-unique")
	pol := f.activePolicy("Default")
	inc := f.openAlertIncident(f.asset("host-1"), "cpu high")
	if _, _, err := f.store.Seed(context.Background(), time.Now().UTC()); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := f.priv.Exec(context.Background(), `
		INSERT INTO escalation_state
			(state_id, tenant_id, incident_id, policy_id, current_tier_index, next_attempt_at, status, attempts, row_version, created_at, updated_at)
		VALUES ($1,$2,$3,$4,0,now(),'active',0,1,now(),now())`,
		domain.NewID(), f.tn.TenantID, inc, pol.PolicyID)
	if err == nil {
		t.Fatal("a second escalation_state row for the same incident was accepted")
	}
	if !isUniqueViolation(err) {
		t.Errorf("insert failed with %v, want a unique_violation", err)
	}
}
