package ops

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/rpsg/oneops/internal/audit"
	"github.com/rpsg/oneops/internal/domain"
)

func TestExecutiveMetrics_RecordAndExport(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewExecutiveMetrics(reg)

	m.SetStartupDuration(2 * time.Second)
	m.SetShutdownDuration(1500 * time.Millisecond)
	m.SetDependencyUp("postgres", true)
	m.IncStartupFailure()
	m.IncShutdownTimeout()
	m.ObserveAuditAppend("ratification", 3*time.Millisecond, true)
	m.ObserveAuditAppend("deletion", 4*time.Millisecond, false)

	if got := testutil.ToFloat64(m.startupDur); got != 2 {
		t.Errorf("startup_duration = %v, want 2", got)
	}
	if got := testutil.ToFloat64(m.shutdownDur); got != 1.5 {
		t.Errorf("shutdown_duration = %v, want 1.5", got)
	}
	if got := testutil.ToFloat64(m.depUp.WithLabelValues("postgres")); got != 1 {
		t.Errorf("dependency_up{postgres} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.startupFail); got != 1 {
		t.Errorf("startup_failures_total = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.shutdownTO); got != 1 {
		t.Errorf("shutdown_timeouts_total = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.govOps.WithLabelValues("ratification", "success")); got != 1 {
		t.Errorf("governance_operations_total{ratification,success} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.govOps.WithLabelValues("deletion", "failure")); got != 1 {
		t.Errorf("governance_operations_total{deletion,failure} = %v, want 1", got)
	}
	if n := testutil.CollectAndCount(reg); n < 7 {
		t.Errorf("collectors exposed = %d, want >= 7", n)
	}
}

// stubAuditor is a governance.Auditor for decorator tests.
type stubAuditor struct {
	calls int
	err   error
	lastX audit.AppendInput
}

func (s *stubAuditor) AppendTx(_ context.Context, _ pgx.Tx, in audit.AppendInput) (domain.AuditEvent, error) {
	s.calls++
	s.lastX = in
	return domain.AuditEvent{EventID: in.EventID}, s.err
}

func TestMeteredAuditor_DelegatesAndRecords(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewExecutiveMetrics(reg)
	inner := &stubAuditor{}
	dec := NewMeteredAuditor(inner, m)

	in := audit.AppendInput{Operation: domain.OpRatification, EventID: "evt_x"}
	ev, err := dec.AppendTx(context.Background(), nil, in)
	if err != nil {
		t.Fatalf("AppendTx: %v", err)
	}
	if ev.EventID != "evt_x" || inner.calls != 1 || inner.lastX.EventID != "evt_x" {
		t.Fatalf("decorator did not delegate unchanged: ev=%+v calls=%d", ev, inner.calls)
	}
	if got := testutil.ToFloat64(m.govOps.WithLabelValues("ratification", "success")); got != 1 {
		t.Errorf("op counter = %v, want 1", got)
	}
}

func TestMeteredAuditor_PropagatesErrorUnchanged(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewExecutiveMetrics(reg)
	sentinel := errors.New("boom")
	dec := NewMeteredAuditor(&stubAuditor{err: sentinel}, m)

	_, err := dec.AppendTx(context.Background(), nil, audit.AppendInput{Operation: domain.OpDeletion})
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want sentinel", err)
	}
	if got := testutil.ToFloat64(m.govOps.WithLabelValues("deletion", "failure")); got != 1 {
		t.Errorf("failure counter = %v, want 1", got)
	}
}

func TestNewMeteredAuditor_NilPassThrough(t *testing.T) {
	inner := &stubAuditor{}
	if got := NewMeteredAuditor(inner, nil); got != inner {
		t.Fatal("nil metrics must pass the inner auditor through unwrapped")
	}
	if got := NewMeteredAuditor(nil, nil); got != nil {
		t.Fatal("nil inner must return nil")
	}
}
