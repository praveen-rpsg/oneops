package security

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// TestSecurityDetector_FirstFiringCreatesOneIncident is the create half of
// E8.1b-2's core payoff — the security analog of
// alerting.TestEvaluator_FirstFiringCreatesOneAlertIncident: a rule's first
// ok->firing transition on a CI with no existing open security incident
// creates exactly one and links the rule to it.
func TestSecurityDetector_FirstFiringCreatesOneIncident(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rule := mkRule(t, "tenant-a", "asset-1", "port_scan", 300, 5)

	store := newFakeStore(rule)
	counter := newFakeCounter()
	correlator := newFakeCorrelator()
	d := newTestDetector(store, counter, correlator, now)
	counter.set(rule.TenantID, rule.AssetID, rule.ObservationType, 10)

	d.RunOnce(context.Background())

	creates := correlator.callsOfKind("create")
	if len(creates) != 1 {
		t.Fatalf("incident creations = %d, want exactly 1", len(creates))
	}
	updated := store.get(rule.RuleID)
	if updated.CurrentIncidentID == nil || *updated.CurrentIncidentID != creates[0].incidentID {
		t.Fatalf("rule.CurrentIncidentID = %v, want the created incident %q", updated.CurrentIncidentID, creates[0].incidentID)
	}
	inc := correlator.get(creates[0].incidentID)
	if inc.Source != domain.IncidentSourceSecurity {
		t.Errorf("created incident source = %q, want security", inc.Source)
	}
	if inc.AssetID == nil || *inc.AssetID != rule.AssetID {
		t.Errorf("created incident asset_id = %v, want %q", inc.AssetID, rule.AssetID)
	}
	if inc.Severity != rule.IncidentSeverity {
		t.Errorf("created incident severity = %q, want the rule's own %q", inc.Severity, rule.IncidentSeverity)
	}
}

// TestSecurityDetector_SecondRuleSameAssetLinksNoDuplicate is the
// no-duplicate bite test — the security analog of
// alerting.TestEvaluator_SecondRuleSameAssetLinksNoDuplicate: a SECOND rule
// firing on the SAME asset (a different observation_type — correlation
// groups by CI, not by rule) must LINK to the incident the first rule's
// firing already created, never create a second one.
func TestSecurityDetector_SecondRuleSameAssetLinksNoDuplicate(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rule1 := mkRule(t, "tenant-a", "asset-1", "port_scan", 300, 5)
	rule2 := mkRule(t, "tenant-a", "asset-1", "malware_detected", 300, 1)

	store := newFakeStore(rule1, rule2)
	counter := newFakeCounter()
	correlator := newFakeCorrelator()
	d := newTestDetector(store, counter, correlator, now)
	counter.set(rule1.TenantID, rule1.AssetID, rule1.ObservationType, 10)
	counter.set(rule2.TenantID, rule2.AssetID, rule2.ObservationType, 3)

	d.RunOnce(context.Background())

	creates := correlator.callsOfKind("create")
	links := correlator.callsOfKind("link")
	if len(creates) != 1 {
		t.Fatalf("incident creations = %d, want exactly 1 (no duplicate)", len(creates))
	}
	if len(links) != 1 {
		t.Fatalf("incident links = %d, want exactly 1", len(links))
	}
	if links[0].incidentID != creates[0].incidentID {
		t.Fatalf("linked incident %q != created incident %q — second rule created its own instead of linking",
			links[0].incidentID, creates[0].incidentID)
	}
	r1 := store.get(rule1.RuleID)
	r2 := store.get(rule2.RuleID)
	if r1.CurrentIncidentID == nil || r2.CurrentIncidentID == nil || *r1.CurrentIncidentID != *r2.CurrentIncidentID {
		t.Fatalf("rule1.CurrentIncidentID=%v rule2.CurrentIncidentID=%v, want both set to the same incident",
			r1.CurrentIncidentID, r2.CurrentIncidentID)
	}
	if len(correlator.incidents) != 1 {
		t.Fatalf("incidents in the correlator = %d, want exactly 1", len(correlator.incidents))
	}
}

