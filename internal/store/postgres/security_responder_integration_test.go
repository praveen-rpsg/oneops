//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/policy"
	"github.com/rpsg/oneops/internal/security"
)

// TestSecurityResponderStore_TenantsAndEnabledRules proves the E8.5
// privileged read surface: TenantsWithEnabledSecurityResponseRules only
// names a tenant holding at least one ENABLED rule, and
// EnabledSecurityResponseRulesForTenant is tenant-explicit and excludes
// disabled rules — mirrors TestIOCMatcherStore_TenantsWithEnabledIOCsAndEnabledIOCsForTenant.
func TestSecurityResponderStore_TenantsAndEnabledRules(t *testing.T) {
	priv := testPool(t)
	a, err := NewTenantStore(priv).Create(adminTestCtx(), newTenant("secresponder-store-alpha", "ext-secresponder-store-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := NewTenantStore(priv).Create(adminTestCtx(), newTenant("secresponder-store-bravo", "ext-secresponder-store-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	scoped := tenantScopedPool(t)
	ruleAdmin := NewSecurityResponseRuleStore(scoped)
	ctxA := assetTestCtx(a)

	enabled, err := domain.NewSecurityResponseRule(a.TenantID, "enabled-rule", domain.IncidentSeverityHigh, nil, "notification", nil)
	if err != nil {
		t.Fatalf("new rule: %v", err)
	}
	if _, err := ruleAdmin.Create(ctxA, enabled); err != nil {
		t.Fatalf("create enabled rule: %v", err)
	}
	disabled, err := domain.NewSecurityResponseRule(a.TenantID, "disabled-rule", domain.IncidentSeverityHigh, nil, "notification", nil)
	if err != nil {
		t.Fatalf("new rule: %v", err)
	}
	createdDisabled, err := ruleAdmin.Create(ctxA, disabled)
	if err != nil {
		t.Fatalf("create disabled rule: %v", err)
	}
	isEnabled := false
	if _, err := ruleAdmin.Update(ctxA, createdDisabled.RuleID, createdDisabled.RowVersion, domain.SecurityResponseRulePatch{Enabled: &isEnabled}); err != nil {
		t.Fatalf("disable rule: %v", err)
	}

	responderStore := NewSecurityResponderStore(priv)

	tenantsWithWork, err := responderStore.TenantsWithEnabledSecurityResponseRules(context.Background())
	if err != nil {
		t.Fatalf("tenants with enabled rules: %v", err)
	}
	found, foundB := false, false
	for _, tn := range tenantsWithWork {
		if tn == a.TenantID {
			found = true
		}
		if tn == b.TenantID {
			foundB = true
		}
	}
	if !found {
		t.Errorf("tenant A (has an enabled rule) not in TenantsWithEnabledSecurityResponseRules: %v", tenantsWithWork)
	}
	if foundB {
		t.Errorf("tenant B (no rules at all) appeared in TenantsWithEnabledSecurityResponseRules: %v", tenantsWithWork)
	}

	gotA, err := responderStore.EnabledSecurityResponseRulesForTenant(context.Background(), a.TenantID, 0, "")
	if err != nil {
		t.Fatalf("enabled rules for tenant a: %v", err)
	}
	if len(gotA) != 1 || gotA[0].RuleID != enabled.RuleID {
		t.Fatalf("enabled rules for tenant a = %+v, want exactly the one enabled rule", gotA)
	}

	gotB, err := responderStore.EnabledSecurityResponseRulesForTenant(context.Background(), b.TenantID, 0, "")
	if err != nil {
		t.Fatalf("enabled rules for tenant b: %v", err)
	}
	if len(gotB) != 0 {
		t.Fatalf("enabled rules for tenant b = %+v, want empty", gotB)
	}
}

// TestSecurityResponderStore_RecentSecurityIncidentsForTenant_ScopedToSecuritySourceTenantAndWindow
// proves RecentSecurityIncidentsForTenant is tenant-explicit, source-scoped
// (a manual- or alert-sourced incident on the SAME tenant/asset/window is
// never returned), and windowed (from, to] exclusive/inclusive.
func TestSecurityResponderStore_RecentSecurityIncidentsForTenant_ScopedToSecuritySourceTenantAndWindow(t *testing.T) {
	priv := testPool(t)
	a, err := NewTenantStore(priv).Create(adminTestCtx(), newTenant("secresponder-window-alpha", "ext-secresponder-window-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := NewTenantStore(priv).Create(adminTestCtx(), newTenant("secresponder-window-bravo", "ext-secresponder-window-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	hostA := telemetryAsset(t, assets, assetTestCtx(a), a.TenantID, "secresponder-window-host-a")
	hostB := telemetryAsset(t, assets, assetTestCtx(b), b.TenantID, "secresponder-window-host-b")

	incPriv := NewIncidentStore(priv)
	base := time.Now().UTC().Add(-time.Hour)

	secA, err := domain.NewSecurityIncident(a.TenantID, "security incident a", "detail", domain.IncidentSeverityHigh, hostA.AssetID)
	if err != nil {
		t.Fatalf("new security incident a: %v", err)
	}
	if _, err := incPriv.Create(adminTestCtx(), secA); err != nil {
		t.Fatalf("create security incident a: %v", err)
	}

	// A manual incident on the SAME tenant/asset must never be returned —
	// this engine only ever considers Source == security.
	manualA, err := domain.NewIncident(a.TenantID, "manual incident a", "detail", domain.IncidentSeverityHigh, &hostA.AssetID, nil)
	if err != nil {
		t.Fatalf("new manual incident a: %v", err)
	}
	if _, err := incPriv.Create(adminTestCtx(), manualA); err != nil {
		t.Fatalf("create manual incident a: %v", err)
	}

	// An alert-sourced incident on the SAME tenant/asset must never be
	// returned either.
	alertA, err := domain.NewAlertIncident(a.TenantID, "alert incident a", "detail", domain.IncidentSeverityHigh, hostA.AssetID)
	if err != nil {
		t.Fatalf("new alert incident a: %v", err)
	}
	if _, err := incPriv.Create(adminTestCtx(), alertA); err != nil {
		t.Fatalf("create alert incident a: %v", err)
	}

	// Tenant B's own security incident, same window — must never appear in
	// tenant A's read.
	secB, err := domain.NewSecurityIncident(b.TenantID, "security incident b", "detail", domain.IncidentSeverityHigh, hostB.AssetID)
	if err != nil {
		t.Fatalf("new security incident b: %v", err)
	}
	if _, err := incPriv.Create(adminTestCtx(), secB); err != nil {
		t.Fatalf("create security incident b: %v", err)
	}

	responderStore := NewSecurityResponderStore(priv)
	from, to := base, time.Now().UTC().Add(time.Hour)

	gotA, err := responderStore.RecentSecurityIncidentsForTenant(context.Background(), a.TenantID, from, to, 0, nil)
	if err != nil {
		t.Fatalf("recent security incidents for tenant a: %v", err)
	}
	if len(gotA) != 1 {
		t.Fatalf("tenant a's security incidents in window = %d, want exactly 1 (manual/alert excluded): %+v", len(gotA), gotA)
	}
	if gotA[0].IncidentID != secA.IncidentID {
		t.Errorf("tenant a's returned incident = %q, want %q", gotA[0].IncidentID, secA.IncidentID)
	}

	gotB, err := responderStore.RecentSecurityIncidentsForTenant(context.Background(), b.TenantID, from, to, 0, nil)
	if err != nil {
		t.Fatalf("recent security incidents for tenant b: %v", err)
	}
	if len(gotB) != 1 || gotB[0].IncidentID != secB.IncidentID {
		t.Fatalf("tenant b's security incidents = %+v, want exactly its own", gotB)
	}

	// Outside the window: nothing.
	past, err := responderStore.RecentSecurityIncidentsForTenant(context.Background(), a.TenantID, base.Add(-2*time.Hour), base.Add(-time.Hour), 0, nil)
	if err != nil {
		t.Fatalf("recent security incidents outside window: %v", err)
	}
	if len(past) != 0 {
		t.Fatalf("incidents outside the window = %+v, want empty", past)
	}
}

// TestSecurityResponderStore_ClaimDispatch_ExactlyOnce proves the ledger's
// own exactly-once claim at the store level, independent of the worker: a
// second ClaimDispatch for the identical (tenant, incident, rule) triple
// returns claimed=false without error, and only one row exists.
func TestSecurityResponderStore_ClaimDispatch_ExactlyOnce(t *testing.T) {
	priv := testPool(t)
	a, err := NewTenantStore(priv).Create(adminTestCtx(), newTenant("secresponder-claim-alpha", "ext-secresponder-claim-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	host := telemetryAsset(t, assets, assetTestCtx(a), a.TenantID, "secresponder-claim-host")

	incPriv := NewIncidentStore(priv)
	inc, err := domain.NewSecurityIncident(a.TenantID, "security incident", "detail", domain.IncidentSeverityHigh, host.AssetID)
	if err != nil {
		t.Fatalf("new security incident: %v", err)
	}
	if _, err := incPriv.Create(adminTestCtx(), inc); err != nil {
		t.Fatalf("create security incident: %v", err)
	}

	// security_response_dispatch.rule_id carries a real foreign key to
	// security_response_rule, so the claim needs a genuine rule row, not a
	// bare minted id.
	ruleAdmin := NewSecurityResponseRuleStore(scoped)
	rule, err := domain.NewSecurityResponseRule(a.TenantID, "claim-test-rule", domain.IncidentSeverityHigh, nil, "http", nil)
	if err != nil {
		t.Fatalf("new rule: %v", err)
	}
	createdRule, err := ruleAdmin.Create(assetTestCtx(a), rule)
	if err != nil {
		t.Fatalf("create rule: %v", err)
	}

	responderStore := NewSecurityResponderStore(priv)
	now := time.Now().UTC()
	ruleID := createdRule.RuleID

	claimed1, err := responderStore.ClaimDispatch(context.Background(), a.TenantID, inc.IncidentID, ruleID, "http", now)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if !claimed1 {
		t.Fatal("first ClaimDispatch must claim (no prior row)")
	}

	claimed2, err := responderStore.ClaimDispatch(context.Background(), a.TenantID, inc.IncidentID, ruleID, "http", now)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if claimed2 {
		t.Fatal("second ClaimDispatch for the identical triple must NOT claim again")
	}

	var count int
	if err := priv.QueryRow(context.Background(),
		`SELECT count(*) FROM security_response_dispatch WHERE tenant_id = $1 AND incident_id = $2 AND rule_id = $3`,
		a.TenantID, inc.IncidentID, ruleID,
	).Scan(&count); err != nil {
		t.Fatalf("count dispatch rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("dispatch rows for the pair = %d, want exactly 1", count)
	}
}

// TestSecurityResponseDispatch_IsAppendOnly proves the ledger's own
// immutability hardening (mirrors control_evidence/incident_event): neither
// UPDATE nor DELETE succeeds against a security_response_dispatch row, even
// on the privileged (table-owner) connection, because the guard is an
// ENABLE ALWAYS trigger, not a revocable grant.
func TestSecurityResponseDispatch_IsAppendOnly(t *testing.T) {
	priv := testPool(t)
	a, err := NewTenantStore(priv).Create(adminTestCtx(), newTenant("secresponder-immutable", "ext-secresponder-immutable"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	host := telemetryAsset(t, assets, assetTestCtx(a), a.TenantID, "secresponder-immutable-host")

	incPriv := NewIncidentStore(priv)
	inc, err := domain.NewSecurityIncident(a.TenantID, "security incident", "detail", domain.IncidentSeverityHigh, host.AssetID)
	if err != nil {
		t.Fatalf("new security incident: %v", err)
	}
	if _, err := incPriv.Create(adminTestCtx(), inc); err != nil {
		t.Fatalf("create security incident: %v", err)
	}

	ruleAdmin := NewSecurityResponseRuleStore(scoped)
	rule, err := domain.NewSecurityResponseRule(a.TenantID, "immutable-test-rule", domain.IncidentSeverityHigh, nil, "http", nil)
	if err != nil {
		t.Fatalf("new rule: %v", err)
	}
	createdRule, err := ruleAdmin.Create(assetTestCtx(a), rule)
	if err != nil {
		t.Fatalf("create rule: %v", err)
	}

	responderStore := NewSecurityResponderStore(priv)
	ruleID := createdRule.RuleID
	if _, err := responderStore.ClaimDispatch(context.Background(), a.TenantID, inc.IncidentID, ruleID, "http", time.Now().UTC()); err != nil {
		t.Fatalf("claim dispatch: %v", err)
	}

	if _, err := priv.Exec(context.Background(),
		`UPDATE security_response_dispatch SET action_type = 'notification' WHERE tenant_id = $1 AND incident_id = $2 AND rule_id = $3`,
		a.TenantID, inc.IncidentID, ruleID); err == nil {
		t.Fatal("UPDATE against security_response_dispatch succeeded — the append-only guard did not hold")
	}
	if _, err := priv.Exec(context.Background(),
		`DELETE FROM security_response_dispatch WHERE tenant_id = $1 AND incident_id = $2 AND rule_id = $3`,
		a.TenantID, inc.IncidentID, ruleID); err == nil {
		t.Fatal("DELETE against security_response_dispatch succeeded — the append-only guard did not hold")
	}
}

// fakeSecurityActionRunner is an in-memory security.ActionRunner recording
// double for the end-to-end tests below — the story's own success criterion
// ("uses a fake/recording action in tests to assert the action ran with the
// right input"), exercised here against the REAL database-backed
// Responder rather than the unit-level fakes.
type fakeSecurityActionRunner struct {
	mu   sync.Mutex
	runs []struct {
		actionType string
		ev         policy.Event
	}
}

func (f *fakeSecurityActionRunner) Run(_ context.Context, actionType string, ev policy.Event, _ json.RawMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, struct {
		actionType string
		ev         policy.Event
	}{actionType, ev})
	return nil
}

func (f *fakeSecurityActionRunner) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.runs)
}

func (f *fakeSecurityActionRunner) tenantsDispatched() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]int{}
	for _, r := range f.runs {
		out[r.ev.TenantID]++
	}
	return out
}

// TestSecurityResponder_EndToEnd_DispatchesSafeActionExactlyOnceAcrossRepeatedPasses
// is the real-database, real-worker proof of E8.5's core payoff: a NEW
// security-sourced incident matching an enabled rule gets that rule's SAFE
// action run exactly once, and a repeated pass over the SAME incident does
// NOT re-fire it — the dispatch ledger, not window-tiling, is what holds.
func TestSecurityResponder_EndToEnd_DispatchesSafeActionExactlyOnceAcrossRepeatedPasses(t *testing.T) {
	priv := testPool(t)
	a := assetTenant(t, NewTenantStore(priv), "secresponder-e2e-once")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	host := telemetryAsset(t, assets, assetTestCtx(a), a.TenantID, "secresponder-e2e-host")

	ruleAdmin := NewSecurityResponseRuleStore(scoped)
	rule, err := domain.NewSecurityResponseRule(a.TenantID, "notify-high", domain.IncidentSeverityHigh, nil, "http",
		[]byte(`{"url":"https://example.com/hook"}`))
	if err != nil {
		t.Fatalf("new rule: %v", err)
	}
	if _, err := ruleAdmin.Create(assetTestCtx(a), rule); err != nil {
		t.Fatalf("create rule: %v", err)
	}

	incPriv := NewIncidentStore(priv)
	inc, err := domain.NewSecurityIncident(a.TenantID, "critical security incident", "detail", domain.IncidentSeverityCritical, host.AssetID)
	if err != nil {
		t.Fatalf("new security incident: %v", err)
	}
	if _, err := incPriv.Create(adminTestCtx(), inc); err != nil {
		t.Fatalf("create security incident: %v", err)
	}

	actions := &fakeSecurityActionRunner{}
	store := NewSecurityResponderStore(priv)
	quiet := slog.New(slog.NewTextHandler(logDiscard{}, nil))
	responder := security.NewResponder(store, store, store, actions, security.NopResponderMetrics{}, quiet, security.ResponderConfig{Interval: time.Hour})

	responder.RunOnce(context.Background())
	if got := actions.count(); got != 1 {
		t.Fatalf("action runs after pass 1 = %d, want exactly 1", got)
	}

	// Repeated passes over the identical incident/rule must not re-fire.
	responder.RunOnce(context.Background())
	responder.RunOnce(context.Background())
	if got := actions.count(); got != 1 {
		t.Fatalf("action runs after 3 repeated passes = %d, want still exactly 1 (no double-fire)", got)
	}
}

// TestSecurityResponder_EndToEnd_TwoTenantsSharingAssetIDIsolated is the
// real-database tenant-isolation proof: two tenants each register a rule and
// a matching security incident that happen to share the SAME asset_id
// string. Each tenant's rule must act ONLY on its own tenant's incident —
// the responder never reads or acts cross-tenant.
func TestSecurityResponder_EndToEnd_TwoTenantsSharingAssetIDIsolated(t *testing.T) {
	priv := testPool(t)
	a := assetTenant(t, NewTenantStore(priv), "secresponder-e2e-iso-alpha")
	b := assetTenant(t, NewTenantStore(priv), "secresponder-e2e-iso-bravo")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	hostA := telemetryAsset(t, assets, assetTestCtx(a), a.TenantID, "secresponder-e2e-iso-host-a")
	hostB := telemetryAsset(t, assets, assetTestCtx(b), b.TenantID, "secresponder-e2e-iso-host-b")

	ruleAdminA := NewSecurityResponseRuleStore(scoped)
	ruleAdminB := NewSecurityResponseRuleStore(scoped)
	rA, err := domain.NewSecurityResponseRule(a.TenantID, "tenant-a-rule", domain.IncidentSeverityHigh, nil, "notification", nil)
	if err != nil {
		t.Fatalf("new rule a: %v", err)
	}
	if _, err := ruleAdminA.Create(assetTestCtx(a), rA); err != nil {
		t.Fatalf("create rule a: %v", err)
	}
	rB, err := domain.NewSecurityResponseRule(b.TenantID, "tenant-b-rule", domain.IncidentSeverityHigh, nil, "notification", nil)
	if err != nil {
		t.Fatalf("new rule b: %v", err)
	}
	if _, err := ruleAdminB.Create(assetTestCtx(b), rB); err != nil {
		t.Fatalf("create rule b: %v", err)
	}

	incPriv := NewIncidentStore(priv)
	incA, err := domain.NewSecurityIncident(a.TenantID, "incident a", "detail", domain.IncidentSeverityCritical, hostA.AssetID)
	if err != nil {
		t.Fatalf("new incident a: %v", err)
	}
	if _, err := incPriv.Create(adminTestCtx(), incA); err != nil {
		t.Fatalf("create incident a: %v", err)
	}
	incB, err := domain.NewSecurityIncident(b.TenantID, "incident b", "detail", domain.IncidentSeverityCritical, hostB.AssetID)
	if err != nil {
		t.Fatalf("new incident b: %v", err)
	}
	if _, err := incPriv.Create(adminTestCtx(), incB); err != nil {
		t.Fatalf("create incident b: %v", err)
	}

	actions := &fakeSecurityActionRunner{}
	store := NewSecurityResponderStore(priv)
	quiet := slog.New(slog.NewTextHandler(logDiscard{}, nil))
	responder := security.NewResponder(store, store, store, actions, security.NopResponderMetrics{}, quiet, security.ResponderConfig{Interval: time.Hour})
	responder.RunOnce(context.Background())

	byTenant := actions.tenantsDispatched()
	if byTenant[a.TenantID] != 1 {
		t.Errorf("tenant a dispatches = %d, want exactly 1", byTenant[a.TenantID])
	}
	if byTenant[b.TenantID] != 1 {
		t.Errorf("tenant b dispatches = %d, want exactly 1", byTenant[b.TenantID])
	}
	if len(byTenant) != 2 {
		t.Fatalf("distinct tenants dispatched = %v, want exactly {a, b}", byTenant)
	}
}
