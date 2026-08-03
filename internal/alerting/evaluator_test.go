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

func newTestEvaluator(store Store, tel TelemetryReader, notifier Notifier, now time.Time) *Evaluator {
	e := NewEvaluator(store, tel, notifier, NopMetrics{}, quiet(), Config{Concurrency: 4})
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
	e := NewEvaluator(store, tel, notifier, NopMetrics{}, quiet(), Config{Concurrency: 8})
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
