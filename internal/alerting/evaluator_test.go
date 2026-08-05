package alerting

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// newTestEvaluator builds an Evaluator with no IncidentCorrelator wired
// (E4.1 correlation is skipped entirely) — the right default for every test
// in this file that predates E4.1 and is not itself testing correlation.
func newTestEvaluator(store Store, tel TelemetryReader, notifier Notifier, now time.Time) *Evaluator {
	return newTestEvaluatorWithCorrelator(store, tel, notifier, nil, now)
}

func newTestEvaluatorWithCorrelator(store Store, tel TelemetryReader, notifier Notifier, correlator IncidentCorrelator, now time.Time) *Evaluator {
	return newTestEvaluatorWithMaintenance(store, tel, notifier, correlator, newFakeMaintenanceChecker(), now)
}

// newTestEvaluatorWithMaintenance delegates to newTestEvaluatorWithDependency
// with a dependency checker that never suppresses (newFakeDependencyChecker's
// zero value), for every test that needs to control the maintenance-window
// checker but not E3.3b's dependency checker.
func newTestEvaluatorWithMaintenance(
	store Store, tel TelemetryReader, notifier Notifier, correlator IncidentCorrelator,
	maintenance MaintenanceWindowChecker, now time.Time,
) *Evaluator {
	return newTestEvaluatorWithDependency(store, tel, notifier, correlator, maintenance, newFakeDependencyChecker(), now)
}

// newTestEvaluatorWithDependency is the fully-general constructor every
// other helper in this file delegates to, for the tests in
// dependency_suppression_test.go that need to control what the
// dependency-suppression checker reports.
func newTestEvaluatorWithDependency(
	store Store, tel TelemetryReader, notifier Notifier, correlator IncidentCorrelator,
	maintenance MaintenanceWindowChecker, dependency DependencySuppressionChecker, now time.Time,
) *Evaluator {
	e := NewEvaluator(store, tel, notifier, correlator, maintenance, dependency, NopMetrics{}, quiet(), Config{Concurrency: 4})
	e.now = func() time.Time { return now }
	return e
}

// TestEvaluator_FiresOnceThenRecoversOnce is the transition-only, no-spam
// proof: a sustained breach fires exactly once (not once per tick), a
// continued breach fires no further notification, and a subsequent recovery
// fires exactly once more.
func TestEvaluator_FiresOnceThenRecoversOnce(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rule := mkRule(t, "tenant-a", "asset-1", "cpu_utilization", domain.ComparatorGT, 90, 300)

	store := newFakeStore(rule)
	tel := newFakeTelemetry()
	notifier := &fakeNotifier{}
	e := newTestEvaluator(store, tel, notifier, now)

	from := now.Add(-300 * time.Second)
	tel.set(rule.TenantID, rule.AssetID, rule.Metric, breachingSamples(from, now, 30*time.Second, 95))

	// Three ticks while still breaching: exactly one notification total.
	e.RunOnce(context.Background())
	e.RunOnce(context.Background())
	e.RunOnce(context.Background())

	if got := notifier.count(); got != 1 {
		t.Fatalf("notifications after sustained breach across 3 ticks = %d, want 1 (transition-only)", got)
	}
	if got := store.get(rule.RuleID).LastState; got != domain.AlertRuleStateFiring {
		t.Fatalf("last_state = %q, want firing", got)
	}

	// Recovery: values now below threshold across the whole window.
	tel.set(rule.TenantID, rule.AssetID, rule.Metric, breachingSamples(from, now, 30*time.Second, 10))
	e.RunOnce(context.Background())
	e.RunOnce(context.Background())

	if got := notifier.count(); got != 2 {
		t.Fatalf("notifications after recovery across 2 more ticks = %d, want 2 (one fire + one recover)", got)
	}
	if got := store.get(rule.RuleID).LastState; got != domain.AlertRuleStateOK {
		t.Fatalf("last_state after recovery = %q, want ok", got)
	}
}

