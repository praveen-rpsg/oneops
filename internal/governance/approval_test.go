package governance

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/rpsg/oneops/internal/domain"
)

// ADR-GOV-005 — multi-approver approval quorum.

const (
	apprCfg = "ONEOPS-CFG-0300"
	apprA   = "alice@oneops"
	apprB   = "bob@oneops"
)

// fakeApprovalRecorder is an in-memory ApprovalRecorder: a set of distinct
// approvers per call, so a test can drive Execute across several calls and
// observe the quorum accumulate exactly as the real store would.
type fakeApprovalRecorder struct {
	approvers                         map[string]bool
	recordErr, countErr               error
	recordCalls, countCalls           int
	lastTenant, lastGov, lastApprover string
}

func (f *fakeApprovalRecorder) Record(_ context.Context, _ pgx.Tx, tenantID, governanceID, approverUserID string) error {
	f.recordCalls++
	f.lastTenant, f.lastGov, f.lastApprover = tenantID, governanceID, approverUserID
	if f.recordErr != nil {
		return f.recordErr
	}
	if f.approvers == nil {
		f.approvers = make(map[string]bool)
	}
	if f.approvers[approverUserID] {
		return ErrAlreadyApproved
	}
	f.approvers[approverUserID] = true
	return nil
}

func (f *fakeApprovalRecorder) CountDistinct(context.Context, pgx.Tx, string) (int, error) {
	f.countCalls++
	if f.countErr != nil {
		return 0, f.countErr
	}
	return len(f.approvers), nil
}

func approveCmd(actor string, required int) Command {
	return Command{
		Operation: domain.OpApproval, CfgID: apprCfg, Actor: actor,
		TenantID: "tenant-1", RequiredApprovals: required, OperationID: "op-approve-" + actor,
	}
}

// Fail-safe: an engine with no ApprovalRecorder wired refuses Approval
// outright rather than approving on an unenforced quorum.
func TestApproval_RecorderUnavailable_Refuses(t *testing.T) {
	s := &mockStore{getObj: obj(domain.LifecycleInReview, domain.RetentionWorkingMaterial, domain.AuthorityNonNormative)}
	e := newEngine(t, s, AllowAllAuthorizer{})
	// no SetApprovalRecorder call
	if _, err := e.Execute(context.Background(), approveCmd(apprA, 1)); !errors.Is(err, ErrApprovalRecorderUnavailable) {
		t.Fatalf("expected ErrApprovalRecorderUnavailable, got %v", err)
	}
	if s.applyCalls != 0 || s.tx.committed {
		t.Fatal("mutated/committed despite unavailable recorder")
	}
}

// Backward compatibility: RequiredApprovals unset (0) defaults to 1, which
// reproduces single-actor approval exactly — the pre-quorum behaviour.
func TestApproval_DefaultRequiredApprovals_SingleActorApproves(t *testing.T) {
	s := &mockStore{getObj: obj(domain.LifecycleInReview, domain.RetentionWorkingMaterial, domain.AuthorityNonNormative), applyRV: 2}
	e := newEngine(t, s, AllowAllAuthorizer{})
	rec := &fakeApprovalRecorder{}
	e.SetApprovalRecorder(rec)

	cmd := approveCmd(apprA, 0) // RequiredApprovals unset
	res, err := e.Execute(context.Background(), cmd)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.NewLifecycle != domain.LifecycleApproved || res.NewAuthority != domain.AuthorityActive {
		t.Fatalf("result = %+v, want Approved/Active", res)
	}
	if res.ApprovalCount != 1 || res.RequiredApprovals != 1 {
		t.Fatalf("approval status = %d of %d, want 1 of 1", res.ApprovalCount, res.RequiredApprovals)
	}
	if !s.tx.committed {
		t.Fatal("not committed")
	}
	if rec.lastTenant != "tenant-1" || rec.lastGov != apprCfg || rec.lastApprover != apprA {
		t.Fatalf("recorded (%q,%q,%q), want (tenant-1,%q,%q)", rec.lastTenant, rec.lastGov, rec.lastApprover, apprCfg, apprA)
	}
}

