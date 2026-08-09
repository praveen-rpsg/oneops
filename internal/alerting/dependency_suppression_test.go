package alerting

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// TestEvaluator_DependencyDownSuppressesFiringNoIncidentNoNotify is E3.3b's
// core payoff at the orchestration level: a sustained breach on X whose
// (tenant, X) has a down dependency commits NEITHER the notification NOR the
// incident create/link — X's firing is treated as collateral of the root's
// outage — while the checker still records that the suppression happened
// (never a silent drop). The store proves the graph-walk + down-check itself
// against real PostgreSQL; this proves the evaluator honours the answer.
//
// Mutation-verified: see TestEvaluator_DependencyCheckIsNotVacuous below.
func TestEvaluator_DependencyDownSuppressesFiringNoIncidentNoNotify(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rule := mkRule(t, "tenant-a", "asset-x", "cpu_utilization", domain.ComparatorGT, 90, 300)

	store := newFakeStore(rule)
	tel := newFakeTelemetry()
	notifier := &fakeNotifier{}
	correlator := newFakeCorrelator()
	dependency := newFakeDependencyChecker(fakeDownDependency{
		tenantID: rule.TenantID, assetID: rule.AssetID, rootAssetID: "db-y",
	})
	e := newTestEvaluatorWithDependency(store, tel, notifier, correlator, newFakeMaintenanceChecker(), dependency, now)

	from := now.Add(-300 * time.Second)
	tel.set(rule.TenantID, rule.AssetID, rule.Metric, breachingSamples(from, now, 30*time.Second, 95))

	e.RunOnce(context.Background())

	if got := notifier.count(); got != 0 {
		t.Errorf("notifications while a dependency is down = %d, want 0", got)
	}
	if creates := correlator.callsOfKind("create"); len(creates) != 0 {
		t.Errorf("incident creations while a dependency is down = %d, want 0", len(creates))
	}
	if got := store.get(rule.RuleID).LastState; got != domain.AlertRuleStateOK {
		t.Errorf("last_state during suppression = %q, want ok (not committed while suppressed)", got)
	}
	if got := dependency.suppressions(); got != 1 {
		t.Errorf("recorded suppressions = %d, want exactly 1 — a suppression must never be a silent drop", got)
	}
	if got := dependency.callCount(); got != 1 {
		t.Errorf("dependency checker consulted %d times, want exactly 1", got)
	}
}

// TestEvaluator_NoDependencyDownFiringProceedsNormally is the non-vacuous
// counterpart: with no down dependency, the checker is consulted (proving it
// is wired into the commit path) but never suppresses, and the firing proceeds
// exactly as E4.1 already guarantees.
func TestEvaluator_NoDependencyDownFiringProceedsNormally(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rule := mkRule(t, "tenant-a", "asset-x", "cpu_utilization", domain.ComparatorGT, 90, 300)

	store := newFakeStore(rule)
	tel := newFakeTelemetry()
	notifier := &fakeNotifier{}
	correlator := newFakeCorrelator()
	dependency := newFakeDependencyChecker() // nothing down
	e := newTestEvaluatorWithDependency(store, tel, notifier, correlator, newFakeMaintenanceChecker(), dependency, now)

	from := now.Add(-300 * time.Second)
	tel.set(rule.TenantID, rule.AssetID, rule.Metric, breachingSamples(from, now, 30*time.Second, 95))

	e.RunOnce(context.Background())

	if got := notifier.count(); got != 1 {
		t.Fatalf("notifications with no dependency down = %d, want 1", got)
	}
	if creates := correlator.callsOfKind("create"); len(creates) != 1 {
		t.Fatalf("incident creations with no dependency down = %d, want 1", len(creates))
	}
	if got := store.get(rule.RuleID).LastState; got != domain.AlertRuleStateFiring {
		t.Fatalf("last_state = %q, want firing", got)
	}
	if got := dependency.callCount(); got != 1 {
		t.Errorf("dependency checker consulted %d times, want exactly 1 (the check must actually run)", got)
	}
	if got := dependency.suppressions(); got != 0 {
		t.Errorf("suppressions = %d, want 0", got)
	}
}

