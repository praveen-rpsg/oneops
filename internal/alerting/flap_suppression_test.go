package alerting

import (
	"context"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// flapClock is a mutable, test-controlled clock. Every pre-E3.2 test in this
// package uses one FIXED `now` across every RunOnce call — flap suppression
// is entirely about behaviour ACROSS elapsed wall-clock time, so the tests
// below need to advance it tick over tick instead.
type flapClock struct{ t time.Time }

func (c *flapClock) now() time.Time { return c.t }

// setMetric replaces the fake telemetry's entire sample set for rule's
// asset/metric with one uniform value spanning rule's whole ForDuration
// window ending at `at`, so a RunOnce evaluated at `at` sees a window that is
// either entirely breaching or entirely not — sustainedBreach's own "every
// sample breaches" requirement (evaluator.go) means the CANDIDATE state
// flips cleanly and deterministically between calls, isolating these tests
// from telemetry-window nuances (already covered by evaluator_test.go) so
// they can focus purely on the dwell.
func setMetric(tel *fakeTelemetry, rule *domain.AlertRule, at time.Time, breaching bool) {
	value := 10.0
	if breaching {
		value = 95.0
	}
	from := at.Add(-time.Duration(rule.ForDuration) * time.Second)
	tel.set(rule.TenantID, rule.AssetID, rule.Metric, breachingSamples(from, at, 10*time.Second, value))
}

// TestEvaluator_FlapSuppression_OscillationCollapsesToOneTransition is the
// non-vacuous suppression proof: a metric crossing its threshold back and
// forth well inside the configured dwell produces ZERO notifications until
// it finally settles, and then exactly ONE — never one per crossing. Each of
// the five flips below individually satisfies ForDuration on its own (every
// sample in its own window uniformly breaches or doesn't), so under E3.1's
// original transition-only logic (no dwell) every single one would have
// fired its own notification: five, not one.
func TestEvaluator_FlapSuppression_OscillationCollapsesToOneTransition(t *testing.T) {
	clock := &flapClock{t: time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)}
	start := clock.t
	rule := mkRule(t, "tenant-a", "asset-1", "cpu_utilization", domain.ComparatorGT, 90, 60)
	rule.FlapDwellSeconds = 120

	store := newFakeStore(rule)
	tel := newFakeTelemetry()
	notifier := &fakeNotifier{}
	e := NewEvaluator(store, tel, notifier, nil, NopMetrics{}, quiet(), Config{Concurrency: 1})
	e.now = clock.now

	tick := func(breaching bool) {
		setMetric(tel, rule, clock.t, breaching)
		e.RunOnce(context.Background())
	}

	// Oscillate faster than the 120s dwell can ever complete: breach,
	// recover, breach, recover, breach — each candidate held only 20s.
	tick(true)                            // t+0:   candidate firing (pending starts)
	clock.t = start.Add(20 * time.Second) // t+20
	tick(false)                           //        candidate ok == LastState -> pending cleared
	clock.t = start.Add(40 * time.Second) // t+40
	tick(true)                            //        candidate firing (pending restarts, fresh clock)
	clock.t = start.Add(60 * time.Second) // t+60
	tick(false)                           //        candidate ok == LastState -> pending cleared again
	clock.t = start.Add(80 * time.Second) // t+80
	tick(true)                            //        candidate firing (pending restarts one more time)

	if got := notifier.count(); got != 0 {
		t.Fatalf("notifications mid-oscillation = %d, want 0 (nothing has stayed stable for the dwell yet)", got)
	}
	if got := store.get(rule.RuleID).LastState; got != domain.AlertRuleStateOK {
		t.Fatalf("last_state mid-oscillation = %q, want ok (unchanged)", got)
	}

	// Now the metric actually settles: held breaching continuously from t+80.
	clock.t = start.Add(140 * time.Second) // 60s into this dwell (120s) — not yet enough
	tick(true)
	if got := notifier.count(); got != 0 {
		t.Fatalf("notifications before this dwell elapsed = %d, want 0", got)
	}

	clock.t = start.Add(200 * time.Second) // 120s since t+80: dwell satisfied
	tick(true)

	if got := notifier.count(); got != 1 {
		t.Fatalf("notifications once the oscillation finally settled = %d, want exactly 1 (at most one "+
			"transition per window, not one per crossing)", got)
	}
	if got := store.get(rule.RuleID).LastState; got != domain.AlertRuleStateFiring {
		t.Fatalf("last_state = %q, want firing", got)
	}
}

