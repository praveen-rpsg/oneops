package alerting

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// TestEvaluator_FirstFiringCreatesOneAlertIncident is the create half of
// E4.1's core payoff: a rule's first ok->firing transition on a CI with no
// existing open alert incident creates exactly one, links the rule to it,
// and still sends the usual notification (notify AND incident, not
// notify-or-incident).
func TestEvaluator_FirstFiringCreatesOneAlertIncident(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rule := mkRule(t, "tenant-a", "asset-1", "cpu_utilization", domain.ComparatorGT, 90, 300)

	store := newFakeStore(rule)
	tel := newFakeTelemetry()
	notifier := &fakeNotifier{}
	correlator := newFakeCorrelator()
	e := newTestEvaluatorWithCorrelator(store, tel, notifier, correlator, now)

	from := now.Add(-300 * time.Second)
	tel.set(rule.TenantID, rule.AssetID, rule.Metric, breachingSamples(from, now, 30*time.Second, 95))

	e.RunOnce(context.Background())

	if got := notifier.count(); got != 1 {
		t.Fatalf("notifications = %d, want 1 (notify AND incident, not either/or)", got)
	}
	creates := correlator.callsOfKind("create")
	if len(creates) != 1 {
		t.Fatalf("incident creations = %d, want exactly 1", len(creates))
	}
	updated := store.get(rule.RuleID)
	if updated.CurrentIncidentID == nil || *updated.CurrentIncidentID != creates[0].incidentID {
		t.Fatalf("rule.CurrentIncidentID = %v, want the created incident %q", updated.CurrentIncidentID, creates[0].incidentID)
	}
	inc := correlator.get(creates[0].incidentID)
	if inc.Source != domain.IncidentSourceAlert {
		t.Errorf("created incident source = %q, want alert", inc.Source)
	}
	if inc.AssetID == nil || *inc.AssetID != rule.AssetID {
		t.Errorf("created incident asset_id = %v, want %q", inc.AssetID, rule.AssetID)
	}
}

// TestEvaluator_SecondRuleSameAssetLinksNoDuplicate is the no-duplicate bite
// test: a SECOND rule firing on the SAME asset (a different metric —
// correlation groups by CI, not by rule) must LINK to the incident the first
// rule's firing already created, never create a second one.
func TestEvaluator_SecondRuleSameAssetLinksNoDuplicate(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rule1 := mkRule(t, "tenant-a", "asset-1", "cpu_utilization", domain.ComparatorGT, 90, 300)
	rule2 := mkRule(t, "tenant-a", "asset-1", "disk_free_bytes", domain.ComparatorLT, 1000, 300)

	store := newFakeStore(rule1, rule2)
	tel := newFakeTelemetry()
	notifier := &fakeNotifier{}
	correlator := newFakeCorrelator()
	e := newTestEvaluatorWithCorrelator(store, tel, notifier, correlator, now)

	from := now.Add(-300 * time.Second)
	tel.set(rule1.TenantID, rule1.AssetID, rule1.Metric, breachingSamples(from, now, 30*time.Second, 95))
	tel.set(rule2.TenantID, rule2.AssetID, rule2.Metric, breachingSamples(from, now, 30*time.Second, 10))

	e.RunOnce(context.Background())

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

// TestEvaluator_RecoveryAppendsNoteClearsLinkDoesNotClose proves the
// firing->ok half: a recovery note is appended to the linked incident, the
// rule's link is cleared, and — this is structurally guaranteed, not merely
// tested, because IncidentCorrelator exposes no status-changing method at
// all — the incident's own status is never touched.
func TestEvaluator_RecoveryAppendsNoteClearsLinkDoesNotClose(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rule := mkRule(t, "tenant-a", "asset-1", "cpu_utilization", domain.ComparatorGT, 90, 300)

	store := newFakeStore(rule)
	tel := newFakeTelemetry()
	notifier := &fakeNotifier{}
	correlator := newFakeCorrelator()
	e := newTestEvaluatorWithCorrelator(store, tel, notifier, correlator, now)

	from := now.Add(-300 * time.Second)
	tel.set(rule.TenantID, rule.AssetID, rule.Metric, breachingSamples(from, now, 30*time.Second, 95))
	e.RunOnce(context.Background())

	linkedID := *store.get(rule.RuleID).CurrentIncidentID
	statusBefore := correlator.get(linkedID).Status

	// Recover: values now below threshold across the whole window.
	tel.set(rule.TenantID, rule.AssetID, rule.Metric, breachingSamples(from, now, 30*time.Second, 10))
	e.RunOnce(context.Background())

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
		t.Errorf("incident status after recovery = %q, want unchanged %q — E4.1 must never auto-resolve/close", got, statusBefore)
	}
}

