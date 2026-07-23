package compliance

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/timeline"
)

type fakeGov struct {
	objs map[string]*domain.ConfigObject
	list []*domain.ConfigObject
	next string
}

func (f *fakeGov) Get(_ context.Context, id string) (*domain.ConfigObject, error) {
	o, ok := f.objs[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return o, nil
}
func (f *fakeGov) List(_ context.Context, _ domain.ListParams) (*domain.Page, error) {
	return &domain.Page{Items: f.list, NextCursor: f.next}, nil
}

type fakeVerifier struct{ res domain.VerifyResult }

func (f fakeVerifier) VerifyChain(_ context.Context, chainID string) (domain.VerifyResult, error) {
	r := f.res
	r.ChainID = chainID
	return r, nil
}

type fakeTimeline struct{ page timeline.Page }

func (f fakeTimeline) ByGovernance(_ context.Context, _ string, _ timeline.Filter) (timeline.Page, error) {
	return f.page, nil
}

var t0 = time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

func compliantSetup() (*fakeGov, fakeVerifier, fakeTimeline) {
	gov := &fakeGov{objs: map[string]*domain.ConfigObject{
		"c1": {CfgID: "c1", Lifecycle: domain.LifecycleRatified, RetentionClass: domain.RetentionCurrentBaseline,
			Authority: domain.AuthorityActive, RowVersion: 2, RatifiedBy: "user:alice", CreatedAt: t0, UpdatedAt: t0},
	}}
	ver := fakeVerifier{res: domain.VerifyResult{OK: true, Checked: 1, HeadSeq: 1}}
	tl := fakeTimeline{page: timeline.Page{Entries: []timeline.Entry{
		{Timestamp: t0, Component: timeline.CompGovernance, Action: "operation_committed", Status: "committed", Metadata: map[string]string{"operation": "ratification"}, Correlation: map[string]string{"event_id": "evt_1"}},
		{Timestamp: t0, Component: timeline.CompAudit, Action: "event_appended", Status: "sealed", Correlation: map[string]string{"event_id": "evt_1"}},
		{Timestamp: t0.Add(time.Second), Component: timeline.CompWebhook, Action: "delivery_delivered", Status: "delivered", Correlation: map[string]string{"delivery_id": "dlv_1", "event_id": "evt_1"}},
		{Timestamp: t0.Add(2 * time.Second), Component: timeline.CompPolicy, Action: "policy_succeeded", Status: "succeeded", Correlation: map[string]string{"policy_execution_id": "pex_1", "event_id": "evt_1"}},
	}}}
	return gov, ver, tl
}

func fixed(s *Service) *Service { s.now = func() time.Time { return t0 }; return s }

func TestEvidence_ReflectsCommittedExecutionAndCompliant(t *testing.T) {
	gov, ver, tl := compliantSetup()
	s := fixed(NewService(gov, ver, tl, nil))

	ev, err := s.Evidence(context.Background(), "c1")
	if err != nil {
		t.Fatalf("Evidence: %v", err)
	}
	if ev.Governance.Lifecycle != "ratified" || ev.Governance.RatifiedBy != "user:alice" {
		t.Fatalf("governance summary = %+v", ev.Governance)
	}
	if !ev.Integrity.Verified {
		t.Fatal("integrity should be verified")
	}
	if len(ev.Timeline) != 4 || len(ev.Webhooks) != 1 || len(ev.Policies) != 1 {
		t.Fatalf("histories = timeline:%d webhooks:%d policies:%d", len(ev.Timeline), len(ev.Webhooks), len(ev.Policies))
	}
	if !ev.Compliant {
		t.Fatalf("expected compliant; checks = %+v", ev.Checks)
	}
	// Correlation ids include the event/delivery/policy ids, deterministically sorted.
	if len(ev.CorrelationIDs) == 0 || !sortedContains(ev.CorrelationIDs, "evt_1") {
		t.Fatalf("correlation ids = %v", ev.CorrelationIDs)
	}
}

func TestEvidence_Deterministic(t *testing.T) {
	gov, ver, tl := compliantSetup()
	s := fixed(NewService(gov, ver, tl, nil))
	a, _ := s.Evidence(context.Background(), "c1")
	b, _ := s.Evidence(context.Background(), "c1")
	ja, _ := BuildJSON(a)
	jb, _ := BuildJSON(b)
	if !bytes.Equal(ja, jb) {
		t.Fatal("evidence JSON not deterministic across builds")
	}
}

func TestExports_Reproducible(t *testing.T) {
	gov, ver, tl := compliantSetup()
	s := fixed(NewService(gov, ver, tl, nil))
	ev, _ := s.Evidence(context.Background(), "c1")
	z1, err := BuildZIP(ev)
	if err != nil {
		t.Fatalf("BuildZIP: %v", err)
	}
	z2, _ := BuildZIP(ev)
	if !bytes.Equal(z1, z2) {
		t.Fatal("ZIP export not reproducible")
	}
	if len(z1) == 0 {
		t.Fatal("empty zip")
	}
}

func TestChecks_UsePersistedDataOnly_NonCompliant(t *testing.T) {
	gov, _, _ := compliantSetup()
	// Draft lifecycle, no approver, broken chain, failed policy => several checks fail.
	gov.objs["c1"].Lifecycle = domain.LifecycleDraft
	gov.objs["c1"].RatifiedBy = ""
	brk := int64(2)
	ver := fakeVerifier{res: domain.VerifyResult{OK: false, FirstBreakSeq: &brk, BreakReason: "hash_mismatch"}}
	tl := fakeTimeline{page: timeline.Page{Entries: []timeline.Entry{
		{Component: timeline.CompPolicy, Status: "dead_letter"},
	}}}
	s := fixed(NewService(gov, ver, tl, nil))

	ev, _ := s.Evidence(context.Background(), "c1")
	if ev.Compliant {
		t.Fatal("expected non-compliant")
	}
	byID := map[string]bool{}
	for _, c := range ev.Checks {
		byID[c.ID] = c.Passed
	}
	for _, id := range []string{"audit-chain-verified", "no-failed-integrity-verification", "governance-lifecycle-complete", "required-approvals-present", "policy-executions-completed"} {
		if byID[id] {
			t.Errorf("check %q should have failed", id)
		}
	}
}

func TestSummaryAndChecksAndReports(t *testing.T) {
	gov, ver, tl := compliantSetup()
	gov.list = []*domain.ConfigObject{gov.objs["c1"]}
	s := fixed(NewService(gov, ver, tl, nil))

	sum, err := s.Summary(context.Background(), "c1")
	if err != nil || !sum.Compliant || sum.ChecksPassed != sum.ChecksTotal {
		t.Fatalf("Summary: %+v %v", sum, err)
	}
	checks, err := s.Checks(context.Background(), "c1")
	if err != nil || len(checks) != 6 {
		t.Fatalf("Checks: %d %v", len(checks), err)
	}
	rep, err := s.Reports(context.Background(), "", 10)
	if err != nil || len(rep.Items) != 1 || !rep.Items[0].Compliant {
		t.Fatalf("Reports: %+v %v", rep, err)
	}
}

func TestEvidence_NotFound(t *testing.T) {
	gov, ver, tl := compliantSetup()
	s := NewService(gov, ver, tl, nil)
	if _, err := s.Evidence(context.Background(), "nope"); err == nil {
		t.Fatal("expected not-found error")
	}
}

func TestMetricsRecorded(t *testing.T) {
	gov, ver, tl := compliantSetup()
	m := &countMetrics{}
	s := fixed(NewService(gov, ver, tl, m))
	_, _ = s.Evidence(context.Background(), "c1")
	if m.queries != 1 || m.durations != 1 {
		t.Fatalf("metrics = %+v", m)
	}
}

type countMetrics struct{ queries, exports, durations int }

func (m *countMetrics) IncQuery()                     { m.queries++ }
func (m *countMetrics) IncExport()                    { m.exports++ }
func (m *countMetrics) ObserveDuration(time.Duration) { m.durations++ }

func sortedContains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
