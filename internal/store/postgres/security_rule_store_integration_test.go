//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

func TestSecurityRuleStore_CreateGetListUpdateDelete(t *testing.T) {
	testPool(t) // ensures migrations are applied before the scoped pool is used
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "security-rule-crud")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	rules := NewSecurityRuleStore(scoped)
	ctx := assetTestCtx(tn)

	host := telemetryAsset(t, assets, ctx, tn.TenantID, "security-rule-host")

	r, err := domain.NewSecurityRule(tn.TenantID, host.AssetID, "port_scan", 300, 5, domain.IncidentSeverityHigh)
	if err != nil {
		t.Fatalf("new security rule: %v", err)
	}
	created, err := rules.Create(ctx, r)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.RowVersion != 1 {
		t.Errorf("row_version = %d, want 1", created.RowVersion)
	}
	if created.LastState != domain.SecurityRuleStateOK {
		t.Errorf("a new rule must start at ok: %+v", created)
	}
	if created.CurrentIncidentID != nil {
		t.Errorf("a new rule must start with no linked incident: %+v", created)
	}
	if created.MinSeverity != domain.DefaultSecurityRuleMinSeverity {
		t.Errorf("min_severity = %q, want default %q", created.MinSeverity, domain.DefaultSecurityRuleMinSeverity)
	}
	if !created.Enabled {
		t.Error("a new rule must be enabled by default")
	}

	got, err := rules.Get(ctx, created.RuleID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.WindowSeconds != 300 || got.ThresholdCount != 5 || got.ObservationType != "port_scan" {
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

	newThreshold := 10
	updated, err := rules.Update(ctx, created.RuleID, created.RowVersion, domain.SecurityRulePatch{ThresholdCount: &newThreshold})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.ThresholdCount != newThreshold || updated.RowVersion != 2 {
		t.Errorf("update = %+v, want threshold_count %v and row_version 2", updated, newThreshold)
	}

	// A stale row_version is refused.
	if _, err := rules.Update(ctx, created.RuleID, created.RowVersion, domain.SecurityRulePatch{ThresholdCount: &newThreshold}); err != domain.ErrVersionMismatch {
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

// TestSecurityRuleStore_CreateRejectsCrossTenantOrNonexistentAsset proves
// ADR-ASSET-001 §6's defense extended to security_rule: a rule cannot be
// created against another tenant's asset, or against an asset_id that does
// not exist anywhere — both return ErrNotFound, not a cross-tenant row.
func TestSecurityRuleStore_CreateRejectsCrossTenantOrNonexistentAsset(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a, err := tenants.Create(adminTestCtx(), newTenant("sr-rej-alpha", "ext-sr-rej-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(adminTestCtx(), newTenant("sr-rej-bravo", "ext-sr-rej-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	rules := NewSecurityRuleStore(scoped)
	ctxA := assetTestCtx(a)
	ctxB := assetTestCtx(b)

	victim := telemetryAsset(t, assets, ctxA, a.TenantID, "victim-host")

	// Tenant B names tenant A's real asset.
	cross, err := domain.NewSecurityRule(b.TenantID, victim.AssetID, "port_scan", 300, 5, domain.IncidentSeverityHigh)
	if err != nil {
		t.Fatalf("new security rule: %v", err)
	}
	if _, err := rules.Create(ctxB, cross); err != domain.ErrNotFound {
		t.Errorf("cross-tenant asset_id err = %v, want ErrNotFound", err)
	}

	// Tenant B names an asset id that does not exist anywhere.
	missing, err := domain.NewSecurityRule(b.TenantID, "no-such-asset", "port_scan", 300, 5, domain.IncidentSeverityHigh)
	if err != nil {
		t.Fatalf("new security rule: %v", err)
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

// TestSecurityRuleIsolation_RLSByTenant is the security gate for this story:
// tenant B can never see, list, patch or delete tenant A's security rules,
// even by the exact rule_id — the same two-tenant proof
// TestAlertRuleIsolation_RLSByTenant establishes for alert_rule.
func TestSecurityRuleIsolation_RLSByTenant(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a, err := tenants.Create(adminTestCtx(), newTenant("sr-iso-alpha", "ext-sr-iso-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(adminTestCtx(), newTenant("sr-iso-bravo", "ext-sr-iso-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	rules := NewSecurityRuleStore(scoped)
	ctxA := assetTestCtx(a)
	ctxB := assetTestCtx(b)

	hostA := telemetryAsset(t, assets, ctxA, a.TenantID, "iso-host-a")

	rA, err := domain.NewSecurityRule(a.TenantID, hostA.AssetID, "port_scan", 300, 5, domain.IncidentSeverityHigh)
	if err != nil {
		t.Fatalf("new security rule: %v", err)
	}
	createdA, err := rules.Create(ctxA, rA)
	if err != nil {
		t.Fatalf("create as tenant a: %v", err)
	}

	// READ: tenant B cannot get tenant A's rule by id, and does not see it in
	// its own list.
	if _, err := rules.Get(ctxB, createdA.RuleID); err != domain.ErrNotFound {
		t.Errorf("tenant B read tenant A's rule: err = %v, want ErrNotFound", err)
	}
	listB, err := rules.List(ctxB, 0, "")
	if err != nil {
		t.Fatalf("list as tenant b: %v", err)
	}
	if len(listB) != 0 {
		t.Errorf("tenant B saw tenant A's rules: %+v", listB)
	}

	// WRITE: tenant B cannot patch tenant A's rule, even with the correct
	// row_version.
	newThreshold := 99
	if _, err := rules.Update(ctxB, createdA.RuleID, createdA.RowVersion, domain.SecurityRulePatch{ThresholdCount: &newThreshold}); err != domain.ErrNotFound {
		t.Errorf("tenant B patched tenant A's rule: err = %v, want ErrNotFound", err)
	}

	// DELETE: tenant B cannot delete tenant A's rule.
	if err := rules.Delete(ctxB, createdA.RuleID); err != domain.ErrNotFound {
		t.Errorf("tenant B deleted tenant A's rule: err = %v, want ErrNotFound", err)
	}

	// Tenant A still sees its own rule, completely undisturbed by tenant B's
	// attempts above.
	stillThere, err := rules.Get(ctxA, createdA.RuleID)
	if err != nil || stillThere.RuleID != createdA.RuleID {
		t.Fatalf("tenant A lost its own rule: %v, %+v", err, stillThere)
	}
	if stillThere.ThresholdCount != 5 || stillThere.RowVersion != createdA.RowVersion {
		t.Errorf("tenant A's rule was mutated by tenant B's attempt: %+v", stillThere)
	}
}

// TestSecurityRuleStore_MinSeverityAndIncidentSeverityRoundTrip proves the
// two independent severity vocabularies (ObservationSeverity for
// min_severity, IncidentSeverity for incident_severity) round-trip through
// Create/Get/Update without collapsing into each other — the store must
// never confuse the two closed sets even though "critical"/"high"/"medium"/
// "low" are spelled identically in both.
func TestSecurityRuleStore_MinSeverityAndIncidentSeverityRoundTrip(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "security-rule-severity")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	rules := NewSecurityRuleStore(scoped)
	ctx := assetTestCtx(tn)
	host := telemetryAsset(t, assets, ctx, tn.TenantID, "severity-host")

	r, err := domain.NewSecurityRule(tn.TenantID, host.AssetID, "malware_detected", 60, 1, domain.IncidentSeverityCritical)
	if err != nil {
		t.Fatalf("new security rule: %v", err)
	}
	r.MinSeverity = domain.ObservationSeverityLow
	created, err := rules.Create(ctx, r)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.MinSeverity != domain.ObservationSeverityLow {
		t.Errorf("min_severity = %q, want %q", created.MinSeverity, domain.ObservationSeverityLow)
	}
	if created.IncidentSeverity != domain.IncidentSeverityCritical {
		t.Errorf("incident_severity = %q, want %q", created.IncidentSeverity, domain.IncidentSeverityCritical)
	}

	newMin := domain.ObservationSeverityCritical
	newIncident := domain.IncidentSeverityLow
	patched, err := rules.Update(ctx, created.RuleID, created.RowVersion, domain.SecurityRulePatch{
		MinSeverity: &newMin, IncidentSeverity: &newIncident,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if patched.MinSeverity != domain.ObservationSeverityCritical {
		t.Errorf("patched min_severity = %q, want %q", patched.MinSeverity, domain.ObservationSeverityCritical)
	}
	if patched.IncidentSeverity != domain.IncidentSeverityLow {
		t.Errorf("patched incident_severity = %q, want %q", patched.IncidentSeverity, domain.IncidentSeverityLow)
	}
}

// TestSecurityRuleDetectorStore_EnabledRulesAndRecordTransition proves the
// detector's own privileged surface (security.Store, E8.1b-2): EnabledRules
// pages across tenants (a disabled rule and another tenant's rule are both
// excluded/included correctly), and RecordTransition CAS-updates
// last_state/last_transition_at/current_incident_id fenced on row_version —
// mirrors TestAlertRuleStore-equivalent coverage for alert_rule.
func TestSecurityRuleDetectorStore_EnabledRulesAndRecordTransition(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a, err := tenants.Create(adminTestCtx(), newTenant("sr-detector-alpha", "ext-sr-detector-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(adminTestCtx(), newTenant("sr-detector-bravo", "ext-sr-detector-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	rules := NewSecurityRuleStore(scoped)
	ctxA := assetTestCtx(a)
	ctxB := assetTestCtx(b)

	hostA := telemetryAsset(t, assets, ctxA, a.TenantID, "sr-detector-host-a")
	hostB := telemetryAsset(t, assets, ctxB, b.TenantID, "sr-detector-host-b")

	rA, err := domain.NewSecurityRule(a.TenantID, hostA.AssetID, "port_scan", 300, 5, domain.IncidentSeverityHigh)
	if err != nil {
		t.Fatalf("new security rule a: %v", err)
	}
	createdA, err := rules.Create(ctxA, rA)
	if err != nil {
		t.Fatalf("create rule a: %v", err)
	}
	rB, err := domain.NewSecurityRule(b.TenantID, hostB.AssetID, "port_scan", 300, 5, domain.IncidentSeverityHigh)
	if err != nil {
		t.Fatalf("new security rule b: %v", err)
	}
	createdB, err := rules.Create(ctxB, rB)
	if err != nil {
		t.Fatalf("create rule b: %v", err)
	}
	// A disabled rule must never appear in EnabledRules.
	rDisabled, err := domain.NewSecurityRule(a.TenantID, hostA.AssetID, "malware_detected", 300, 1, domain.IncidentSeverityCritical)
	if err != nil {
		t.Fatalf("new disabled security rule: %v", err)
	}
	createdDisabled, err := rules.Create(ctxA, rDisabled)
	if err != nil {
		t.Fatalf("create disabled rule: %v", err)
	}
	disabled := false
	if _, err := rules.Update(ctxA, createdDisabled.RuleID, createdDisabled.RowVersion, domain.SecurityRulePatch{Enabled: &disabled}); err != nil {
		t.Fatalf("disable rule: %v", err)
	}

	detector := NewSecurityRuleDetectorStore(priv)
	seen := map[string]*domain.SecurityRule{}
	after := ""
	for {
		page, err := detector.EnabledRules(context.Background(), 500, after)
		if err != nil {
			t.Fatalf("enabled rules: %v", err)
		}
		if len(page) == 0 {
			break
		}
		for _, r := range page {
			seen[r.RuleID] = r
			after = r.RuleID
		}
		if len(page) < 500 {
			break
		}
	}
	if _, ok := seen[createdA.RuleID]; !ok {
		t.Errorf("EnabledRules did not include tenant a's rule")
	}
	if _, ok := seen[createdB.RuleID]; !ok {
		t.Errorf("EnabledRules did not include tenant b's rule — the due-scan is cross-tenant by design")
	}
	if _, ok := seen[createdDisabled.RuleID]; ok {
		t.Errorf("EnabledRules included a disabled rule")
	}

	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	incidentID := "fake-incident-id"
	updated, err := detector.RecordTransition(context.Background(), createdA.RuleID, createdA.RowVersion, domain.SecurityRuleStateFiring, now, &incidentID)
	if err != nil {
		t.Fatalf("record transition: %v", err)
	}
	if updated.LastState != domain.SecurityRuleStateFiring {
		t.Errorf("last_state = %q, want firing", updated.LastState)
	}
	if !updated.LastTransitionAt.Equal(now) {
		t.Errorf("last_transition_at = %s, want %s", updated.LastTransitionAt, now)
	}
	if updated.CurrentIncidentID == nil || *updated.CurrentIncidentID != incidentID {
		t.Errorf("current_incident_id = %v, want %q", updated.CurrentIncidentID, incidentID)
	}

	// A stale row_version is refused.
	if _, err := detector.RecordTransition(context.Background(), createdA.RuleID, createdA.RowVersion, domain.SecurityRuleStateOK, now, nil); err != domain.ErrVersionMismatch {
		t.Errorf("stale record transition err = %v, want ErrVersionMismatch", err)
	}

	// Clearing the link (firing->ok) sets current_incident_id back to nil.
	cleared, err := detector.RecordTransition(context.Background(), createdA.RuleID, updated.RowVersion, domain.SecurityRuleStateOK, now.Add(time.Minute), nil)
	if err != nil {
		t.Fatalf("record recovery transition: %v", err)
	}
	if cleared.CurrentIncidentID != nil {
		t.Errorf("current_incident_id after recovery = %v, want nil", cleared.CurrentIncidentID)
	}
	if cleared.LastState != domain.SecurityRuleStateOK {
		t.Errorf("last_state after recovery = %q, want ok", cleared.LastState)
	}
}