// TestEvaluator_ShortBreachDoesNotFire proves a breach that has not covered
// the full ForDuration window does not fire, even though every sample that
// DOES exist breaches.
func TestEvaluator_ShortBreachDoesNotFire(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rule := mkRule(t, "tenant-a", "asset-1", "cpu_utilization", domain.ComparatorGT, 90, 300)

	store := newFakeStore(rule)
	tel := newFakeTelemetry()
	notifier := &fakeNotifier{}
	e := newTestEvaluator(store, tel, notifier, now)

	// Only the last 20s of the 300s window has data — far short of sustained.
	tel.set(rule.TenantID, rule.AssetID, rule.Metric,
		breachingSamples(now.Add(-20*time.Second), now, 10*time.Second, 95))

	e.RunOnce(context.Background())

	if got := notifier.count(); got != 0 {
		t.Fatalf("notifications for a short breach = %d, want 0", got)
	}
	if got := store.get(rule.RuleID).LastState; got != domain.AlertRuleStateOK {
		t.Fatalf("last_state = %q, want ok (unchanged)", got)
	}
}

// TestEvaluator_BelowThresholdDoesNotFire proves full window coverage with
// values that never cross the threshold does not fire.
func TestEvaluator_BelowThresholdDoesNotFire(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rule := mkRule(t, "tenant-a", "asset-1", "cpu_utilization", domain.ComparatorGT, 90, 300)

	store := newFakeStore(rule)
	tel := newFakeTelemetry()
	notifier := &fakeNotifier{}
	e := newTestEvaluator(store, tel, notifier, now)

	tel.set(rule.TenantID, rule.AssetID, rule.Metric,
		breachingSamples(now.Add(-300*time.Second), now, 30*time.Second, 50))

	e.RunOnce(context.Background())

	if got := notifier.count(); got != 0 {
		t.Fatalf("notifications below threshold = %d, want 0", got)
	}
}

// TestEvaluator_NoDataDoesNotFire proves a rule whose asset has reported no
// telemetry at all in the window does not fire — absence of evidence is not
// evidence of a sustained breach.
func TestEvaluator_NoDataDoesNotFire(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rule := mkRule(t, "tenant-a", "asset-1", "cpu_utilization", domain.ComparatorGT, 90, 300)

	store := newFakeStore(rule)
	tel := newFakeTelemetry()
	notifier := &fakeNotifier{}
	e := newTestEvaluator(store, tel, notifier, now)

	e.RunOnce(context.Background())

	if got := notifier.count(); got != 0 {
		t.Fatalf("notifications with no telemetry data = %d, want 0", got)
	}
}

// TestEvaluator_LongWindowSeesRecentRecoveryNotStaleBreach is the bite test
// for the truncation direction TelemetryReader.QueryRangeForTenant's doc
// comment records: a window whose true sample count exceeds
// domain.MaxTelemetryQueryLimit must be evaluated over its MOST RECENT
// cap-worth of samples, not its oldest.
//
// A 24h ForDuration on a metric reporting more often than once per ~17s
// produces more than MaxTelemetryQueryLimit (5000) samples in one window
// (e.g. one every 10s ≈ 8640). This test gives the evaluator exactly the two
// shapes of MaxTelemetryQueryLimit-sized slice QueryRangeForTenant could, in
// principle, hand back for such a window: fetched-newest (the fix — recovery
// near "now" is visible) and fetched-oldest (the bug this guards against —
// only stale, still-breaching data from early in the window is visible,
// because the recovery lives past where an oldest-first LIMIT would ever
// reach). Only the newest-first shape may result in a fire.
func TestEvaluator_LongWindowSeesRecentRecoveryNotStaleBreach(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rule := mkRule(t, "tenant-a", "asset-1", "cpu_utilization", domain.ComparatorGT, 90, 24*3600)
	from := now.Add(-24 * time.Hour)

	n := domain.MaxTelemetryQueryLimit
	step := 24 * time.Hour / time.Duration(n+1)

	// What QueryRangeForTenant now actually returns for such a window: the
	// most recent n samples, oldest-first — a breach through most of the cap,
	// recovered in the final few samples nearest "now".
	mostRecent := make([]domain.Sample, n)
	for i := 0; i < n; i++ {
		value := 95.0
		if i >= n-10 {
			value = 10.0 // recovered in the tail closest to "now"
		}
		mostRecent[i] = domain.Sample{Timestamp: from.Add(step * time.Duration(i+1)), Value: value}
	}

	store := newFakeStore(rule)
	tel := newFakeTelemetry()
	tel.set(rule.TenantID, rule.AssetID, rule.Metric, mostRecent)
	notifier := &fakeNotifier{}
	e := newTestEvaluator(store, tel, notifier, now)

	e.RunOnce(context.Background())

	if got := notifier.count(); got != 0 {
		t.Fatalf("notifications for a long window whose recent tail recovered = %d, want 0 "+
			"(the evaluator must see the recovery, not just stale early-window breach data)", got)
	}
	if got := store.get(rule.RuleID).LastState; got != domain.AlertRuleStateOK {
		t.Fatalf("last_state = %q, want ok", got)
	}

	// Sanity check / regression guard: what the OLD `ORDER BY ts ASC LIMIT n`
	// would have handed back for the identical real-world window — the
	// oldest n samples, none of which have recovered yet, because the
	// recovery is past the cap in timestamp order. This shape must still
	// fire; if it stopped firing too, sustainedBreach's capped-window branch
	// would be broken in the other direction (never firing a real, sustained,
	// still-current breach).
	oldestFirst := make([]domain.Sample, n)
	for i := 0; i < n; i++ {
		oldestFirst[i] = domain.Sample{Timestamp: from.Add(step * time.Duration(i+1)), Value: 95}
	}
	store2 := newFakeStore(rule)
	tel2 := newFakeTelemetry()
	tel2.set(rule.TenantID, rule.AssetID, rule.Metric, oldestFirst)
	notifier2 := &fakeNotifier{}
	e2 := newTestEvaluator(store2, tel2, notifier2, now)

	e2.RunOnce(context.Background())

	if got := notifier2.count(); got != 1 {
		t.Fatalf("sanity check failed: a capped, fully-breaching sample set must still fire "+
			"(notifications = %d, want 1) — otherwise the capped branch never fires anything", got)
	}
}