// TestEvaluator_DependencySelfClearsWhenRootRecovers is E3.3b's self-clearing
// proof, the dependency analogue of E3.3a's end-of-window resumption: while a
// dependency is down X's firing is suppressed tick after tick (each recorded,
// never silently), but the instant the root recovers — so the checker stops
// reporting a down dependency — a STILL-firing X pages on that very next tick,
// because the suppression never committed anything and so left a real ok->firing
// transition still to make.
func TestEvaluator_DependencySelfClearsWhenRootRecovers(t *testing.T) {
	clock := &flapClock{t: time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)}
	rule := mkRule(t, "tenant-a", "asset-x", "cpu_utilization", domain.ComparatorGT, 90, 60)

	store := newFakeStore(rule)
	tel := newFakeTelemetry()
	notifier := &fakeNotifier{}
	correlator := newFakeCorrelator()
	dependency := newFakeDependencyChecker(fakeDownDependency{
		tenantID: rule.TenantID, assetID: rule.AssetID, rootAssetID: "db-y",
	})
	e := newTestEvaluatorWithDependency(store, tel, notifier, correlator, newFakeMaintenanceChecker(), dependency, clock.t)
	e.now = clock.now

	tick := func() {
		setMetric(tel, rule, clock.t, true)
		e.RunOnce(context.Background())
	}

	// Tick 1: the dependency is down — suppressed.
	tick()
	if got := notifier.count(); got != 0 {
		t.Fatalf("notifications while the dependency is down = %d, want 0", got)
	}
	if got := store.get(rule.RuleID).LastState; got != domain.AlertRuleStateOK {
		t.Fatalf("last_state while suppressed = %q, want ok", got)
	}

	// Tick 2: still down, breach continues — still suppressed, count advances.
	clock.t = clock.t.Add(30 * time.Second)
	tick()
	if got := notifier.count(); got != 0 {
		t.Fatalf("notifications still while the dependency is down = %d, want 0", got)
	}
	if got := dependency.suppressions(); got != 2 {
		t.Fatalf("recorded suppressions after 2 ticks with a down dependency = %d, want 2", got)
	}

	// Root recovers: the checker no longer reports a down dependency. X is
	// still firing, so it must page on THIS tick.
	dependency.clearDowns()
	clock.t = clock.t.Add(30 * time.Second)
	tick()
	if got := notifier.count(); got != 1 {
		t.Fatalf("notifications on the tick the root recovers = %d, want 1 — a still-firing X must page the "+
			"instant its down dependency clears, not later", got)
	}
	if got := store.get(rule.RuleID).LastState; got != domain.AlertRuleStateFiring {
		t.Fatalf("last_state after the root recovered = %q, want firing", got)
	}
	if creates := correlator.callsOfKind("create"); len(creates) != 1 {
		t.Fatalf("incident creations once the root recovered = %d, want 1", len(creates))
	}
}