// TestEvaluator_FlapSuppression_SustainedChangeStillTransitionsPromptly is
// the non-vacuous "still works" proof required alongside suppression: a
// GENUINE, uninterrupted change must still transition, and must do so as
// soon as its own dwell elapses — never later, and never suppressed forever.
// Both directions are proven: firing requires its own dwell, and recovery
// requires its own dwell measured independently (a momentary dip back under
// threshold must not let a later, unrelated recovery "borrow" time from an
// earlier attempt — this test starts the recovery dwell's clock fresh).
func TestEvaluator_FlapSuppression_SustainedChangeStillTransitionsPromptly(t *testing.T) {
	clock := &flapClock{t: time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)}
	start := clock.t
	rule := mkRule(t, "tenant-a", "asset-1", "cpu_utilization", domain.ComparatorGT, 90, 60)
	rule.FlapDwellSeconds = 90

	store := newFakeStore(rule)
	tel := newFakeTelemetry()
	notifier := &fakeNotifier{}
	e := NewEvaluator(store, tel, notifier, nil, NopMetrics{}, quiet(), Config{Concurrency: 1})
	e.now = clock.now

	tick := func(breaching bool) {
		setMetric(tel, rule, clock.t, breaching)
		e.RunOnce(context.Background())
	}

	tick(true) // t+0: candidate first departs from ok; dwell starts

	clock.t = start.Add(30 * time.Second)
	tick(true)
	if got := notifier.count(); got != 0 {
		t.Fatalf("notified at t+30 (dwell=90s) = %d, want 0", got)
	}

	clock.t = start.Add(89 * time.Second)
	tick(true)
	if got := notifier.count(); got != 0 {
		t.Fatalf("notified 1s before the dwell elapsed = %d, want 0", got)
	}

	clock.t = start.Add(90 * time.Second)
	tick(true)
	if got := notifier.count(); got != 1 {
		t.Fatalf("notifications once the sustained breach's dwell elapsed = %d, want exactly 1 — "+
			"a genuine, uninterrupted change must not be blocked", got)
	}
	if got := store.get(rule.RuleID).LastState; got != domain.AlertRuleStateFiring {
		t.Fatalf("last_state = %q, want firing", got)
	}

	// Recovery: the identical proof, the other direction. Its dwell clock
	// starts fresh at the first tick that observes the recovered candidate.
	recoverPendingStart := start.Add(100 * time.Second)
	clock.t = recoverPendingStart
	tick(false)
	if got := notifier.count(); got != 1 {
		t.Fatalf("notified the instant recovery was first observed = %d, want 1 (still just the firing)", got)
	}
	if got := store.get(rule.RuleID).LastState; got != domain.AlertRuleStateFiring {
		t.Fatalf("last_state before the recovery dwell elapsed = %q, want firing (unchanged)", got)
	}

	clock.t = recoverPendingStart.Add(89 * time.Second)
	tick(false)
	if got := notifier.count(); got != 1 {
		t.Fatalf("notified 1s before the recovery dwell elapsed = %d, want 1", got)
	}

	clock.t = recoverPendingStart.Add(90 * time.Second)
	tick(false)
	if got := notifier.count(); got != 2 {
		t.Fatalf("notifications once the sustained recovery's dwell elapsed = %d, want 2 (fire + recover)", got)
	}
	if got := store.get(rule.RuleID).LastState; got != domain.AlertRuleStateOK {
		t.Fatalf("last_state after recovery = %q, want ok", got)
	}
}