// The quorum gate itself: with required=2, one distinct approver leaves the
// object exactly where it was — recorded, not approved. This is the test
// that must bite: a bypass would show Approved after only one approval.
func TestApproval_BelowQuorum_RecordsWithoutTransitioning(t *testing.T) {
	base := obj(domain.LifecycleInReview, domain.RetentionWorkingMaterial, domain.AuthorityNonNormative)
	s := &mockStore{getObj: base, applyRV: 2}
	e := newEngine(t, s, AllowAllAuthorizer{})
	e.SetApprovalRecorder(&fakeApprovalRecorder{})

	res, err := e.Execute(context.Background(), approveCmd(apprA, 2))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.NewLifecycle != domain.LifecycleInReview {
		t.Fatalf("NewLifecycle = %q, want unchanged %q (FAIL-SAFE: must not reach Approved below quorum)",
			res.NewLifecycle, domain.LifecycleInReview)
	}
	if res.NewAuthority != domain.AuthorityNonNormative || res.NewRetention != domain.RetentionWorkingMaterial {
		t.Fatalf("dimensions moved below quorum: authority=%q retention=%q", res.NewAuthority, res.NewRetention)
	}
	if res.ApprovalCount != 1 || res.RequiredApprovals != 2 {
		t.Fatalf("approval status = %d of %d, want 1 of 2", res.ApprovalCount, res.RequiredApprovals)
	}
	if !s.tx.committed {
		t.Fatal("the approval itself must still commit even though quorum is not met")
	}
}

// The other half of the gate: a SECOND distinct approver reaching the
// threshold DOES transition the object. Exercised as two real Execute calls
// against the same recorder, so the count is genuinely accumulated rather
// than asserted directly.
func TestApproval_QuorumMet_Transitions(t *testing.T) {
	rec := &fakeApprovalRecorder{}

	s1 := &mockStore{getObj: obj(domain.LifecycleInReview, domain.RetentionWorkingMaterial, domain.AuthorityNonNormative), applyRV: 2}
	e1 := newEngine(t, s1, AllowAllAuthorizer{})
	e1.SetApprovalRecorder(rec)
	if _, err := e1.Execute(context.Background(), approveCmd(apprA, 2)); err != nil {
		t.Fatalf("first approval: %v", err)
	}

	s2 := &mockStore{getObj: obj(domain.LifecycleInReview, domain.RetentionWorkingMaterial, domain.AuthorityNonNormative), applyRV: 3}
	e2 := newEngine(t, s2, AllowAllAuthorizer{})
	e2.SetApprovalRecorder(rec)
	res, err := e2.Execute(context.Background(), approveCmd(apprB, 2))
	if err != nil {
		t.Fatalf("second approval: %v", err)
	}
	if res.NewLifecycle != domain.LifecycleApproved || res.NewAuthority != domain.AuthorityActive {
		t.Fatalf("result after quorum met = %+v, want Approved/Active", res)
	}
	if res.ApprovalCount != 2 || res.RequiredApprovals != 2 {
		t.Fatalf("approval status = %d of %d, want 2 of 2", res.ApprovalCount, res.RequiredApprovals)
	}
}

// The distinctness half of the gate: the SAME approver calling twice must
// not be able to satisfy a quorum of 2 alone. A bypass here would show
// Approved after one actor approved twice.
func TestApproval_DuplicateApprover_Rejected(t *testing.T) {
	base := obj(domain.LifecycleInReview, domain.RetentionWorkingMaterial, domain.AuthorityNonNormative)
	s := &mockStore{getObj: base, applyRV: 2}
	e := newEngine(t, s, AllowAllAuthorizer{})
	rec := &fakeApprovalRecorder{}
	e.SetApprovalRecorder(rec)

	if _, err := e.Execute(context.Background(), approveCmd(apprA, 2)); err != nil {
		t.Fatalf("first approval: %v", err)
	}

	s2 := &mockStore{getObj: base, applyRV: 3}
	e2 := newEngine(t, s2, AllowAllAuthorizer{})
	e2.SetApprovalRecorder(rec)
	_, err := e2.Execute(context.Background(), approveCmd(apprA, 2))
	if !errors.Is(err, ErrAlreadyApproved) {
		t.Fatalf("expected ErrAlreadyApproved, got %v", err)
	}
	if s2.applyCalls != 0 || s2.tx.committed {
		t.Fatal("mutated/committed on a duplicate approver")
	}
	if rec.countCalls != 1 {
		t.Fatalf("CountDistinct called %d times, want 1 (only after the first, successful, Record)", rec.countCalls)
	}
}

// A recorder error before the count is known must roll back exactly like any
// other store failure.
func TestApproval_CountError_RollsBack(t *testing.T) {
	s := &mockStore{getObj: obj(domain.LifecycleInReview, domain.RetentionWorkingMaterial, domain.AuthorityNonNormative)}
	e := newEngine(t, s, AllowAllAuthorizer{})
	e.SetApprovalRecorder(&fakeApprovalRecorder{countErr: errors.New("db down")})

	if _, err := e.Execute(context.Background(), approveCmd(apprA, 1)); err == nil {
		t.Fatal("expected count error to propagate")
	}
	if s.applyCalls != 0 || s.tx.committed {
		t.Fatal("mutated/committed despite count error")
	}
}
