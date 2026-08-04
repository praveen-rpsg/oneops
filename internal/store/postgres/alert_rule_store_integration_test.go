//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

func TestAlertRuleStore_CreateGetListUpdateDelete(t *testing.T) {
	testPool(t) // ensures migrations are applied before the scoped pool is used
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "alert-rule-crud")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	rules := NewAlertRuleStore(scoped)
	ctx := assetTestCtx(tn)

	host := telemetryAsset(t, assets, ctx, tn.TenantID, "alert-host")

	r, err := domain.NewAlertRule(tn.TenantID, host.AssetID, "cpu_utilization", domain.ComparatorGT, 90, 300, domain.AlertSeverityCritical)
	if err != nil {
		t.Fatalf("new alert rule: %v", err)
	}
	created, err := rules.Create(ctx, r)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.RowVersion != 1 {
		t.Errorf("row_version = %d, want 1", created.RowVersion)
	}
	if created.LastState != domain.AlertRuleStateOK || !created.LastTransitionAt.IsZero() {
		t.Errorf("a new rule must start at ok/never-transitioned: %+v", created)
	}

	got, err := rules.Get(ctx, created.RuleID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Threshold != 90 || got.Comparator != domain.ComparatorGT || got.ForDuration != 300 {
		t.Errorf("get returned %+v, want the created fields", got)
	}

	list, err := rules.List(ctx, 0, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, item := range list {
		if item.RuleID == created.RuleID {
			found = true
		}
	}
	if !found {
		t.Errorf("list did not include the created rule: %+v", list)
	}

	newThreshold := 95.0
	updated, err := rules.Update(ctx, created.RuleID, created.RowVersion, domain.AlertRulePatch{Threshold: &newThreshold})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Threshold != newThreshold || updated.RowVersion != 2 {
		t.Errorf("update = %+v, want threshold %v and row_version 2", updated, newThreshold)
	}

	// A stale row_version is refused.
	if _, err := rules.Update(ctx, created.RuleID, created.RowVersion, domain.AlertRulePatch{Threshold: &newThreshold}); err != domain.ErrVersionMismatch {
		t.Errorf("stale update err = %v, want ErrVersionMismatch", err)
	}

	if err := rules.Delete(ctx, created.RuleID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := rules.Get(ctx, created.RuleID); err != domain.ErrNotFound {
		t.Errorf("get after delete err = %v, want ErrNotFound", err)
	}
	if err := rules.Delete(ctx, created.RuleID); err != domain.ErrNotFound {
		t.Errorf("delete of an already-deleted rule err = %v, want ErrNotFound", err)
	}
}

// TestAlertRuleStore_CreateRejectsCrossTenantOrNonexistentAsset proves
// ADR-ASSET-001 §6's defense extended to alert_rule: a rule cannot be created
// against another tenant's asset, or against an asset_id that does not exist
// anywhere — both return ErrNotFound, not a cross-tenant row.
func TestAlertRuleStore_CreateRejectsCrossTenantOrNonexistentAsset(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a, err := tenants.Create(adminTestCtx(), newTenant("ar-rej-alpha", "ext-ar-rej-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(adminTestCtx(), newTenant("ar-rej-bravo", "ext-ar-rej-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	rules := NewAlertRuleStore(scoped)
	ctxA := assetTestCtx(a)
	ctxB := assetTestCtx(b)

	victim := telemetryAsset(t, assets, ctxA, a.TenantID, "victim-host")

	// Tenant B names tenant A's real asset.
	cross, err := domain.NewAlertRule(b.TenantID, victim.AssetID, "cpu_utilization", domain.ComparatorGT, 90, 60, domain.AlertSeverityWarning)
	if err != nil {
		t.Fatalf("new alert rule: %v", err)
	}
	if _, err := rules.Create(ctxB, cross); err != domain.ErrNotFound {
		t.Errorf("cross-tenant asset_id err = %v, want ErrNotFound", err)
	}

	// Tenant B names an asset id that does not exist anywhere.
	missing, err := domain.NewAlertRule(b.TenantID, "no-such-asset", "cpu_utilization", domain.ComparatorGT, 90, 60, domain.AlertSeverityWarning)
	if err != nil {
		t.Fatalf("new alert rule: %v", err)
	}
	if _, err := rules.Create(ctxB, missing); err != domain.ErrNotFound {
		t.Errorf("nonexistent asset_id err = %v, want ErrNotFound", err)
	}

	// Nothing was written naming the victim's asset.
	list, err := rules.List(ctxA, 0, "")
	if err != nil {
		t.Fatalf("list as tenant a: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("a cross-tenant rule was written: %+v", list)
	}
}

// TestAlertRuleIsolation_RLSByTenant proves two-tenant isolation bites: tenant
// B can never see tenant A's rules, even by the exact rule_id.
func TestAlertRuleIsolation_RLSByTenant(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a, err := tenants.Create(adminTestCtx(), newTenant("ar-iso-alpha", "ext-ar-iso-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(adminTestCtx(), newTenant("ar-iso-bravo", "ext-ar-iso-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	rules := NewAlertRuleStore(scoped)
	ctxA := assetTestCtx(a)
	ctxB := assetTestCtx(b)

	hostA := telemetryAsset(t, assets, ctxA, a.TenantID, "iso-host-a")

	rA, err := domain.NewAlertRule(a.TenantID, hostA.AssetID, "cpu_utilization", domain.ComparatorGT, 90, 60, domain.AlertSeverityWarning)
	if err != nil {
		t.Fatalf("new alert rule: %v", err)
	}
	createdA, err := rules.Create(ctxA, rA)
	if err != nil {
		t.Fatalf("create as tenant a: %v", err)
	}

	if _, err := rules.Get(ctxB, createdA.RuleID); err != domain.ErrNotFound {
		t.Errorf("tenant B read tenant A's rule by id: err = %v, want ErrNotFound", err)
	}
	listB, err := rules.List(ctxB, 0, "")
	if err != nil {
		t.Fatalf("list as tenant b: %v", err)
	}
	if len(listB) != 0 {
		t.Errorf("tenant B saw tenant A's rules: %+v", listB)
	}
	if err := rules.Delete(ctxB, createdA.RuleID); err != domain.ErrNotFound {
		t.Errorf("tenant B deleted tenant A's rule: err = %v, want ErrNotFound", err)
	}

	// Tenant A still sees its own rule, undisturbed.
	stillThere, err := rules.Get(ctxA, createdA.RuleID)
	if err != nil || stillThere.RuleID != createdA.RuleID {
		t.Fatalf("tenant A lost its own rule: %v, %+v", err, stillThere)
	}
}

// TestAlertRuleStore_EnabledRulesAndRecordTransition exercises the
// evaluator's privileged surface directly: EnabledRules excludes disabled
// rules and crosses tenants, and RecordTransition is a fenced CAS that
// refuses a stale row_version.
func TestAlertRuleStore_EnabledRulesAndRecordTransition(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "alert-rule-eval")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	scopedRules := NewAlertRuleStore(scoped)
	ctx := assetTestCtx(tn)
	host := telemetryAsset(t, assets, ctx, tn.TenantID, "eval-host")

	mk := func(enabled bool) *domain.AlertRule {
		r, err := domain.NewAlertRule(tn.TenantID, host.AssetID, "cpu_utilization", domain.ComparatorGT, 90, 60, domain.AlertSeverityWarning)
		if err != nil {
			t.Fatalf("new alert rule: %v", err)
		}
		r.Enabled = enabled
		created, err := scopedRules.Create(ctx, r)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		return created
	}

	enabled := mk(true)
	disabled := mk(false)

	privRules := NewAlertRuleStore(priv)
	due, err := privRules.EnabledRules(context.Background(), 100, "")
	if err != nil {
		t.Fatalf("enabled rules: %v", err)
	}
	ids := map[string]bool{}
	for _, r := range due {
		ids[r.RuleID] = true
	}
	if !ids[enabled.RuleID] {
		t.Error("an enabled rule must appear in EnabledRules")
	}
	if ids[disabled.RuleID] {
		t.Error("a disabled rule must never appear in EnabledRules")
	}

	now := time.Now().UTC()
	updated, err := privRules.RecordTransition(context.Background(), enabled.RuleID, enabled.RowVersion, domain.AlertRuleStateFiring, now, nil)
	if err != nil {
		t.Fatalf("record transition: %v", err)
	}
	if updated.LastState != domain.AlertRuleStateFiring || !updated.LastTransitionAt.Equal(now) {
		t.Errorf("record transition = %+v, want firing at %v", updated, now)
	}
	if updated.CurrentIncidentID != nil {
		t.Errorf("current_incident_id = %v, want nil (RecordTransition was given nil)", updated.CurrentIncidentID)
	}

	// A stale row_version (the pre-transition one) is refused, not silently
	// re-applied — the evaluator's concurrent-edit defense.
	if _, err := privRules.RecordTransition(context.Background(), enabled.RuleID, enabled.RowVersion, domain.AlertRuleStateOK, now, nil); err != domain.ErrVersionMismatch {
		t.Errorf("stale transition err = %v, want ErrVersionMismatch", err)
	}
}

// TestTelemetryStore_QueryRangeForTenantIsolatesTenant is the store-level
// proof for E3.1's #1 risk: even on the PRIVILEGED pool (no row-level
// security), QueryRangeForTenant returns only the named tenant's samples.
//
// Two tenants' samples are written for the SAME asset_id/metric — a
// collision a real, globally-unique (ULID) asset id would never itself
// produce, but the isolation this method provides must not depend on that:
// it is a SQL predicate, not an accident of unique ids. Directly inserting
// both tenants' rows against one shared asset_id, bypassing the app-level
// uniqueness that would normally prevent the collision, proves the defense
// is real rather than incidental.
//
// Mutation-verified by hand: removing "tenant_id = $1" from
// TelemetryStore.QueryRangeForTenant's WHERE clause makes this test fail —
// tenant B's samples appear in tenant A's query result.
func TestTelemetryStore_QueryRangeForTenantIsolatesTenant(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a, err := tenants.Create(adminTestCtx(), newTenant("tel-iso-alpha", "ext-tel-iso-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(adminTestCtx(), newTenant("tel-iso-bravo", "ext-tel-iso-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	// telemetry_sample.asset_id has a real FK to asset (ADR-ASSET-001 §6), so
	// the shared id under test must name an actual row — created under
	// tenant A, then also used, deliberately, by a raw insert labelled
	// tenant B below. The FK's own privileges bypass row-level security on
	// asset (the same fact WriteSamples' doc comment records), which is
	// exactly the crack this test's precondition exploits on purpose.
	scoped := tenantScopedPool(t)
	sharedAssetRow := telemetryAsset(t, NewAssetStore(scoped), assetTestCtx(a), a.TenantID, "shared-isolation-host")
	sharedAsset := sharedAssetRow.AssetID
	const metric = "cpu_utilization"
	// This package's telemetry fixtures cluster around 2026-08-01..03 (see
	// telemetry_store_integration_test.go, telemetry_rollup_integration_test.go);
	// TelemetryRollupWorker.MaterializeRange aggregates telemetry_sample with
	// NO tenant/asset filter at all, across whatever window it is given, so a
	// fixture sharing that window inflates an unrelated rollup test's bucket
	// count (proven live: reusing 2026-08-01T00:00:00Z here made
	// TestTelemetryRollupWorker_MaterializesAvgMinMaxCount see 4 buckets
	// instead of 2). A distant, unused year avoids the collision without
	// coordinating fixture times across files.
	base := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)

	// Raw inserts on the privileged pool: this deliberately bypasses
	// WriteSamples' asset-ownership re-verification (ADR-ASSET-001 §6) to
	// construct the adversarial precondition — two tenants' rows sharing one
	// asset_id — that QueryRangeForTenant must still isolate correctly.
	insert := func(tenantID string, value float64) {
		if _, err := priv.Exec(context.Background(), `
			INSERT INTO telemetry_sample (tenant_id, asset_id, metric, ts, value, labels)
			VALUES ($1, $2, $3, $4, $5, '{}')`,
			tenantID, sharedAsset, metric, base, value); err != nil {
			t.Fatalf("insert sample for %s: %v", tenantID, err)
		}
	}
	insert(a.TenantID, 111)
	insert(b.TenantID, 222)

	store := NewTelemetryStore(priv)
	gotA, err := store.QueryRangeForTenant(context.Background(), a.TenantID, sharedAsset, metric, base.Add(-time.Minute), base.Add(time.Minute))
	if err != nil {
		t.Fatalf("query range for tenant a: %v", err)
	}
	if len(gotA) != 1 || gotA[0].Value != 111 || gotA[0].TenantID != a.TenantID {
		t.Fatalf("tenant a's query returned %+v, want exactly its own sample (value=111)", gotA)
	}

	gotB, err := store.QueryRangeForTenant(context.Background(), b.TenantID, sharedAsset, metric, base.Add(-time.Minute), base.Add(time.Minute))
	if err != nil {
		t.Fatalf("query range for tenant b: %v", err)
	}
	if len(gotB) != 1 || gotB[0].Value != 222 || gotB[0].TenantID != b.TenantID {
		t.Fatalf("tenant b's query returned %+v, want exactly its own sample (value=222)", gotB)
	}
}

// TestTelemetryStore_QueryRangeForTenantReturnsMostRecentWhenWindowExceedsCap
// is the store-level bite test for the spurious-alert fix: a window whose
// true sample count exceeds domain.MaxTelemetryQueryLimit must be answered
// with the MOST RECENT cap-worth of samples, not the oldest.
//
// domain.MaxTelemetryQueryLimit+50 samples are bulk-inserted one second
// apart, breaching for all but the newest 50 (a recent recovery). If the
// query still favoured the oldest rows (the pre-fix `ORDER BY ts ASC LIMIT
// N`), every one of the MaxTelemetryQueryLimit rows returned would be a
// pre-recovery, still-breaching value and the recovery would never be
// visible at all — exactly the spurious-alert bug this fix closes.
func TestTelemetryStore_QueryRangeForTenantReturnsMostRecentWhenWindowExceedsCap(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "tel-cap-recent")

	scoped := tenantScopedPool(t)
	asset := telemetryAsset(t, NewAssetStore(scoped), assetTestCtx(tn), tn.TenantID, "cap-recent-host")

	const metric = "cpu_utilization"
	const total = domain.MaxTelemetryQueryLimit + 50
	const recoveredTail = 50
	// A distant, unused base avoids colliding with the rollup worker's
	// global, unfiltered aggregation window — see the isolation test above.
	base := time.Date(2032, 1, 1, 0, 0, 0, 0, time.UTC)

	// Bulk insert via generate_series: `total` one-row INSERTs would dominate
	// this test's runtime for no reason relevant to what it proves. Value is
	// 95 (breaching a >90 threshold) for every row except the most recent
	// `recoveredTail`, which recovered to 10.
	if _, err := priv.Exec(context.Background(), `
		INSERT INTO telemetry_sample (tenant_id, asset_id, metric, ts, value, labels)
		SELECT $1, $2, $3, $4::timestamptz + (i * interval '1 second'),
		       CASE WHEN i >= $5 THEN 10 ELSE 95 END, '{}'
		  FROM generate_series(0, $6 - 1) AS i`,
		tn.TenantID, asset.AssetID, metric, base, total-recoveredTail, total); err != nil {
		t.Fatalf("bulk insert samples: %v", err)
	}

	store := NewTelemetryStore(priv)
	from := base
	to := base.Add(time.Duration(total) * time.Second)
	got, err := store.QueryRangeForTenant(context.Background(), tn.TenantID, asset.AssetID, metric, from, to)
	if err != nil {
		t.Fatalf("query range for tenant: %v", err)
	}
	if len(got) != domain.MaxTelemetryQueryLimit {
		t.Fatalf("returned %d samples, want exactly the cap (%d)", len(got), domain.MaxTelemetryQueryLimit)
	}

	// Ascending order is the documented contract: the LAST returned sample
	// must be the most recent one written (the tail of the window), not
	// merely the last of an early, stale slice.
	last := got[len(got)-1]
	wantLastTS := base.Add(time.Duration(total-1) * time.Second)
	if !last.Timestamp.Equal(wantLastTS) {
		t.Fatalf("last returned sample ts = %v, want %v (the window's true last sample) — "+
			"the query is not favouring the most recent rows", last.Timestamp, wantLastTS)
	}

	// The recovered tail must be present and visible at the end of the
	// returned slice — this is the actual bug this fix closes: under the
	// pre-fix ASC ordering, none of these would ever be returned at all.
	recoveredSeen := 0
	for _, s := range got[len(got)-recoveredTail:] {
		if s.Value == 10 {
			recoveredSeen++
		}
	}
	if recoveredSeen != recoveredTail {
		t.Fatalf("recovered samples visible in the returned tail = %d, want %d", recoveredSeen, recoveredTail)
	}

	// And the fetch was genuinely truncated from the front: the earliest
	// breaching sample (ts=base) must NOT be among what was returned.
	if got[0].Timestamp.Equal(base) {
		t.Fatalf("the window's very first (oldest) sample was returned — the query is not " +
			"truncating from the front, so this test proves nothing about the fix")
	}
}