// TestEvaluator_ManualIncidentIsNotAutoLinked is the make-or-break proof that
// Source gates correlation: a manually-filed, OPEN incident sitting on the
// exact same asset a rule then fires on must NOT be discovered by
// FindOrCreateOpenAlertIncident — a new, alert-sourced incident is created
// instead. Mutation-verified: deleting the Source==IncidentSourceAlert check
// from fakeCorrelator.FindOrCreateOpenAlertIncident (mirroring what removing
// `source = 'alert'` from ux_incident_open_alert_per_asset's predicate and
// the real store's SELECT would do) makes this test link to the manual
// incident instead of creating a new one.
func TestEvaluator_ManualIncidentIsNotAutoLinked(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rule := mkRule(t, "tenant-a", "asset-1", "cpu_utilization", domain.ComparatorGT, 90, 300)

	manual, err := domain.NewIncident(rule.TenantID, "operator opened this by hand", "", domain.IncidentSeverityHigh, &rule.AssetID, nil)
	if err != nil {
		t.Fatalf("new manual incident: %v", err)
	}
	correlator := newFakeCorrelator(manual)

	store := newFakeStore(rule)
	tel := newFakeTelemetry()
	notifier := &fakeNotifier{}
	e := newTestEvaluatorWithCorrelator(store, tel, notifier, correlator, now)

	from := now.Add(-300 * time.Second)
	tel.set(rule.TenantID, rule.AssetID, rule.Metric, breachingSamples(from, now, 30*time.Second, 95))
	e.RunOnce(context.Background())

	links := correlator.callsOfKind("link")
	if len(links) != 0 {
		t.Fatalf("links to a manual incident = %d, want 0 — a manually-filed incident must never be auto-linked", len(links))
	}
	creates := correlator.callsOfKind("create")
	if len(creates) != 1 {
		t.Fatalf("incident creations = %d, want exactly 1 (a fresh alert incident, separate from the manual one)", len(creates))
	}
	if creates[0].incidentID == manual.IncidentID {
		t.Fatalf("correlation reused the manual incident's id — it must create a distinct one")
	}
	updated := store.get(rule.RuleID)
	if updated.CurrentIncidentID == nil || *updated.CurrentIncidentID == manual.IncidentID {
		t.Fatalf("rule.CurrentIncidentID = %v, must not be the manual incident %q", updated.CurrentIncidentID, manual.IncidentID)
	}
}

// TestEvaluator_CorrelationIsTenantIsolated is the mutation-tested,
// make-or-break proof for the privileged correlator's tenant boundary
// (ADR-TENANCY-012): many tenants share the exact same asset_id (an
// adversarial collision a real, globally-unique asset id would not normally
// produce — see TestEvaluator_CrossTenantTelemetryIsolation's identical
// reasoning), and every firing must create/link ONLY its own tenant's
// incident. fakeCorrelator.FindOrCreateOpenAlertIncident filters candidates
// by `inc.TenantID != want.TenantID` exactly as the real store's SQL filters
// by an explicit `tenant_id = $1` predicate: deleting either check makes
// every tenant here collapse onto ONE shared incident instead of one each.
func TestEvaluator_CorrelationIsTenantIsolated(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	from := now.Add(-300 * time.Second)
	const sharedAsset = "asset-shared"
	const sharedMetric = "cpu_utilization"

	tel := newFakeTelemetry()
	var rules []*domain.AlertRule
	for i := 0; i < 10; i++ {
		tenantID := fmt.Sprintf("tenant-%02d", i)
		rule := mkRule(t, tenantID, sharedAsset, sharedMetric, domain.ComparatorGT, 90, 300)
		rules = append(rules, rule)
		tel.set(tenantID, sharedAsset, sharedMetric, breachingSamples(from, now, 30*time.Second, 95))
	}

	store := newFakeStore(rules...)
	notifier := &fakeNotifier{}
	correlator := newFakeCorrelator()
	e := newTestEvaluatorWithCorrelator(store, tel, notifier, correlator, now)
	e.cfg.Concurrency = 8

	e.RunOnce(context.Background())

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

// TestEvaluator_CorrelationFailureDoesNotPersistTransition mirrors
// TestEvaluator_ConcurrentEditDuringTransitionIsHandled's shape for the
// correlation step: if the correlator errors (e.g. the AppendAlertNote
// tenant check above refused it), the transition is NOT persisted — the
// next tick re-detects the same real-world transition and retries both the
// notification and the correlation, the same "duplicate over silent loss"
// doctrine ADR-TENANCY-012 §2 states for notify.
func TestEvaluator_CorrelationFailureDoesNotPersistTransition(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rule := mkRule(t, "tenant-a", "asset-1", "cpu_utilization", domain.ComparatorGT, 90, 300)

	store := newFakeStore(rule)
	tel := newFakeTelemetry()
	notifier := &fakeNotifier{}
	correlator := newFakeCorrelator()
	correlator.failNext = errors.New("injected: tenant isolation refused this correlation")
	e := newTestEvaluatorWithCorrelator(store, tel, notifier, correlator, now)

	from := now.Add(-300 * time.Second)
	tel.set(rule.TenantID, rule.AssetID, rule.Metric, breachingSamples(from, now, 30*time.Second, 95))
	e.RunOnce(context.Background())

	if got := notifier.count(); got != 1 {
		t.Fatalf("notifications = %d, want 1 (already enqueued before correlation ran)", got)
	}
	if got := store.get(rule.RuleID).LastState; got != domain.AlertRuleStateOK {
		t.Fatalf("last_state = %q, want ok (unchanged: the transition must not persist on a correlation failure)", got)
	}

	// Next tick retries: this time correlation succeeds.
	e.RunOnce(context.Background())
	if got := notifier.count(); got != 2 {
		t.Fatalf("notifications after retry = %d, want 2 (one duplicate from the retried transition)", got)
	}
	if got := store.get(rule.RuleID).LastState; got != domain.AlertRuleStateFiring {
		t.Fatalf("last_state after retry = %q, want firing", got)
	}
	if len(correlator.callsOfKind("create")) != 1 {
		t.Fatalf("incident creations after retry = %d, want exactly 1", len(correlator.callsOfKind("create")))
	}
}
