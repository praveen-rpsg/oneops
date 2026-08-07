package security

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newTestDetector(store Store, counter ObservationCounter, correlator IncidentCorrelator, now time.Time) *Detector {
	d := NewDetector(store, counter, correlator, NopMetrics{}, quiet(), Config{Concurrency: 4})
	d.now = func() time.Time { return now }
	return d
}

// TestSecurityDetector_FiresOnceThenRecoversOnce is the transition-only,
// no-spam proof — the security-detector analog of
// alerting.TestEvaluator_FiresOnceThenRecoversOnce: a sustained breach fires
// exactly once (not once per tick), a continued breach records no further
// transition, and a subsequent recovery transitions exactly once more.
func TestSecurityDetector_FiresOnceThenRecoversOnce(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rule := mkRule(t, "tenant-a", "asset-1", "port_scan", 300, 5)

	store := newFakeStore(rule)
	counter := newFakeCounter()
	correlator := newFakeCorrelator()
	d := newTestDetector(store, counter, correlator, now)

	counter.set(rule.TenantID, rule.AssetID, rule.ObservationType, 10) // >= threshold 5

	d.RunOnce(context.Background())
	d.RunOnce(context.Background())
	d.RunOnce(context.Background())

	if got := store.get(rule.RuleID).LastState; got != domain.SecurityRuleStateFiring {
		t.Fatalf("last_state = %q, want firing", got)
	}
	if got := len(correlator.callsOfKind("create")); got != 1 {
		t.Fatalf("incident creations across 3 firing ticks = %d, want 1 (transition-only)", got)
	}

	counter.set(rule.TenantID, rule.AssetID, rule.ObservationType, 1) // below threshold
	d.RunOnce(context.Background())
	d.RunOnce(context.Background())

	if got := store.get(rule.RuleID).LastState; got != domain.SecurityRuleStateOK {
		t.Fatalf("last_state after recovery = %q, want ok", got)
	}
	if got := len(correlator.callsOfKind("note")); got != 1 {
		t.Fatalf("recovery notes across 2 recovered ticks = %d, want 1 (transition-only)", got)
	}
}

// TestSecurityDetector_BelowThresholdDoesNotFire proves a count that never
// reaches ThresholdCount leaves the rule at ok with no correlation at all.
func TestSecurityDetector_BelowThresholdDoesNotFire(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rule := mkRule(t, "tenant-a", "asset-1", "port_scan", 300, 5)

	store := newFakeStore(rule)
	counter := newFakeCounter()
	correlator := newFakeCorrelator()
	d := newTestDetector(store, counter, correlator, now)

	counter.set(rule.TenantID, rule.AssetID, rule.ObservationType, 4) // < threshold 5
	d.RunOnce(context.Background())

	if got := store.get(rule.RuleID).LastState; got != domain.SecurityRuleStateOK {
		t.Fatalf("last_state = %q, want ok", got)
	}
	if got := len(correlator.callsOfKind("create")) + len(correlator.callsOfKind("link")); got != 0 {
		t.Fatalf("correlations for a below-threshold count = %d, want 0", got)
	}
}

// TestSecurityDetector_ThresholdIsInclusive proves a count exactly equal to
// ThresholdCount fires — "meets or exceeds", per domain.SecurityRule's own
// doc comment.
func TestSecurityDetector_ThresholdIsInclusive(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rule := mkRule(t, "tenant-a", "asset-1", "port_scan", 300, 5)

	store := newFakeStore(rule)
	counter := newFakeCounter()
	correlator := newFakeCorrelator()
	d := newTestDetector(store, counter, correlator, now)

	counter.set(rule.TenantID, rule.AssetID, rule.ObservationType, 5) // == threshold
	d.RunOnce(context.Background())

	if got := store.get(rule.RuleID).LastState; got != domain.SecurityRuleStateFiring {
		t.Fatalf("last_state = %q, want firing (threshold is inclusive)", got)
	}
}

// TestSecurityDetector_QueriesTheRuleOwnWindow proves the counter is called
// with a [now-WindowSeconds, now] window derived from the rule's own config.
func TestSecurityDetector_QueriesTheRuleOwnWindow(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rule := mkRule(t, "tenant-a", "asset-1", "port_scan", 300, 5)
	rule.MinSeverity = domain.ObservationSeverityHigh

	store := newFakeStore(rule)
	counter := newFakeCounter()
	correlator := newFakeCorrelator()
	d := newTestDetector(store, counter, correlator, now)

	d.RunOnce(context.Background())

	calls := counter.callsFor(rule.TenantID)
	if len(calls) != 1 {
		t.Fatalf("counter calls = %d, want 1", len(calls))
	}
	c := calls[0]
	if c.assetID != rule.AssetID || c.observationType != rule.ObservationType {
		t.Errorf("counted asset/type = %s/%s, want %s/%s", c.assetID, c.observationType, rule.AssetID, rule.ObservationType)
	}
	if c.minSeverity != domain.ObservationSeverityHigh {
		t.Errorf("counted min_severity = %q, want %q", c.minSeverity, domain.ObservationSeverityHigh)
	}
	wantFrom := now.Add(-300 * time.Second)
	if !c.from.Equal(wantFrom) || !c.to.Equal(now) {
		t.Errorf("counted window = [%s, %s], want [%s, %s]", c.from, c.to, wantFrom, now)
	}
}
