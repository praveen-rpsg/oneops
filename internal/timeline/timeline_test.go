package timeline

import (
	"context"
	"testing"
	"time"
)

type fakeSource struct {
	auditByEvent map[string][]AuditRow
	auditByChain map[string][]AuditRow
	deliveries   map[string][]DeliveryRow // keyed by event id
	policyExecs  map[string][]PolicyRow   // keyed by event id
	jobs         map[string]ReplayRow
	execByID     map[string]PolicyRow
}

func (f *fakeSource) AuditByEventID(_ context.Context, id string) ([]AuditRow, error) {
	return f.auditByEvent[id], nil
}
func (f *fakeSource) AuditByChain(_ context.Context, chainID string, _ int) ([]AuditRow, error) {
	return f.auditByChain[chainID], nil
}
func (f *fakeSource) DeliveriesByEventIDs(_ context.Context, ids []string) ([]DeliveryRow, error) {
	var out []DeliveryRow
	for _, id := range ids {
		out = append(out, f.deliveries[id]...)
	}
	return out, nil
}
func (f *fakeSource) PolicyExecutionsByEventIDs(_ context.Context, ids []string) ([]PolicyRow, error) {
	var out []PolicyRow
	for _, id := range ids {
		out = append(out, f.policyExecs[id]...)
	}
	return out, nil
}
func (f *fakeSource) ReplayJob(_ context.Context, id string) (ReplayRow, bool, error) {
	r, ok := f.jobs[id]
	return r, ok, nil
}
func (f *fakeSource) PolicyExecution(_ context.Context, id string) (PolicyRow, bool, error) {
	p, ok := f.execByID[id]
	return p, ok, nil
}

var t0 = time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

func fullSource() *fakeSource {
	audit := AuditRow{ChainID: "c1", Seq: 1, EventID: "evt_1", OperationID: "op_1", Operation: "ratification", Actor: "user:alice", OccurredAt: t0}
	return &fakeSource{
		auditByEvent: map[string][]AuditRow{"evt_1": {audit}},
		auditByChain: map[string][]AuditRow{"c1": {audit}},
		deliveries: map[string][]DeliveryRow{"evt_1": {
			{ID: "dlv_1", WebhookID: "wh_1", EventID: "evt_1", Status: "delivered", StatusCode: 200, CreatedAt: t0.Add(time.Second), LastAttempt: t0.Add(2 * time.Second)},
		}},
		policyExecs: map[string][]PolicyRow{"evt_1": {
			{ID: "pex_1", PolicyID: "pol_1", EventID: "evt_1", Status: "succeeded", StartedAt: t0.Add(3 * time.Second), EndedAt: t0.Add(4 * time.Second), CreatedAt: t0.Add(3 * time.Second)},
		}},
		jobs:     map[string]ReplayRow{"rpl_1": {ID: "rpl_1", WebhookID: "wh_1", Status: "completed", EventsReplayed: 5, CreatedAt: t0, UpdatedAt: t0.Add(time.Minute)}},
		execByID: map[string]PolicyRow{"pex_1": {ID: "pex_1", PolicyID: "pol_1", EventID: "evt_1", Status: "succeeded", CreatedAt: t0.Add(3 * time.Second), EndedAt: t0.Add(4 * time.Second)}},
	}
}

func TestByEvent_ReflectsCommittedExecutionOrdered(t *testing.T) {
	svc := NewService(fullSource(), nil)
	page, err := svc.ByEvent(context.Background(), "evt_1", Filter{})
	if err != nil {
		t.Fatalf("ByEvent: %v", err)
	}
	// governance + audit (same ts) + webhook + policy = 4 entries, chronological.
	if len(page.Entries) != 4 {
		t.Fatalf("entries = %d, want 4: %+v", len(page.Entries), page.Entries)
	}
	wantOrder := []string{"governance", "audit", "webhook", "policy"}
	for i, want := range wantOrder {
		if page.Entries[i].Component != want {
			t.Fatalf("entry %d component = %q, want %q", i, page.Entries[i].Component, want)
		}
	}
	// Correlation correctness: every entry carries the event id.
	for _, e := range page.Entries {
		if e.Correlation["event_id"] != "evt_1" {
			t.Errorf("entry %s missing event correlation: %v", e.Component, e.Correlation)
		}
	}
	// Non-decreasing timestamps (deterministic order).
	for i := 1; i < len(page.Entries); i++ {
		if page.Entries[i].Timestamp.Before(page.Entries[i-1].Timestamp) {
			t.Fatalf("timeline not ordered at %d", i)
		}
	}
}