// TestEvaluator_RecoveryIsNeverDependencySuppressed proves the direction E3.3b
// does not touch: a firing->ok transition commits (notifies, appends the
// recovery note, clears the link) even while a dependency is down — recovery
// (good news) is never withheld, and the checker is not even consulted for it.
func TestEvaluator_RecoveryIsNeverDependencySuppressed(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rule := mkRule(t, "tenant-a", "asset-x", "cpu_utilization", domain.ComparatorGT, 90, 300)

	store := newFakeStore(rule)
	tel := newFakeTelemetry()
	notifier := &fakeNotifier{}
	correlator := newFakeCorrelator()
	// No down dependency yet: get the rule genuinely firing with a linked incident.
	dependency := newFakeDependencyChecker()
	e := newTestEvaluatorWithDependency(store, tel, notifier, correlator, newFakeMaintenanceChecker(), dependency, now)

	from := now.Add(-300 * time.Second)
	tel.set(rule.TenantID, rule.AssetID, rule.Metric, breachingSamples(from, now, 30*time.Second, 95))
	e.RunOnce(context.Background())
	if got := store.get(rule.RuleID).LastState; got != domain.AlertRuleStateFiring {
		t.Fatalf("setup: last_state = %q, want firing", got)
	}
	callsAfterFiring := dependency.callCount()

	// Now a dependency goes down WHILE X recovers.
	dependency.setDown(fakeDownDependency{tenantID: rule.TenantID, assetID: rule.AssetID, rootAssetID: "db-y"})
	tel.set(rule.TenantID, rule.AssetID, rule.Metric, breachingSamples(from, now, 30*time.Second, 10))
	e.RunOnce(context.Background())

	if got := notifier.count(); got != 2 {
		t.Fatalf("notifications after recovery under a down dependency = %d, want 2 (fire + recover, "+
			"recovery must never be suppressed)", got)
	}
	if got := store.get(rule.RuleID).LastState; got != domain.AlertRuleStateOK {
		t.Fatalf("last_state after recovery under a down dependency = %q, want ok", got)
	}
	if notes := correlator.callsOfKind("note"); len(notes) != 1 {
		t.Fatalf("recovery notes under a down dependency = %d, want 1", len(notes))
	}
	if got := dependency.callCount(); got != callsAfterFiring {
		t.Errorf("dependency checker consulted %d times for a firing->ok recovery, want 0 additional (the "+
			"checker must not even be consulted for recovery)", got-callsAfterFiring)
	}
	if got := dependency.suppressions(); got != 0 {
		t.Errorf("suppressions recorded for a recovery = %d, want 0", got)
	}
}

// TestEvaluator_DependencySuppressionIsTenantIsolated is the cross-tenant proof
// at the orchestration level, mirroring TestEvaluator_MaintenanceWindowIsTenant
// Isolated: many tenants share the exact same asset_id, only one of them has a
// down dependency, and only ITS rule may be suppressed — every other tenant's
// identical-asset_id rule must still fire and page. (The store-level test proves
// the down-check's SQL tenant predicate itself bites against real PostgreSQL;
// this proves the evaluator threads rule.TenantID through, never a shared label.)
func TestEvaluator_DependencySuppressionIsTenantIsolated(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	from := now.Add(-300 * time.Second)
	const sharedAsset = "asset-shared"
	const sharedMetric = "cpu_utilization"

	tel := newFakeTelemetry()
	var rules []*domain.AlertRule
	for i := 0; i < 5; i++ {
		tenantID := fmt.Sprintf("tenant-%02d", i)
		rule := mkRule(t, tenantID, sharedAsset, sharedMetric, domain.ComparatorGT, 90, 300)
		rules = append(rules, rule)
		tel.set(tenantID, sharedAsset, sharedMetric, breachingSamples(from, now, 30*time.Second, 95))
	}
	const suppressedTenant = "tenant-00"

	store := newFakeStore(rules...)
	notifier := &fakeNotifier{}
	dependency := newFakeDependencyChecker(fakeDownDependency{
		tenantID: suppressedTenant, assetID: sharedAsset, rootAssetID: "db-y",
	})
	e := newTestEvaluatorWithDependency(store, tel, notifier, nil, newFakeMaintenanceChecker(), dependency, now)
	e.cfg.Concurrency = 8

	e.RunOnce(context.Background())

	for _, rule := range rules {
		got := store.get(rule.RuleID).LastState
		want := domain.AlertRuleStateFiring
		if rule.TenantID == suppressedTenant {
			want = domain.AlertRuleStateOK
		}
		if got != want {
			t.Errorf("tenant %s (shared asset_id with a DIFFERENT tenant's down dependency): last_state = %q, "+
				"want %q — dependency-suppression tenant isolation did not bite", rule.TenantID, got, want)
		}
	}
	if got := notifier.count(); got != len(rules)-1 {
		t.Errorf("notifications = %d, want %d (every tenant except the one with a down dependency)", got, len(rules)-1)
	}
	if got := dependency.suppressions(); got != 1 {
		t.Errorf("suppressions = %d, want exactly 1 (only the tenant that actually has a down dependency)", got)
	}
}

