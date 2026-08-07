package security

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

func mkResponseRule(
	t *testing.T, tenantID, name string, minSeverity domain.IncidentSeverity, assetID *string, actionType string,
) *domain.SecurityResponseRule {
	t.Helper()
	r, err := domain.NewSecurityResponseRule(tenantID, name, minSeverity, assetID, actionType, nil)
	if err != nil {
		t.Fatalf("new security response rule: %v", err)
	}
	return r
}

// mkSecurityIncident builds a security-sourced incident with createdAt set
// directly (a plain struct here, never persisted) — mirrors how
// ioc_matcher_test.go builds domain.SecurityObservation literals.
func mkSecurityIncident(t *testing.T, tenantID, assetID string, severity domain.IncidentSeverity, createdAt time.Time) domain.Incident {
	t.Helper()
	inc, err := domain.NewSecurityIncident(tenantID, "incident on "+assetID, "detail", severity, assetID)
	if err != nil {
		t.Fatalf("new security incident: %v", err)
	}
	inc.CreatedAt = createdAt
	return *inc
}

func newTestResponder(
	rules ResponseRuleLister, incidents IncidentWindowReader, dispatches DispatchClaimer,
	actions ActionRunner, now time.Time, interval time.Duration,
) *Responder {
	r := NewResponder(rules, incidents, dispatches, actions, NopResponderMetrics{}, quiet(), ResponderConfig{Concurrency: 4, Interval: interval})
	r.now = func() time.Time { return now }
	return r
}

// TestSecurityResponder_MatchDispatchesSafeActionWithRightInput is the
// story's own core payoff: a NEW security-sourced incident whose severity
// meets an enabled rule's threshold gets that rule's SAFE action run exactly
// once, with an input naming the incident (id, source, severity, asset,
// title) and the rule.
func TestSecurityResponder_MatchDispatchesSafeActionWithRightInput(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rule := mkResponseRule(t, "tenant-a", "notify-high", domain.IncidentSeverityHigh, nil, "http")
	rules := newFakeResponseRuleLister(rule)

	inc := mkSecurityIncident(t, "tenant-a", "asset-1", domain.IncidentSeverityCritical, now.Add(-5*time.Second))
	incidents := newFakeSecurityIncidentReader(inc)
	dispatches := newFakeDispatchClaimer()
	actions := newFakeActionRunner()

	r := newTestResponder(rules, incidents, dispatches, actions, now, time.Minute)
	r.RunOnce(context.Background())

	if got := actions.runCount(); got != 1 {
		t.Fatalf("action runs = %d, want exactly 1", got)
	}
	run := actions.runs[0]
	if run.actionType != "http" {
		t.Errorf("actionType = %q, want %q", run.actionType, "http")
	}
	if run.ev.TenantID != "tenant-a" {
		t.Errorf("ev.TenantID = %q, want tenant-a", run.ev.TenantID)
	}
	if run.ev.Metadata["incident_id"] != inc.IncidentID {
		t.Errorf("ev.Metadata[incident_id] = %q, want %q", run.ev.Metadata["incident_id"], inc.IncidentID)
	}
	if run.ev.Metadata["source"] != "security" {
		t.Errorf("ev.Metadata[source] = %q, want security", run.ev.Metadata["source"])
	}
	if run.ev.Metadata["severity"] != "critical" {
		t.Errorf("ev.Metadata[severity] = %q, want critical", run.ev.Metadata["severity"])
	}
	if run.ev.Metadata["asset_id"] != "asset-1" {
		t.Errorf("ev.Metadata[asset_id] = %q, want asset-1", run.ev.Metadata["asset_id"])
	}
	if run.ev.Metadata["title"] == "" {
		t.Error("ev.Metadata[title] must name the incident's own title, got empty")
	}
	if run.ev.Metadata["rule_id"] != rule.RuleID {
		t.Errorf("ev.Metadata[rule_id] = %q, want %q", run.ev.Metadata["rule_id"], rule.RuleID)
	}
	if !dispatches.isClaimed("tenant-a", inc.IncidentID, rule.RuleID) {
		t.Error("the (incident, rule) pair must be claimed on the dispatch ledger after a successful run")
	}
}

// TestSecurityResponder_BelowThresholdSeverityDoesNotDispatch proves the
// MinSeverity floor: an incident below a rule's threshold never runs that
// rule's action.
func TestSecurityResponder_BelowThresholdSeverityDoesNotDispatch(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rule := mkResponseRule(t, "tenant-a", "notify-high", domain.IncidentSeverityHigh, nil, "notification")
	rules := newFakeResponseRuleLister(rule)

	inc := mkSecurityIncident(t, "tenant-a", "asset-1", domain.IncidentSeverityMedium, now.Add(-5*time.Second))
	incidents := newFakeSecurityIncidentReader(inc)
	dispatches := newFakeDispatchClaimer()
	actions := newFakeActionRunner()

	r := newTestResponder(rules, incidents, dispatches, actions, now, time.Minute)
	r.RunOnce(context.Background())

	if got := actions.runCount(); got != 0 {
		t.Fatalf("action runs for a below-threshold incident = %d, want 0", got)
	}
}

