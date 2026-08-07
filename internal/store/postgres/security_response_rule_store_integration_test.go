//go:build integration

package postgres

import (
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

func TestSecurityResponseRuleStore_CreateGetListUpdateDelete(t *testing.T) {
	testPool(t) // ensures migrations are applied before the scoped pool is used
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "secresp-rule-crud")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	rules := NewSecurityResponseRuleStore(scoped)
	ctx := assetTestCtx(tn)

	host := telemetryAsset(t, assets, ctx, tn.TenantID, "secresp-host")

	r, err := domain.NewSecurityResponseRule(tn.TenantID, "notify-high", domain.IncidentSeverityHigh, &host.AssetID, "http",
		[]byte(`{"url":"https://example.com/hook"}`))
	if err != nil {
		t.Fatalf("new security response rule: %v", err)
	}
	created, err := rules.Create(ctx, r)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.RowVersion != 1 {
		t.Errorf("row_version = %d, want 1", created.RowVersion)
	}
	if !created.Enabled {
		t.Error("a new rule must be enabled by default")
	}
	if created.AssetID == nil || *created.AssetID != host.AssetID {
		t.Errorf("asset_id = %v, want %q", created.AssetID, host.AssetID)
	}
	if created.ActionType != "http" {
		t.Errorf("action_type = %q, want http", created.ActionType)
	}

	got, err := rules.Get(ctx, created.RuleID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "notify-high" || got.MinSeverity != domain.IncidentSeverityHigh {
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

	newName := "notify-critical-only"
	newSeverity := domain.IncidentSeverityCritical
	updated, err := rules.Update(ctx, created.RuleID, created.RowVersion, domain.SecurityResponseRulePatch{
		Name: &newName, MinSeverity: &newSeverity,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Name != newName || updated.MinSeverity != newSeverity || updated.RowVersion != 2 {
		t.Errorf("update = %+v, want name %q, min_severity %q, row_version 2", updated, newName, newSeverity)
	}
	// action_type is fixed at creation — no patch field exists to change it,
	// so this is a compile-time property, not a runtime assertion (see
	// domain.SecurityResponseRulePatch's own doc comment).

	// A stale row_version is refused.
	if _, err := rules.Update(ctx, created.RuleID, created.RowVersion, domain.SecurityResponseRulePatch{Name: &newName}); err != domain.ErrVersionMismatch {
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

// TestSecurityResponseRuleStore_NilAssetIDScopesToEveryAsset proves the
// optional AssetID: a rule created with none set round-trips as nil, never a
// forced empty string or an existence-check failure.
func TestSecurityResponseRuleStore_NilAssetIDScopesToEveryAsset(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "secresp-rule-nilasset")

	scoped := tenantScopedPool(t)
	rules := NewSecurityResponseRuleStore(scoped)
	ctx := assetTestCtx(tn)

	r, err := domain.NewSecurityResponseRule(tn.TenantID, "notify-everything", domain.IncidentSeverityHigh, nil, "notification", nil)
	if err != nil {
		t.Fatalf("new security response rule: %v", err)
	}
	created, err := rules.Create(ctx, r)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.AssetID != nil {
		t.Errorf("asset_id = %v, want nil", created.AssetID)
	}

	got, err := rules.Get(ctx, created.RuleID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.AssetID != nil {
		t.Errorf("re-read asset_id = %v, want nil", got.AssetID)
	}
}

// TestSecurityResponseRuleStore_CreateRejectsCrossTenantOrNonexistentAsset
// mirrors TestSecurityRuleStore_CreateRejectsCrossTenantOrNonexistentAsset
// exactly, for the optional AssetID scope.
func TestSecurityResponseRuleStore_CreateRejectsCrossTenantOrNonexistentAsset(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a, err := tenants.Create(adminTestCtx(), newTenant("secresp-rej-alpha", "ext-secresp-rej-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(adminTestCtx(), newTenant("secresp-rej-bravo", "ext-secresp-rej-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	rules := NewSecurityResponseRuleStore(scoped)
	ctxA := assetTestCtx(a)
	ctxB := assetTestCtx(b)

	victim := telemetryAsset(t, assets, ctxA, a.TenantID, "secresp-victim-host")

	cross, err := domain.NewSecurityResponseRule(b.TenantID, "cross", domain.IncidentSeverityHigh, &victim.AssetID, "http",
		[]byte(`{"url":"https://example.com"}`))
	if err != nil {
		t.Fatalf("new security response rule: %v", err)
	}
	if _, err := rules.Create(ctxB, cross); err != domain.ErrNotFound {
		t.Errorf("cross-tenant asset_id err = %v, want ErrNotFound", err)
	}

	missingAsset := "no-such-asset"
	missing, err := domain.NewSecurityResponseRule(b.TenantID, "missing", domain.IncidentSeverityHigh, &missingAsset, "http",
		[]byte(`{"url":"https://example.com"}`))
	if err != nil {
		t.Fatalf("new security response rule: %v", err)
	}
	if _, err := rules.Create(ctxB, missing); err != domain.ErrNotFound {
		t.Errorf("nonexistent asset_id err = %v, want ErrNotFound", err)
	}

	list, err := rules.List(ctxA, 0, "")
	if err != nil {
		t.Fatalf("list as tenant a: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("a cross-tenant rule was written: %+v", list)
	}
}

// TestSecurityResponseRuleIsolation_RLSByTenant mirrors
// TestSecurityRuleIsolation_RLSByTenant exactly.
func TestSecurityResponseRuleIsolation_RLSByTenant(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a, err := tenants.Create(adminTestCtx(), newTenant("secresp-iso-alpha", "ext-secresp-iso-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(adminTestCtx(), newTenant("secresp-iso-bravo", "ext-secresp-iso-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	scoped := tenantScopedPool(t)
	rules := NewSecurityResponseRuleStore(scoped)
	ctxA := assetTestCtx(a)
	ctxB := assetTestCtx(b)

	rA, err := domain.NewSecurityResponseRule(a.TenantID, "tenant-a-rule", domain.IncidentSeverityHigh, nil, "notification", nil)
	if err != nil {
		t.Fatalf("new security response rule: %v", err)
	}
	createdA, err := rules.Create(ctxA, rA)
	if err != nil {
		t.Fatalf("create as tenant a: %v", err)
	}

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

	newName := "hijacked"
	if _, err := rules.Update(ctxB, createdA.RuleID, createdA.RowVersion, domain.SecurityResponseRulePatch{Name: &newName}); err != domain.ErrNotFound {
		t.Errorf("tenant B patched tenant A's rule: err = %v, want ErrNotFound", err)
	}

	if err := rules.Delete(ctxB, createdA.RuleID); err != domain.ErrNotFound {
		t.Errorf("tenant B deleted tenant A's rule: err = %v, want ErrNotFound", err)
	}

	stillThere, err := rules.Get(ctxA, createdA.RuleID)
	if err != nil || stillThere.RuleID != createdA.RuleID {
		t.Fatalf("tenant A lost its own rule: %v, %+v", err, stillThere)
	}
	if stillThere.Name != "tenant-a-rule" || stillThere.RowVersion != createdA.RowVersion {
		t.Errorf("tenant A's rule was mutated by tenant B's attempt: %+v", stillThere)
	}
}

// TestSecurityResponseRule_DBLevelRejectsUnsafeActionType is defense in
// depth (the story's own non-negotiable #2): even a raw INSERT that bypasses
// domain.SecurityResponseRule.Validate entirely is refused by the database's
// own ck_security_response_rule_action_type CHECK constraint for
// "command" — the SAFE allowlist holds even against a caller that skipped
// the application layer.
func TestSecurityResponseRule_DBLevelRejectsUnsafeActionType(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "secresp-db-unsafe")

	scoped := tenantScopedPool(t)
	ctx := assetTestCtx(tn)

	_, err := scoped.Exec(ctx, `
		INSERT INTO security_response_rule (rule_id, tenant_id, name, min_severity, action_type, action_config)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		domain.NewID(), tn.TenantID, "raw-insert", "high", "command", []byte(`{}`))
	if err == nil {
		t.Fatal("a raw INSERT with action_type='command' succeeded — the database-level SAFE allowlist did not hold")
	}
}