// TestEvaluator_DependencySuppressedFiringDoesNotCorruptDwellPending is the E3.2
// composition proof: a rule with a configured flap dwell that finally settles
// WHILE a dependency is down must not have its pending candidate corrupted by
// the suppression — once the root recovers, the already-satisfied dwell commits
// on the very next tick, it does not restart timing from zero.
func TestEvaluator_DependencySuppressedFiringDoesNotCorruptDwellPending(t *testing.T) {
	clock := &flapClock{t: time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)}
	start := clock.t
	rule := mkRule(t, "tenant-a", "asset-x", "cpu_utilization", domain.ComparatorGT, 90, 60)
	rule.FlapDwellSeconds = 90

	store := newFakeStore(rule)
	tel := newFakeTelemetry()
	notifier := &fakeNotifier{}
	dependency := newFakeDependencyChecker(fakeDownDependency{
		tenantID: rule.TenantID, assetID: rule.AssetID, rootAssetID: "db-y",
	})
	e := newTestEvaluatorWithDependency(store, tel, notifier, nil, newFakeMaintenanceChecker(), dependency, start)
	e.now = clock.now
	e.cfg.Concurrency = 1

	tick := func() {
		setMetric(tel, rule, clock.t, true)
		e.RunOnce(context.Background())
	}

	tick() // t+0: candidate departs from ok; dwell (90s) starts pending
	if got := store.get(rule.RuleID).PendingState; got != domain.AlertRuleStateFiring {
		t.Fatalf("pending_state after the first tick = %q, want firing", got)
	}

	// t+90: the dwell has elapsed, but the dependency is still down — the
	// transition is ready-to-commit yet held back by suppression, NOT re-timed.
	clock.t = start.Add(90 * time.Second)
	tick()
	if got := notifier.count(); got != 0 {
		t.Fatalf("notifications once the dwell elapsed but the dependency is still down = %d, want 0", got)
	}
	if got := store.get(rule.RuleID).LastState; got != domain.AlertRuleStateOK {
		t.Fatalf("last_state while suppressed = %q, want ok (not committed)", got)
	}
	pendingRow := store.get(rule.RuleID)
	if pendingRow.PendingState != domain.AlertRuleStateFiring || !pendingRow.PendingSince.Equal(start) {
		t.Fatalf("pending candidate after a suppressed, dwell-satisfied tick = %q since %v, want firing since %v — "+
			"suppression must not corrupt E3.2's bookkeeping", pendingRow.PendingState, pendingRow.PendingSince, start)
	}

	// t+120: the root recovers. The already-satisfied dwell commits IMMEDIATELY
	// on this tick — it does not need another 90s.
	dependency.clearDowns()
	clock.t = start.Add(120 * time.Second)
	tick()
	if got := notifier.count(); got != 1 {
		t.Fatalf("notifications on the tick the root recovers (dwell long since satisfied) = %d, want 1", got)
	}
	if got := store.get(rule.RuleID).LastState; got != domain.AlertRuleStateFiring {
		t.Fatalf("last_state after the root recovered = %q, want firing", got)
	}
	final := store.get(rule.RuleID)
	if final.PendingState != "" || !final.PendingSince.IsZero() {
		t.Errorf("pending candidate after a committed transition = %q since %v, want cleared", final.PendingState, final.PendingSince)
	}
}