// TestSustainedBreach_CappedWindowSkipsStartCoverageCheck is the direct,
// white-box proof of sustainedBreach's capped branch: a metric reporting far
// more often than ForDuration/MaxTelemetryQueryLimit fills the cap with
// samples that do NOT reach back to the window's nominal start at all (e.g.
// one sample/second against a 24h ForDuration: the most recent
// MaxTelemetryQueryLimit samples span only its most recent ~83 minutes). That
// must still fire on what it has — refusing to fire a genuinely sustained,
// currently-breaching metric merely because the window is long and the
// metric is high-frequency would be a regression in the opposite direction
// from the one this fix closes.
func TestSustainedBreach_CappedWindowSkipsStartCoverageCheck(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	from := now.Add(-24 * time.Hour)

	n := domain.MaxTelemetryQueryLimit
	samples := make([]domain.Sample, n)
	for i := 0; i < n; i++ {
		samples[i] = domain.Sample{Timestamp: now.Add(-time.Duration(n-i) * time.Second), Value: 95}
	}
	// samples[0] is ~83 minutes before "now" — nowhere near `from` (24h ago).

	if !sustainedBreach(samples, domain.ComparatorGT, 90, from) {
		t.Fatal("a fully-breaching, capped, most-recent sample set must fire even though it " +
			"does not reach back to the window's nominal start")
	}
}

// TestSustainedBreach_UncappedWindowStillRequiresStartCoverage proves the
// capped branch above did not weaken the common, unbounded case: a short
// breach that does not cover the window's start must still not count as
// sustained (TestEvaluator_ShortBreachDoesNotFire's proof, restated directly
// against sustainedBreach).
func TestSustainedBreach_UncappedWindowStillRequiresStartCoverage(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	from := now.Add(-300 * time.Second)
	samples := []domain.Sample{{Timestamp: now.Add(-20 * time.Second), Value: 95}}

	if sustainedBreach(samples, domain.ComparatorGT, 90, from) {
		t.Fatal("a short, uncapped breach must not count as sustained")
	}
}