// TestSecurityResponder_AssetScopedRuleOnlyMatchesThatAsset proves the
// optional AssetID scope: a rule naming one asset never fires for a
// same-severity incident on a different asset, but does fire for its own.
func TestSecurityResponder_AssetScopedRuleOnlyMatchesThatAsset(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	scoped := "asset-1"
	rule := mkResponseRule(t, "tenant-a", "notify-asset-1", domain.IncidentSeverityHigh, &scoped, "notification")
	rules := newFakeResponseRuleLister(rule)

	other := mkSecurityIncident(t, "tenant-a", "asset-2", domain.IncidentSeverityCritical, now.Add(-10*time.Second))
	mine := mkSecurityIncident(t, "tenant-a", "asset-1", domain.IncidentSeverityCritical, now.Add(-5*time.Second))
	incidents := newFakeSecurityIncidentReader(other, mine)
	dispatches := newFakeDispatchClaimer()
	actions := newFakeActionRunner()

	r := newTestResponder(rules, incidents, dispatches, actions, now, time.Minute)
	r.RunOnce(context.Background())

	if got := actions.runCount(); got != 1 {
		t.Fatalf("action runs = %d, want exactly 1 (only the scoped asset's incident)", got)
	}
	if actions.runs[0].ev.Metadata["asset_id"] != "asset-1" {
		t.Errorf("dispatched for asset_id = %q, want asset-1", actions.runs[0].ev.Metadata["asset_id"])
	}
}

// TestSecurityResponder_DisabledRuleDoesNotMatch proves a disabled rule is
// never evaluated, even though its condition would otherwise match.
func TestSecurityResponder_DisabledRuleDoesNotMatch(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rule := mkResponseRule(t, "tenant-a", "notify-high", domain.IncidentSeverityHigh, nil, "http")
	rule.Enabled = false
	rules := newFakeResponseRuleLister(rule)

	inc := mkSecurityIncident(t, "tenant-a", "asset-1", domain.IncidentSeverityCritical, now.Add(-5*time.Second))
	incidents := newFakeSecurityIncidentReader(inc)
	dispatches := newFakeDispatchClaimer()
	actions := newFakeActionRunner()

	r := newTestResponder(rules, incidents, dispatches, actions, now, time.Minute)
	r.RunOnce(context.Background())

	if got := actions.runCount(); got != 0 {
		t.Fatalf("action runs with only a disabled rule = %d, want 0", got)
	}
}

// TestSecurityResponder_ExactlyOnce_NoDoubleFireAcrossRepeatedPasses is the
// make-or-break proof: a repeated RunOnce pass over a window that still
// covers the SAME incident must NOT re-run the action — the dispatch ledger
// makes the second pass a no-op, not a second webhook/notification.
func TestSecurityResponder_ExactlyOnce_NoDoubleFireAcrossRepeatedPasses(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rule := mkResponseRule(t, "tenant-a", "notify-high", domain.IncidentSeverityHigh, nil, "http")
	rules := newFakeResponseRuleLister(rule)

	inc := mkSecurityIncident(t, "tenant-a", "asset-1", domain.IncidentSeverityCritical, now.Add(-5*time.Second))
	// A WIDE window (an hour) so both passes' (from, to] windows still cover
	// the same incident — proving the ledger, not window-tiling, is what
	// prevents the second fire.
	incidents := newFakeSecurityIncidentReader(inc)
	dispatches := newFakeDispatchClaimer()
	actions := newFakeActionRunner()

	r := newTestResponder(rules, incidents, dispatches, actions, now, time.Hour)
	r.RunOnce(context.Background())
	if got := actions.runCount(); got != 1 {
		t.Fatalf("action runs after pass 1 = %d, want exactly 1", got)
	}

	// Re-run the identical pass (same clock, same window, same incident
	// still returned by the reader) — a restart or a re-triggered tick.
	r.RunOnce(context.Background())
	if got := actions.runCount(); got != 1 {
		t.Fatalf("action runs after a REPEATED pass over the same incident = %d, want still exactly 1 (no double-fire)", got)
	}
	if got := dispatches.calls; got < 2 {
		t.Fatalf("ClaimDispatch calls = %d, want at least 2 (both passes attempted to claim)", got)
	}
}