func TestOrdering_Deterministic(t *testing.T) {
	svc := NewService(fullSource(), nil)
	a, _ := svc.ByEvent(context.Background(), "evt_1", Filter{})
	b, _ := svc.ByEvent(context.Background(), "evt_1", Filter{})
	if len(a.Entries) != len(b.Entries) {
		t.Fatal("length differs across calls")
	}
	for i := range a.Entries {
		if a.Entries[i].Component != b.Entries[i].Component || !a.Entries[i].Timestamp.Equal(b.Entries[i].Timestamp) {
			t.Fatalf("ordering not deterministic at %d", i)
		}
	}
}

func TestFilter_ComponentAndStatusAndTime(t *testing.T) {
	svc := NewService(fullSource(), nil)

	only, _ := svc.ByEvent(context.Background(), "evt_1", Filter{Component: CompWebhook})
	if len(only.Entries) != 1 || only.Entries[0].Component != CompWebhook {
		t.Fatalf("component filter = %+v", only.Entries)
	}
	byStatus, _ := svc.ByEvent(context.Background(), "evt_1", Filter{Status: "committed"})
	if len(byStatus.Entries) != 1 || byStatus.Entries[0].Status != "committed" {
		t.Fatalf("status filter = %+v", byStatus.Entries)
	}
	// Time range excluding the audit/governance (t0) but keeping later entries.
	after, _ := svc.ByEvent(context.Background(), "evt_1", Filter{From: t0.Add(time.Second)})
	if len(after.Entries) != 2 { // webhook (t0+2s) + policy (t0+4s)
		t.Fatalf("time filter = %d entries, want 2", len(after.Entries))
	}
}

func TestPagination(t *testing.T) {
	svc := NewService(fullSource(), nil)
	p1, _ := svc.ByEvent(context.Background(), "evt_1", Filter{Limit: 2})
	if len(p1.Entries) != 2 || p1.NextOffset != "2" {
		t.Fatalf("page1 = %+v next=%q", p1.Entries, p1.NextOffset)
	}
	p2, _ := svc.ByEvent(context.Background(), "evt_1", Filter{Limit: 2, Offset: 2})
	if len(p2.Entries) != 2 || p2.NextOffset != "" {
		t.Fatalf("page2 = %+v next=%q", p2.Entries, p2.NextOffset)
	}
}

func TestByGovernance_Correlation(t *testing.T) {
	svc := NewService(fullSource(), nil)
	page, err := svc.ByGovernance(context.Background(), "c1", Filter{})
	if err != nil {
		t.Fatalf("ByGovernance: %v", err)
	}
	if len(page.Entries) != 4 {
		t.Fatalf("entries = %d, want 4", len(page.Entries))
	}
	for _, e := range page.Entries {
		if e.Correlation["governance_id"] != "c1" && e.Correlation["event_id"] != "evt_1" {
			t.Errorf("entry not correlated to governance c1: %+v", e.Correlation)
		}
	}
}

func TestByReplay(t *testing.T) {
	svc := NewService(fullSource(), nil)
	page, err := svc.ByReplay(context.Background(), "rpl_1", Filter{})
	if err != nil {
		t.Fatalf("ByReplay: %v", err)
	}
	if len(page.Entries) != 2 { // created + completed
		t.Fatalf("entries = %d, want 2", len(page.Entries))
	}
	if page.Entries[0].Action != "job_created" || page.Entries[1].Action != "job_completed" {
		t.Fatalf("replay actions = %q,%q", page.Entries[0].Action, page.Entries[1].Action)
	}
	// Unknown job -> ErrNotFound.
	if _, err := svc.ByReplay(context.Background(), "nope", Filter{}); err != ErrNotFound {
		t.Fatalf("unknown job err = %v", err)
	}
}

func TestByPolicyExecution_IncludesTriggeringEvent(t *testing.T) {
	svc := NewService(fullSource(), nil)
	page, err := svc.ByPolicyExecution(context.Background(), "pex_1", Filter{})
	if err != nil {
		t.Fatalf("ByPolicyExecution: %v", err)
	}
	// governance + audit (the triggering committed event) + policy execution = 3.
	if len(page.Entries) != 3 {
		t.Fatalf("entries = %d, want 3: %+v", len(page.Entries), page.Entries)
	}
	last := page.Entries[len(page.Entries)-1]
	if last.Component != CompPolicy || last.Correlation["policy_execution_id"] != "pex_1" {
		t.Fatalf("policy entry wrong: %+v", last)
	}
	if _, err := svc.ByPolicyExecution(context.Background(), "nope", Filter{}); err != ErrNotFound {
		t.Fatalf("unknown exec err = %v", err)
	}
}

func TestMetricsRecorded(t *testing.T) {
	m := &countMetrics{}
	svc := NewService(fullSource(), m)
	_, _ = svc.ByEvent(context.Background(), "evt_1", Filter{})
	if m.queries != 1 || m.durations != 1 {
		t.Fatalf("metrics = %+v", m)
	}
}

type countMetrics struct{ queries, durations int }

func (m *countMetrics) IncQuery()                     { m.queries++ }
func (m *countMetrics) ObserveDuration(time.Duration) { m.durations++ }