// TestSecurityDetector_RecoveryAppendsNoteClearsLinkDoesNotClose proves the
// firing->ok half: a recovery note is appended to the linked incident, the
// rule's link is cleared, and — structurally guaranteed because
// IncidentCorrelator exposes no status-changing method at all — the
// incident's own status is never touched.
func TestSecurityDetector_RecoveryAppendsNoteClearsLinkDoesNotClose(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rule := mkRule(t, "tenant-a", "asset-1", "port_scan", 300, 5)

	store := newFakeStore(rule)
	counter := newFakeCounter()
	correlator := newFakeCorrelator()
	d := newTestDetector(store, counter, correlator, now)
	counter.set(rule.TenantID, rule.AssetID, rule.ObservationType, 10)
	d.RunOnce(context.Background())

	linkedID := *store.get(rule.RuleID).CurrentIncidentID
	statusBefore := correlator.get(linkedID).Status

	counter.set(rule.TenantID, rule.AssetID, rule.ObservationType, 0)
	d.RunOnce(context.Background())

	notes := correlator.callsOfKind("note")
	if len(notes) != 1 {
		t.Fatalf("recovery notes = %d, want exactly 1", len(notes))
	}
	if notes[0].incidentID != linkedID {
		t.Errorf("recovery note incident = %q, want the linked incident %q", notes[0].incidentID, linkedID)
	}
	if updated := store.get(rule.RuleID); updated.CurrentIncidentID != nil {
		t.Errorf("rule.CurrentIncidentID after recovery = %v, want nil (cleared)", updated.CurrentIncidentID)
	}
	if got := correlator.get(linkedID).Status; got != statusBefore {
		t.Errorf("incident status after recovery = %q, want unchanged %q — this package must never auto-resolve/close", got, statusBefore)
	}
}

// TestSecurityDetector_ManualIncidentIsNotAutoLinked proves Source gates
// correlation: a manually-filed, OPEN incident sitting on the exact same
// asset a rule then fires on must NOT be discovered by
// FindOrCreateOpenSecurityIncident — a new, security-sourced incident is
// created instead.
func TestSecurityDetector_ManualIncidentIsNotAutoLinked(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rule := mkRule(t, "tenant-a", "asset-1", "port_scan", 300, 5)

	manual, err := domain.NewIncident(rule.TenantID, "operator opened this by hand", "", domain.IncidentSeverityHigh, &rule.AssetID, nil)
	if err != nil {
		t.Fatalf("new manual incident: %v", err)
	}
	correlator := newFakeCorrelator(manual)

	store := newFakeStore(rule)
	counter := newFakeCounter()
	d := newTestDetector(store, counter, correlator, now)
	counter.set(rule.TenantID, rule.AssetID, rule.ObservationType, 10)
	d.RunOnce(context.Background())

	if got := len(correlator.callsOfKind("link")); got != 0 {
		t.Fatalf("links to a manual incident = %d, want 0", got)
	}
	creates := correlator.callsOfKind("create")
	if len(creates) != 1 {
		t.Fatalf("incident creations = %d, want exactly 1 (a fresh security incident, separate from the manual one)", len(creates))
	}
	if creates[0].incidentID == manual.IncidentID {
		t.Fatalf("correlation reused the manual incident's id — it must create a distinct one")
	}
}

// TestSecurityDetector_AlertIncidentIsNotAutoLinked is the coexistence proof
// at the unit level: an OPEN alert-sourced incident on the exact same asset
// must NOT be discovered by FindOrCreateOpenSecurityIncident either — Source
// distinguishes the two correlation paths, so an alert incident and a
// security incident on the same CI are always two separate rows. The real
// database-level proof (the two source-scoped partial unique indexes never
// interfering) lives in internal/store/postgres's integration suite.
func TestSecurityDetector_AlertIncidentIsNotAutoLinked(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rule := mkRule(t, "tenant-a", "asset-1", "port_scan", 300, 5)

	alertInc, err := domain.NewAlertIncident(rule.TenantID, "cpu_utilization firing", "", domain.IncidentSeverityHigh, rule.AssetID)
	if err != nil {
		t.Fatalf("new alert incident: %v", err)
	}
	correlator := newFakeCorrelator(alertInc)

	store := newFakeStore(rule)
	counter := newFakeCounter()
	d := newTestDetector(store, counter, correlator, now)
	counter.set(rule.TenantID, rule.AssetID, rule.ObservationType, 10)
	d.RunOnce(context.Background())

	if got := len(correlator.callsOfKind("link")); got != 0 {
		t.Fatalf("links to an alert-sourced incident = %d, want 0 — Source must gate correlation", got)
	}
	creates := correlator.callsOfKind("create")
	if len(creates) != 1 {
		t.Fatalf("incident creations = %d, want exactly 1 (a fresh security incident, coexisting with the alert one)", len(creates))
	}
	if creates[0].incidentID == alertInc.IncidentID {
		t.Fatalf("correlation reused the alert incident's id — it must create a distinct one")
	}
}