// TestEvaluator_CrossTenantTelemetryIsolation is the make-or-break proof for
// E3.1's #1 risk: the evaluator processes every tenant's rules from one
// privileged process, concurrently (Config.Concurrency > 1). A rule for
// tenant A must be evaluated ONLY against tenant A's own telemetry, never
// against another tenant's — even when many tenants' rules share the exact
// same asset_id/metric string (an adversarial collision a real, globally
// unique asset id would not normally produce, but the evaluator's own
// dispatch must not depend on that not happening: it must pass each rule's
// own TenantID to every telemetry read, never a neighbour's).
//
// Mutation-verified: passing a shared/wrong tenant id into
// QueryRangeForTenant (e.g. reusing one loop-scoped variable across
// goroutines, or hard-coding any single rule's tenant) makes tenant B's rule
// fire on tenant A's breaching data — this test catches that by configuring
// DIFFERENT, mutually exclusive sample sets per tenant on the identical
// asset_id/metric and asserting each rule's verdict matches only its own
// tenant's data.
func TestEvaluator_CrossTenantTelemetryIsolation(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	from := now.Add(-300 * time.Second)

	const sharedAsset = "asset-shared"
	const sharedMetric = "cpu_utilization"

	tel := newFakeTelemetry()
	var rules []*domain.AlertRule
	for i := 0; i < 20; i++ {
		tenantID := fmt.Sprintf("tenant-%02d", i)
		rule := mkRule(t, tenantID, sharedAsset, sharedMetric, domain.ComparatorGT, 90, 300)
		rules = append(rules, rule)
		if i%2 == 0 {
			// Even tenants: sustained breach.
			tel.set(tenantID, sharedAsset, sharedMetric, breachingSamples(from, now, 30*time.Second, 95))
		} else {
			// Odd tenants: sustained NON-breach. If the evaluator ever crossed
			// wires, an odd tenant would inherit an even neighbour's breaching
			// data and fire.
			tel.set(tenantID, sharedAsset, sharedMetric, breachingSamples(from, now, 30*time.Second, 10))
		}
	}

	store := newFakeStore(rules...)
	notifier := &fakeNotifier{}
	e := NewEvaluator(store, tel, notifier, nil, newFakeMaintenanceChecker(), newFakeDependencyChecker(), NopMetrics{}, quiet(), Config{Concurrency: 8})
	e.now = func() time.Time { return now }

	e.RunOnce(context.Background())

	for i, rule := range rules {
		got := store.get(rule.RuleID).LastState
		want := domain.AlertRuleStateOK
		if i%2 == 0 {
			want = domain.AlertRuleStateFiring
		}
		if got != want {
			t.Errorf("tenant %s (asset/metric shared with 19 others): last_state = %q, want %q — "+
				"cross-tenant telemetry read defense did not bite", rule.TenantID, got, want)
		}
		calls := tel.callsFor(rule.TenantID)
		if len(calls) != 1 {
			t.Fatalf("tenant %s: %d telemetry calls recorded under its own tenant id, want 1", rule.TenantID, len(calls))
		}
		if calls[0].tenantID != rule.TenantID {
			t.Errorf("tenant %s: telemetry call carried tenant id %q", rule.TenantID, calls[0].tenantID)
		}
	}
	// Ten even tenants fire, ten odd tenants do not.
	if got := notifier.count(); got != 10 {
		t.Fatalf("notifications = %d, want 10 (one per breaching tenant, zero cross-tenant leakage)", got)
	}
}

// TestEvaluator_DisabledOrConcurrentlyEditedRuleTransitionIsSkipped proves a
// RecordTransition version mismatch (the rule was edited or disabled between
// this tick's read and this tick's write) is handled without crashing the
// pass or double-processing the rest of the batch.
func TestEvaluator_ConcurrentEditDuringTransitionIsHandled(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rule := mkRule(t, "tenant-a", "asset-1", "cpu_utilization", domain.ComparatorGT, 90, 300)

	store := newFakeStore(rule)
	tel := newFakeTelemetry()
	notifier := &fakeNotifier{}
	e := newTestEvaluator(store, tel, notifier, now)

	from := now.Add(-300 * time.Second)
	tel.set(rule.TenantID, rule.AssetID, rule.Metric, breachingSamples(from, now, 30*time.Second, 95))

	// Simulate a concurrent PATCH bumping row_version in the exact window
	// between this tick's read (already done by the time notify runs) and
	// its RecordTransition write.
	notifier.hook = func(*domain.Notification) {
		store.mu.Lock()
		store.rules[rule.RuleID].RowVersion = 99
		store.mu.Unlock()
	}

	e.RunOnce(context.Background())

	// The notification was still sent (enqueued before the CAS), but the
	// state was not clobbered — see Evaluator.evaluateRule's doc comment for
	// why "duplicate over silent loss" is the accepted residual here.
	if got := notifier.count(); got != 1 {
		t.Fatalf("notifications = %d, want 1", got)
	}
	if got := store.get(rule.RuleID).LastState; got != domain.AlertRuleStateOK {
		t.Fatalf("last_state = %q, want ok (the concurrent edit's version won, not this tick's)", got)
	}
}