// TestSecurityResponder_ActionErrorStillCountsAsClaimed proves the
// record-first, at-most-once ordering this story's design commits to: the
// ledger row is claimed BEFORE the action runs, so an action failure does
// NOT get retried on the next pass — the alternative (retry until success)
// would risk a duplicate outbound call on a transient failure that actually
// half-succeeded downstream, which is a worse failure mode for a SAFE-action
// responder than a single dropped firing.
func TestSecurityResponder_ActionErrorStillCountsAsClaimed(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rule := mkResponseRule(t, "tenant-a", "notify-high", domain.IncidentSeverityHigh, nil, "http")
	rules := newFakeResponseRuleLister(rule)

	inc := mkSecurityIncident(t, "tenant-a", "asset-1", domain.IncidentSeverityCritical, now.Add(-5*time.Second))
	incidents := newFakeSecurityIncidentReader(inc)
	dispatches := newFakeDispatchClaimer()
	actions := newFakeActionRunner()
	actions.failNext = errors.New("webhook target unreachable")

	r := newTestResponder(rules, incidents, dispatches, actions, now, time.Hour)
	r.RunOnce(context.Background())

	if got := actions.runCount(); got != 0 {
		t.Fatalf("successful action runs recorded = %d, want 0 (the one attempt failed)", got)
	}
	if !dispatches.isClaimed("tenant-a", inc.IncidentID, rule.RuleID) {
		t.Fatal("the pair must still be claimed even though the action itself failed (record-first ordering)")
	}

	// A second pass must NOT retry: the ledger already holds the claim.
	r.RunOnce(context.Background())
	if got := actions.runCount(); got != 0 {
		t.Fatalf("action runs after a second pass following a claimed-but-failed dispatch = %d, want still 0 (no retry)", got)
	}
}

// TestSecurityResponder_TenantIsolation_SharedAssetID is the make-or-break,
// isolation-level proof: multiple tenants each register a rule and an
// incident that happen to share the SAME asset_id string (an adversarial
// collision a globally unique id would not normally produce). Each tenant's
// rule must act ONLY on its own tenant's incident — an action dispatched for
// tenant A must never be attributable to, or triggered by, tenant B's data.
func TestSecurityResponder_TenantIsolation_SharedAssetID(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	const sharedAsset = "asset-shared"

	var rules []*domain.SecurityResponseRule
	var incidentsList []domain.Incident
	for i := 0; i < 5; i++ {
		tenantID := fmt.Sprintf("tenant-%02d", i)
		rules = append(rules, mkResponseRule(t, tenantID, "notify", domain.IncidentSeverityHigh, nil, "notification"))
		incidentsList = append(incidentsList, mkSecurityIncident(t, tenantID, sharedAsset, domain.IncidentSeverityCritical, now.Add(-5*time.Second)))
	}

	ruleLister := newFakeResponseRuleLister(rules...)
	incidentReader := newFakeSecurityIncidentReader(incidentsList...)
	dispatches := newFakeDispatchClaimer()
	actions := newFakeActionRunner()

	r := newTestResponder(ruleLister, incidentReader, dispatches, actions, now, time.Minute)
	r.cfg.Concurrency = 8
	r.RunOnce(context.Background())

	if got := actions.runCount(); got != 5 {
		t.Fatalf("action runs = %d, want 5 (one per tenant sharing the same asset_id, zero cross-tenant reuse)", got)
	}
	seenTenants := map[string]bool{}
	for _, run := range actions.runs {
		if seenTenants[run.ev.TenantID] {
			t.Errorf("tenant %s triggered more than one action", run.ev.TenantID)
		}
		seenTenants[run.ev.TenantID] = true
	}
	if len(seenTenants) != 5 {
		t.Fatalf("distinct tenants that dispatched = %d, want 5", len(seenTenants))
	}
}

// TestRuleMatchesIncident unit-tests the pure match predicate directly (no
// worker plumbing).
func TestRuleMatchesIncident(t *testing.T) {
	high := domain.IncidentSeverityHigh
	assetA := "asset-a"
	tests := []struct {
		name     string
		rule     *domain.SecurityResponseRule
		incident domain.Incident
		want     bool
	}{
		{
			"severity meets threshold, unscoped",
			&domain.SecurityResponseRule{MinSeverity: high},
			domain.Incident{Severity: domain.IncidentSeverityCritical},
			true,
		},
		{
			"severity below threshold",
			&domain.SecurityResponseRule{MinSeverity: high},
			domain.Incident{Severity: domain.IncidentSeverityMedium},
			false,
		},
		{
			"asset-scoped, matching asset",
			&domain.SecurityResponseRule{MinSeverity: high, AssetID: &assetA},
			domain.Incident{Severity: domain.IncidentSeverityCritical, AssetID: &assetA},
			true,
		},
		{
			"asset-scoped, non-matching asset",
			&domain.SecurityResponseRule{MinSeverity: high, AssetID: &assetA},
			domain.Incident{Severity: domain.IncidentSeverityCritical, AssetID: strptr("asset-b")},
			false,
		},
		{
			"asset-scoped, incident has no asset",
			&domain.SecurityResponseRule{MinSeverity: high, AssetID: &assetA},
			domain.Incident{Severity: domain.IncidentSeverityCritical, AssetID: nil},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ruleMatchesIncident(tt.rule, &tt.incident); got != tt.want {
				t.Errorf("ruleMatchesIncident() = %v, want %v", got, tt.want)
			}
		})
	}
}

func strptr(s string) *string { return &s }