// TestEvaluator_DependencyCheckErrorDoesNotPersistTransition mirrors the
// maintenance-check error case: if the dependency checker itself errors (a
// database blip, a graph-walk failure), the transition is NOT persisted and no
// notification is sent — the next tick re-detects the same real-world
// transition and retries, the "duplicate over silent loss" doctrine this
// package applies everywhere.
func TestEvaluator_DependencyCheckErrorDoesNotPersistTransition(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rule := mkRule(t, "tenant-a", "asset-x", "cpu_utilization", domain.ComparatorGT, 90, 300)

	store := newFakeStore(rule)
	tel := newFakeTelemetry()
	notifier := &fakeNotifier{}
	dependency := newFakeDependencyChecker()
	dependency.failNext = errors.New("injected: dependency suppression check unavailable")
	e := newTestEvaluatorWithDependency(store, tel, notifier, nil, newFakeMaintenanceChecker(), dependency, now)

	from := now.Add(-300 * time.Second)
	tel.set(rule.TenantID, rule.AssetID, rule.Metric, breachingSamples(from, now, 30*time.Second, 95))
	e.RunOnce(context.Background())

	if got := notifier.count(); got != 0 {
		t.Fatalf("notifications when the dependency check errors = %d, want 0", got)
	}
	if got := store.get(rule.RuleID).LastState; got != domain.AlertRuleStateOK {
		t.Fatalf("last_state after a dependency-check error = %q, want ok (unchanged)", got)
	}

	// Next tick: the checker succeeds (nothing down), the retried transition commits.
	e.RunOnce(context.Background())
	if got := notifier.count(); got != 1 {
		t.Fatalf("notifications after retry = %d, want 1", got)
	}
	if got := store.get(rule.RuleID).LastState; got != domain.AlertRuleStateFiring {
		t.Fatalf("last_state after retry = %q, want firing", got)
	}
}

// TestEvaluator_MaintenanceWindowPrecedesDependencyCheck pins the ADR-ALERTING-003
// precedence: when BOTH an operator's maintenance window and a down dependency
// apply to the same firing, the maintenance window suppresses FIRST and the
// dependency checker is not consulted at all — the operator's explicit
// declaration is what the metric/log trail attributes the suppression to, and
// the second check is short-circuited.
func TestEvaluator_MaintenanceWindowPrecedesDependencyCheck(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rule := mkRule(t, "tenant-a", "asset-x", "cpu_utilization", domain.ComparatorGT, 90, 300)

	store := newFakeStore(rule)
	tel := newFakeTelemetry()
	notifier := &fakeNotifier{}
	maintenance := newFakeMaintenanceChecker(fakeWindow{
		tenantID: rule.TenantID, assetID: rule.AssetID,
		startsAt: now.Add(-time.Hour), endsAt: now.Add(time.Hour),
	})
	dependency := newFakeDependencyChecker(fakeDownDependency{
		tenantID: rule.TenantID, assetID: rule.AssetID, rootAssetID: "db-y",
	})
	e := newTestEvaluatorWithDependency(store, tel, notifier, nil, maintenance, dependency, now)

	from := now.Add(-300 * time.Second)
	tel.set(rule.TenantID, rule.AssetID, rule.Metric, breachingSamples(from, now, 30*time.Second, 95))
	e.RunOnce(context.Background())

	if got := notifier.count(); got != 0 {
		t.Fatalf("notifications when both suppressions apply = %d, want 0", got)
	}
	if got := maintenance.suppressions(); got != 1 {
		t.Errorf("maintenance suppressions = %d, want 1 (it suppresses first)", got)
	}
	if got := dependency.callCount(); got != 0 {
		t.Errorf("dependency checker consulted %d times when a maintenance window already suppressed = %d, "+
			"want 0 (maintenance must short-circuit the dependency check)", got, got)
	}
}