// TestSecurityDetector_CorrelationIsTenantIsolated is the mutation-tested,
// make-or-break proof for the privileged correlator's tenant boundary
// (ADR-TENANCY-012) — the security analog of
// alerting.TestEvaluator_CorrelationIsTenantIsolated: many tenants share the
// exact same asset_id, and every firing must create/link ONLY its own
// tenant's incident.
func TestSecurityDetector_CorrelationIsTenantIsolated(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	const sharedAsset = "asset-shared"
	const sharedType = "port_scan"

	counter := newFakeCounter()
	var rules []*domain.SecurityRule
	for i := 0; i < 10; i++ {
		tenantID := fmt.Sprintf("tenant-%02d", i)
		rule := mkRule(t, tenantID, sharedAsset, sharedType, 300, 5)
		rules = append(rules, rule)
		counter.set(tenantID, sharedAsset, sharedType, 10)
	}

	store := newFakeStore(rules...)
	correlator := newFakeCorrelator()
	d := newTestDetector(store, counter, correlator, now)
	d.cfg.Concurrency = 8

	d.RunOnce(context.Background())

	creates := correlator.callsOfKind("create")
	if len(creates) != 10 {
		t.Fatalf("incidents created = %d, want 10 (one per tenant sharing the same asset_id, zero cross-tenant reuse)", len(creates))
	}
	seen := map[string]bool{}
	for i, rule := range rules {
		updated := store.get(rule.RuleID)
		if updated.CurrentIncidentID == nil {
			t.Fatalf("tenant %s: CurrentIncidentID not set", rule.TenantID)
		}
		inc := correlator.get(*updated.CurrentIncidentID)
		if inc.TenantID != rule.TenantID {
			t.Errorf("tenant %s (index %d): linked incident belongs to tenant %q — cross-tenant correlation leaked",
				rule.TenantID, i, inc.TenantID)
		}
		if seen[*updated.CurrentIncidentID] {
			t.Errorf("incident %s linked to more than one tenant's rule", *updated.CurrentIncidentID)
		}
		seen[*updated.CurrentIncidentID] = true
	}
}

// TestSecurityDetector_CorrelationFailureDoesNotPersistTransition mirrors
// alerting.TestEvaluator_CorrelationFailureDoesNotPersistTransition: if the
// correlator errors, the transition is NOT persisted — the next tick
// re-detects the same real-world transition and retries the correlation.
func TestSecurityDetector_CorrelationFailureDoesNotPersistTransition(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rule := mkRule(t, "tenant-a", "asset-1", "port_scan", 300, 5)

	store := newFakeStore(rule)
	counter := newFakeCounter()
	correlator := newFakeCorrelator()
	correlator.failNext = errors.New("injected: tenant isolation refused this correlation")
	d := newTestDetector(store, counter, correlator, now)
	counter.set(rule.TenantID, rule.AssetID, rule.ObservationType, 10)

	d.RunOnce(context.Background())
	if got := store.get(rule.RuleID).LastState; got != domain.SecurityRuleStateOK {
		t.Fatalf("last_state = %q, want ok (unchanged: the transition must not persist on a correlation failure)", got)
	}

	// Next tick retries: this time correlation succeeds.
	d.RunOnce(context.Background())
	if got := store.get(rule.RuleID).LastState; got != domain.SecurityRuleStateFiring {
		t.Fatalf("last_state after retry = %q, want firing", got)
	}
	if len(correlator.callsOfKind("create")) != 1 {
		t.Fatalf("incident creations after retry = %d, want exactly 1", len(correlator.callsOfKind("create")))
	}
}
