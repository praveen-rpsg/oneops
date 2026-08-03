package alerting

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// fakeStore is an in-memory alerting.Store, mirroring collector's fakeStore
// shape: EnabledRules pages the fixture set, RecordTransition performs the
// same fenced, single-row CAS the real store's UPDATE ... WHERE rule_id=$1
// AND row_version=$2 does.
type fakeStore struct {
	mu    sync.Mutex
	rules map[string]*domain.AlertRule
}

func newFakeStore(rules ...*domain.AlertRule) *fakeStore {
	s := &fakeStore{rules: map[string]*domain.AlertRule{}}
	for _, r := range rules {
		cp := *r
		s.rules[r.RuleID] = &cp
	}
	return s
}

func (f *fakeStore) EnabledRules(_ context.Context, limit int, after string) ([]*domain.AlertRule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for id, r := range f.rules {
		if r.Enabled && id > after {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if limit > 0 && len(ids) > limit {
		ids = ids[:limit]
	}
	out := make([]*domain.AlertRule, 0, len(ids))
	for _, id := range ids {
		cp := *f.rules[id]
		out = append(out, &cp)
	}
	return out, nil
}

func (f *fakeStore) RecordTransition(
	_ context.Context, ruleID string, rowVersion int64, state domain.AlertRuleState, at time.Time,
) (*domain.AlertRule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rules[ruleID]
	if !ok {
		return nil, domain.ErrNotFound
	}
	if r.RowVersion != rowVersion {
		return nil, domain.ErrVersionMismatch
	}
	r.LastState = state
	r.LastTransitionAt = at
	r.RowVersion++
	cp := *r
	return &cp, nil
}

func (f *fakeStore) get(ruleID string) *domain.AlertRule {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *f.rules[ruleID]
	return &cp
}

// telemetryCall records one QueryRangeForTenant invocation, for the
// cross-tenant-mixup test to assert against.
type telemetryCall struct {
	tenantID, assetID, metric string
	from, to                  time.Time
}

// fakeTelemetry is an in-memory alerting.TelemetryReader keyed by
// tenant+asset+metric, so two different tenants' rules pointed at the SAME
// asset_id/metric string (an adversarial/collision scenario a globally
// unique asset id would not normally produce, but the evaluator's isolation
// must not depend on that) can be configured with DIFFERENT sample sets and
// the test can prove neither ever receives the other's data.
type fakeTelemetry struct {
	mu      sync.Mutex
	samples map[string][]domain.Sample
	calls   []telemetryCall
}

func newFakeTelemetry() *fakeTelemetry {
	return &fakeTelemetry{samples: map[string][]domain.Sample{}}
}

func telemetryKey(tenantID, assetID, metric string) string {
	return tenantID + "|" + assetID + "|" + metric
}

func (f *fakeTelemetry) set(tenantID, assetID, metric string, samples []domain.Sample) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.samples[telemetryKey(tenantID, assetID, metric)] = samples
}

func (f *fakeTelemetry) QueryRangeForTenant(
	_ context.Context, tenantID, assetID, metric string, from, to time.Time,
) ([]domain.Sample, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, telemetryCall{tenantID, assetID, metric, from, to})
	return f.samples[telemetryKey(tenantID, assetID, metric)], nil
}

func (f *fakeTelemetry) callsFor(tenantID string) []telemetryCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []telemetryCall
	for _, c := range f.calls {
		if c.tenantID == tenantID {
			out = append(out, c)
		}
	}
	return out
}

// fakeNotifier is an in-memory alerting.Notifier, recording every enqueued
// notification for the transition-only tests to assert against. hook, when
// set, runs synchronously inside Enqueue — after the evaluator's read of the
// rule but before its RecordTransition write — so a test can inject a
// concurrent edit into exactly that window.
type fakeNotifier struct {
	mu   sync.Mutex
	sent []*domain.Notification
	hook func(n *domain.Notification)
}

func (f *fakeNotifier) Enqueue(_ context.Context, n *domain.Notification) (*domain.Notification, error) {
	f.mu.Lock()
	f.sent = append(f.sent, n)
	f.mu.Unlock()
	if f.hook != nil {
		f.hook(n)
	}
	return n, nil
}

func (f *fakeNotifier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func mkRule(t *testing.T, tenantID, assetID, metric string, cmp domain.AlertComparator, threshold float64, forDurationSeconds int) *domain.AlertRule {
	t.Helper()
	r, err := domain.NewAlertRule(tenantID, assetID, metric, cmp, threshold, forDurationSeconds, domain.AlertSeverityWarning)
	if err != nil {
		t.Fatalf("new alert rule: %v", err)
	}
	return r
}

func breachingSamples(from, to time.Time, step time.Duration, value float64) []domain.Sample {
	var out []domain.Sample
	for ts := from; !ts.After(to); ts = ts.Add(step) {
		out = append(out, domain.Sample{Timestamp: ts, Value: value})
	}
	return out
}