// TestEvaluator_DependencyCheckIsNotVacuous is the mutation proof the story
// requires: with the check defeated (NopDependencySuppressionChecker, which
// always reports "nothing down" — reproducing what deleting the
// dependencySuppressed call in evaluateRule would do) the exact scenario that
// must suppress in TestEvaluator_DependencyDownSuppressesFiringNoIncidentNoNotify
// instead FIRES. The two tests' assertions are opposite, so the suite would
// fail if the evaluator's real dependencySuppressed call were removed.
func TestEvaluator_DependencyCheckIsNotVacuous(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rule := mkRule(t, "tenant-a", "asset-x", "cpu_utilization", domain.ComparatorGT, 90, 300)

	store := newFakeStore(rule)
	tel := newFakeTelemetry()
	notifier := &fakeNotifier{}
	// NopDependencySuppressionChecker always reports "nothing down" — standing
	// in for the evaluator having stopped consulting the check at all.
	e := newTestEvaluatorWithDependency(store, tel, notifier, nil, newFakeMaintenanceChecker(), NopDependencySuppressionChecker{}, now)

	from := now.Add(-300 * time.Second)
	tel.set(rule.TenantID, rule.AssetID, rule.Metric, breachingSamples(from, now, 30*time.Second, 95))
	e.RunOnce(context.Background())

	if got := notifier.count(); got != 1 {
		t.Fatalf("with the dependency check defeated, notifications = %d, want 1 (proves the real check, not "+
			"this test, is what makes the suppressing test pass)", got)
	}
}

// TestEvaluator_ResourceSymptomNeverDependencySuppressed is E3.5's (ADR-
// ALERTING-006) core payoff: a resource-class rule (e.g. disk/cpu) on X pages
// normally even while a dependency of X is down, because a resource symptom
// is usually independent of a dependency's own health — suppressing it would
// mask a genuine, independent failure. This is the mutation proof the story
// requires: with the `rule.SymptomClass == domain.AlertSymptomClassResource`
// gate at the top of dependencySuppressed removed, this rule would take the
// exact fixture TestEvaluator_DependencyDownSuppressesFiringNoIncidentNoNotify
// uses and get suppressed instead — this test's assertions (fires, notifies,
// consults the checker zero times) would flip to FAIL.
func TestEvaluator_ResourceSymptomNeverDependencySuppressed(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rule := mkRule(t, "tenant-a", "asset-x", "disk_utilization", domain.ComparatorGT, 90, 300)
	rule.SymptomClass = domain.AlertSymptomClassResource

	store := newFakeStore(rule)
	tel := newFakeTelemetry()
	notifier := &fakeNotifier{}
	correlator := newFakeCorrelator()
	dependency := newFakeDependencyChecker(fakeDownDependency{
		tenantID: rule.TenantID, assetID: rule.AssetID, rootAssetID: "db-y",
	})
	e := newTestEvaluatorWithDependency(store, tel, notifier, correlator, newFakeMaintenanceChecker(), dependency, now)

	from := now.Add(-300 * time.Second)
	tel.set(rule.TenantID, rule.AssetID, rule.Metric, breachingSamples(from, now, 30*time.Second, 95))

	e.RunOnce(context.Background())

	if got := notifier.count(); got != 1 {
		t.Errorf("notifications for a resource-class symptom with a down dependency = %d, want 1 (never "+
			"dependency-suppressed)", got)
	}
	if creates := correlator.callsOfKind("create"); len(creates) != 1 {
		t.Errorf("incident creations for a resource-class symptom with a down dependency = %d, want 1", len(creates))
	}
	if got := store.get(rule.RuleID).LastState; got != domain.AlertRuleStateFiring {
		t.Errorf("last_state for a resource-class symptom with a down dependency = %q, want firing", got)
	}
	if got := dependency.callCount(); got != 0 {
		t.Errorf("dependency checker consulted %d times for a resource-class symptom, want 0 — the gate must "+
			"return before Suppress is ever called", got)
	}
	if got := dependency.suppressions(); got != 0 {
		t.Errorf("recorded suppressions for a resource-class symptom = %d, want 0 (nothing to record: the check "+
			"was never consulted)", got)
	}
}