// TestEvaluator_FlapSuppression_PendingSurvivesNewEvaluatorInstance proves
// the dwell clock is carried entirely by the persisted row, not by anything
// held in the Evaluator's own memory: a brand new Evaluator instance sharing
// only the store — standing in for a process restart or a leader failover —
// still commits the transition exactly when the dwell started on the FIRST
// instance says it should, not one dwell-period late.
func TestEvaluator_FlapSuppression_PendingSurvivesNewEvaluatorInstance(t *testing.T) {
	clock := &flapClock{t: time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)}
	start := clock.t
	rule := mkRule(t, "tenant-a", "asset-1", "cpu_utilization", domain.ComparatorGT, 90, 60)
	rule.FlapDwellSeconds = 100

	store := newFakeStore(rule) // the only state a real restart/failover carries forward
	tel := newFakeTelemetry()

	notifier1 := &fakeNotifier{}
	e1 := NewEvaluator(store, tel, notifier1, nil, NopMetrics{}, quiet(), Config{Concurrency: 1})
	e1.now = clock.now

	setMetric(tel, rule, clock.t, true)
	e1.RunOnce(context.Background()) // pending firing starts at `start`, on e1

	if got := store.get(rule.RuleID).PendingState; got != domain.AlertRuleStateFiring {
		t.Fatalf("pending_state after e1's tick = %q, want firing", got)
	}
	if got := notifier1.count(); got != 0 {
		t.Fatalf("e1 notified on its very first tick = %d, want 0 (dwell not yet elapsed)", got)
	}

	// e1 is discarded here — nothing about it is reused. e2 knows only what
	// the store's row says.
	clock.t = start.Add(100 * time.Second)
	notifier2 := &fakeNotifier{}
	e2 := NewEvaluator(store, tel, notifier2, nil, NopMetrics{}, quiet(), Config{Concurrency: 1})
	e2.now = clock.now
	setMetric(tel, rule, clock.t, true)
	e2.RunOnce(context.Background())

	if got := notifier2.count(); got != 1 {
		t.Fatalf("notifications on the new evaluator instance once the dwell (started by e1) elapsed = %d, "+
			"want 1 — the dwell clock did not survive the restart", got)
	}
	if got := store.get(rule.RuleID).LastState; got != domain.AlertRuleStateFiring {
		t.Fatalf("last_state = %q, want firing", got)
	}
}

// TestEvaluator_FlapSuppression_ZeroDwellCommitsImmediately proves the
// explicit opt-out (FlapDwellSeconds == 0, mkRule's own default for every
// other test in this package) reproduces E3.1's original immediate-commit
// behaviour exactly, with no pending bookkeeping ever written.
func TestEvaluator_FlapSuppression_ZeroDwellCommitsImmediately(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rule := mkRule(t, "tenant-a", "asset-1", "cpu_utilization", domain.ComparatorGT, 90, 60)
	rule.FlapDwellSeconds = 0

	store := newFakeStore(rule)
	tel := newFakeTelemetry()
	notifier := &fakeNotifier{}
	e := newTestEvaluator(store, tel, notifier, now)

	setMetric(tel, rule, now, true)
	e.RunOnce(context.Background())

	if got := notifier.count(); got != 1 {
		t.Fatalf("notifications with flap_dwell_seconds=0 = %d, want 1 (immediate commit, no dwell)", got)
	}
	if got := store.get(rule.RuleID).LastState; got != domain.AlertRuleStateFiring {
		t.Fatalf("last_state = %q, want firing", got)
	}
	if got := store.get(rule.RuleID).PendingState; got != "" {
		t.Errorf("pending_state = %q, want empty — a dwell of 0 must never write pending bookkeeping at all", got)
	}
}