// TestEvaluator_AvailabilitySymptomStillDependencySuppressed pins the
// unchanged half of E3.5: an availability-class symptom on X (reachability)
// legitimately cascades from a down dependency, so it is suppressed exactly
// as it was before symptom-class scoping existed — mirrors
// TestEvaluator_DependencyDownSuppressesFiringNoIncidentNoNotify with the
// class made explicit.
func TestEvaluator_AvailabilitySymptomStillDependencySuppressed(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rule := mkRule(t, "tenant-a", "asset-x", "up", domain.ComparatorLT, 1, 300)
	rule.SymptomClass = domain.AlertSymptomClassAvailability

	store := newFakeStore(rule)
	tel := newFakeTelemetry()
	notifier := &fakeNotifier{}
	correlator := newFakeCorrelator()
	dependency := newFakeDependencyChecker(fakeDownDependency{
		tenantID: rule.TenantID, assetID: rule.AssetID, rootAssetID: "db-y",
	})
	e := newTestEvaluatorWithDependency(store, tel, notifier, correlator, newFakeMaintenanceChecker(), dependency, now)

	from := now.Add(-300 * time.Second)
	tel.set(rule.TenantID, rule.AssetID, rule.Metric, breachingSamples(from, now, 30*time.Second, 0))

	e.RunOnce(context.Background())

	if got := notifier.count(); got != 0 {
		t.Errorf("notifications for an availability-class symptom while a dependency is down = %d, want 0 "+
			"(cascading symptom, unchanged behavior)", got)
	}
	if creates := correlator.callsOfKind("create"); len(creates) != 0 {
		t.Errorf("incident creations for an availability-class symptom while a dependency is down = %d, want 0", len(creates))
	}
	if got := store.get(rule.RuleID).LastState; got != domain.AlertRuleStateOK {
		t.Errorf("last_state during suppression = %q, want ok (not committed while suppressed)", got)
	}
	if got := dependency.callCount(); got != 1 {
		t.Errorf("dependency checker consulted %d times for an availability-class symptom, want exactly 1", got)
	}
	if got := dependency.suppressions(); got != 1 {
		t.Errorf("recorded suppressions for an availability-class symptom = %d, want exactly 1", got)
	}
}

// TestEvaluator_UnspecifiedSymptomStillDependencySuppressed pins E3.4's
// non-breaking guarantee at the E3.5 call site: an unspecified-class rule
// (the default for every pre-E3.4 rule and every rule an operator never
// classified) keeps today's conservative behavior — suppressed exactly as it
// was before symptom-class scoping existed.
func TestEvaluator_UnspecifiedSymptomStillDependencySuppressed(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rule := mkRule(t, "tenant-a", "asset-x", "cpu_utilization", domain.ComparatorGT, 90, 300)
	rule.SymptomClass = domain.AlertSymptomClassUnspecified

	store := newFakeStore(rule)
	tel := newFakeTelemetry()
	notifier := &fakeNotifier{}
	correlator := newFakeCorrelator()
	dependency := newFakeDependencyChecker(fakeDownDependency{
		tenantID: rule.TenantID, assetID: rule.AssetID, rootAssetID: "db-y",
	})
	e := newTestEvaluatorWithDependency(store, tel, notifier, correlator, newFakeMaintenanceChecker(), dependency, now)

	from := now.Add(-300 * time.Second)
	tel.set(rule.TenantID, rule.AssetID, rule.Metric, breachingSamples(from, now, 30*time.Second, 95))

	e.RunOnce(context.Background())

	if got := notifier.count(); got != 0 {
		t.Errorf("notifications for an unspecified-class symptom while a dependency is down = %d, want 0 "+
			"(conservative default, unchanged behavior)", got)
	}
	if creates := correlator.callsOfKind("create"); len(creates) != 0 {
		t.Errorf("incident creations for an unspecified-class symptom while a dependency is down = %d, want 0", len(creates))
	}
	if got := store.get(rule.RuleID).LastState; got != domain.AlertRuleStateOK {
		t.Errorf("last_state during suppression = %q, want ok (not committed while suppressed)", got)
	}
	if got := dependency.callCount(); got != 1 {
		t.Errorf("dependency checker consulted %d times for an unspecified-class symptom, want exactly 1", got)
	}
	if got := dependency.suppressions(); got != 1 {
		t.Errorf("recorded suppressions for an unspecified-class symptom = %d, want exactly 1", got)
	}
}
