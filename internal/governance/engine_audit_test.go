package governance

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rpsg/oneops/internal/audit"
	"github.com/rpsg/oneops/internal/domain"
)

// spyAuditor records every AppendTx invocation and can inject a failure. It
// satisfies the engine's interface-only, transaction-scoped Auditor port
// (ADR-AUDIT-005). It also captures the transaction it was handed and whether
// that transaction was already committed at call time, so tests can prove the
// audit append runs inside the engine's own transaction, before commit.
type spyAuditor struct {
	calls           int
	lastTx          pgx.Tx
	lastIn          audit.AppendInput
	committedAtCall bool
	err             error
}

func (s *spyAuditor) AppendTx(_ context.Context, tx pgx.Tx, in audit.AppendInput) (domain.AuditEvent, error) {
	s.calls++
	s.lastTx = tx
	s.lastIn = in
	if ft, ok := tx.(*fakeTx); ok {
		s.committedAtCall = ft.committed
	}
	if s.err != nil {
		return domain.AuditEvent{}, s.err
	}
	// Echo a persisted event; the engine discards it.
	return domain.AuditEvent{
		ChainID:          in.ChainID,
		Seq:              1,
		EventID:          in.EventID,
		OperationID:      in.OperationID,
		Operation:        in.Operation,
		Actor:            in.Actor,
		PayloadCanonical: in.PayloadCanonical,
		OccurredAt:       in.OccurredAt,
	}, nil
}

func fixedTime() time.Time { return time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC) }

// ratifyEngine wires a ratification happy-path engine with the given spy and a
// deterministic clock so OccurredAt is assertable. It returns the store too so
// tests can inspect transaction commit/rollback and mutation calls.
func ratifyEngine(t *testing.T, spy *spyAuditor) (*Engine, *mockStore) {
	t.Helper()
	s := &mockStore{
		getObj:  obj(domain.LifecycleDraft, domain.RetentionWorkingMaterial, domain.AuthorityNonNormative),
		applyRV: 2,
	}
	e, err := NewEngine(s, AllowAllAuthorizer{}, spy)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	e.now = fixedTime
	return e, s
}

const (
	ratifyCfgID = "c1"
	ratifyOpID  = "op-xyz"
	ratifyActor = "actor-1"
	ratifyRV    = 2
)

func ratifyCommand() Command {
	return Command{
		Operation:   domain.OpRatification,
		CfgID:       ratifyCfgID,
		Actor:       ratifyActor,
		OperationID: ratifyOpID,
	}
}

// runRatify executes the ratification happy path and returns its spy + result.
func runRatify(t *testing.T) (*spyAuditor, Result) {
	t.Helper()
	spy := &spyAuditor{}
	e, _ := ratifyEngine(t, spy)
	res, err := e.Execute(context.Background(), ratifyCommand())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return spy, res
}

// expectedRatifyAppendInput reconstructs, from the audit contract alone, the
// AppendInput a correct engine must produce for the ratification happy path.
func expectedRatifyAppendInput(t *testing.T, occurredAt time.Time) audit.AppendInput {
	t.Helper()
	res := Result{
		Operation:    domain.OpRatification,
		CfgID:        ratifyCfgID,
		Actor:        ratifyActor,
		OccurredAt:   occurredAt,
		NewLifecycle: domain.LifecycleRatified,
		NewRetention: domain.RetentionCurrentBaseline,
		NewAuthority: domain.AuthorityActive,
		RowVersion:   ratifyRV,
	}
	payload, err := res.auditPayload()
	if err != nil {
		t.Fatalf("auditPayload: %v", err)
	}
	in, err := audit.Resolve(domain.EventInput{
		ChainID:     domain.AuditChainID(ratifyCfgID),
		OperationID: ratifyOpID,
		Operation:   domain.OpRatification,
		Payload:     payload,
	}, ratifyActor, occurredAt)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return in
}

func assertAppendInputEqual(t *testing.T, got, want audit.AppendInput) {
	t.Helper()
	if got.ChainID != want.ChainID {
		t.Errorf("ChainID = %q, want %q", got.ChainID, want.ChainID)
	}
	if got.OperationID != want.OperationID {
		t.Errorf("OperationID = %q, want %q", got.OperationID, want.OperationID)
	}
	if got.EventID != want.EventID {
		t.Errorf("EventID = %q, want %q", got.EventID, want.EventID)
	}
	if got.Operation != want.Operation {
		t.Errorf("Operation = %q, want %q", got.Operation, want.Operation)
	}
	if got.Actor != want.Actor {
		t.Errorf("Actor = %q, want %q", got.Actor, want.Actor)
	}
	if !got.OccurredAt.Equal(want.OccurredAt) {
		t.Errorf("OccurredAt = %v, want %v", got.OccurredAt, want.OccurredAt)
	}
	if !bytes.Equal(got.PayloadCanonical, want.PayloadCanonical) {
		t.Errorf("PayloadCanonical = %q, want %q", got.PayloadCanonical, want.PayloadCanonical)
	}
}

func TestExecute_SuccessEmitsExactlyOneAuditEvent(t *testing.T) {
	spy, _ := runRatify(t)
	if spy.calls != 1 {
		t.Fatalf("audit Append called %d times, want exactly 1", spy.calls)
	}
}

// TestExecute_AppendCalledExactlyOnce and _ResolveCalledExactlyOnce: the engine
// calls Resolve inline and feeds its sole output to a single Append. One Append
// carrying exactly the independently-resolved input therefore proves both that
// Append ran once and that Resolve ran once (Resolve is a pure function, not a
// mockable port, by design).
func TestExecute_AppendAndResolveCalledExactlyOnce(t *testing.T) {
	spy, res := runRatify(t)
	if spy.calls != 1 {
		t.Fatalf("Append calls = %d, want 1", spy.calls)
	}
	assertAppendInputEqual(t, spy.lastIn, expectedRatifyAppendInput(t, res.OccurredAt))
}

func TestExecute_ExactAppendInputContents(t *testing.T) {
	spy, res := runRatify(t)
	assertAppendInputEqual(t, spy.lastIn, expectedRatifyAppendInput(t, res.OccurredAt))
}

func TestExecute_OperationIDPropagated(t *testing.T) {
	spy, _ := runRatify(t)
	if spy.lastIn.OperationID != ratifyOpID {
		t.Fatalf("OperationID = %q, want %q", spy.lastIn.OperationID, ratifyOpID)
	}
}

func TestExecute_ChainIDPropagated(t *testing.T) {
	spy, _ := runRatify(t)
	want := domain.AuditChainID(ratifyCfgID)
	if spy.lastIn.ChainID != want {
		t.Fatalf("ChainID = %q, want %q", spy.lastIn.ChainID, want)
	}
}

func TestExecute_ActorPropagated(t *testing.T) {
	spy, _ := runRatify(t)
	if spy.lastIn.Actor != ratifyActor {
		t.Fatalf("Actor = %q, want %q", spy.lastIn.Actor, ratifyActor)
	}
}

func TestExecute_TimestampPropagated(t *testing.T) {
	spy, res := runRatify(t)
	if !spy.lastIn.OccurredAt.Equal(res.OccurredAt) {
		t.Fatalf("audit OccurredAt = %v, want result's %v", spy.lastIn.OccurredAt, res.OccurredAt)
	}
	if !res.OccurredAt.Equal(fixedTime()) {
		t.Fatalf("result OccurredAt = %v, want engine clock %v", res.OccurredAt, fixedTime())
	}
}

func TestExecute_EventIDDeterministic(t *testing.T) {
	wantEventID, err := audit.DeriveEventID(ratifyOpID)
	if err != nil {
		t.Fatalf("DeriveEventID: %v", err)
	}
	spy1, _ := runRatify(t)
	spy2, _ := runRatify(t)
	if spy1.lastIn.EventID != wantEventID {
		t.Fatalf("EventID = %q, want derived %q", spy1.lastIn.EventID, wantEventID)
	}
	if spy1.lastIn.EventID != spy2.lastIn.EventID {
		t.Fatalf("EventID not deterministic across runs: %q vs %q", spy1.lastIn.EventID, spy2.lastIn.EventID)
	}
}

func TestExecute_CanonicalPayloadUnchanged(t *testing.T) {
	spy, res := runRatify(t)
	// The engine must hand audit exactly the canonicalization of its own committed
	// payload — no re-shaping, no governance-side canonicalization.
	replica := Result{
		Operation:    domain.OpRatification,
		CfgID:        ratifyCfgID,
		Actor:        ratifyActor,
		OccurredAt:   res.OccurredAt,
		NewLifecycle: domain.LifecycleRatified,
		NewRetention: domain.RetentionCurrentBaseline,
		NewAuthority: domain.AuthorityActive,
		RowVersion:   ratifyRV,
	}
	rawPayload, err := replica.auditPayload()
	if err != nil {
		t.Fatalf("auditPayload: %v", err)
	}
	wantCanonical, err := audit.Canonicalize(rawPayload)
	if err != nil {
		t.Fatalf("Canonicalize: %v", err)
	}
	if !bytes.Equal(spy.lastIn.PayloadCanonical, wantCanonical) {
		t.Fatalf("PayloadCanonical = %q, want %q", spy.lastIn.PayloadCanonical, wantCanonical)
	}
}

func TestExecute_InvalidTransitionEmitsNone(t *testing.T) {
	spy := &spyAuditor{}
	s := &mockStore{getObj: obj(domain.LifecycleRatified, domain.RetentionCurrentBaseline, domain.AuthorityActive)}
	e, err := NewEngine(s, AllowAllAuthorizer{}, spy)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	_, err = e.Execute(context.Background(), Command{
		Operation: domain.OpRatification, CfgID: "c1", Actor: "a", OperationID: "op-1",
	})
	var te *TransitionError
	if !errors.As(err, &te) {
		t.Fatalf("expected TransitionError, got %v", err)
	}
	if spy.calls != 0 {
		t.Fatalf("audit emitted %d events on invalid transition, want 0", spy.calls)
	}
}

func TestExecute_AuthorizationFailureEmitsNone(t *testing.T) {
	spy := &spyAuditor{}
	s := &mockStore{getObj: obj(domain.LifecycleDraft, domain.RetentionWorkingMaterial, domain.AuthorityNonNormative)}
	e, err := NewEngine(s, denyAuthorizer{err: errors.New("forbidden")}, spy)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if _, err := e.Execute(context.Background(), Command{
		Operation: domain.OpRatification, CfgID: "c1", Actor: "a", OperationID: "op-1",
	}); err == nil {
		t.Fatal("expected authorization error")
	}
	if spy.calls != 0 {
		t.Fatalf("audit emitted %d events on authorization failure, want 0", spy.calls)
	}
}

func TestExecute_OptimisticConcurrencyEmitsNone(t *testing.T) {
	spy := &spyAuditor{}
	s := &mockStore{getObj: obj(domain.LifecycleDraft, domain.RetentionWorkingMaterial, domain.AuthorityNonNormative)}
	e, err := NewEngine(s, AllowAllAuthorizer{}, spy)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	_, err = e.Execute(context.Background(), Command{
		Operation: domain.OpRatification, CfgID: "c1", Actor: "a", OperationID: "op-1", ExpectedRowVersion: 99,
	})
	if !errors.Is(err, domain.ErrVersionMismatch) {
		t.Fatalf("expected version mismatch, got %v", err)
	}
	if spy.calls != 0 {
		t.Fatalf("audit emitted %d events on concurrency failure, want 0", spy.calls)
	}
}

func TestExecute_StoreFailureEmitsNone(t *testing.T) {
	spy := &spyAuditor{}
	s := &mockStore{
		getObj:   obj(domain.LifecycleDraft, domain.RetentionWorkingMaterial, domain.AuthorityNonNormative),
		applyErr: errors.New("db down"),
	}
	e, err := NewEngine(s, AllowAllAuthorizer{}, spy)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if _, err := e.Execute(context.Background(), Command{
		Operation: domain.OpRatification, CfgID: "c1", Actor: "a", OperationID: "op-1",
	}); err == nil {
		t.Fatal("expected store error")
	}
	if spy.calls != 0 {
		t.Fatalf("audit emitted %d events on pre-commit store failure, want 0", spy.calls)
	}
}

func TestExecute_AppendFailurePropagatesUnchanged(t *testing.T) {
	sentinel := errors.New("audit unavailable")
	spy := &spyAuditor{err: sentinel}
	e, s := ratifyEngine(t, spy)

	_, err := e.Execute(context.Background(), ratifyCommand())
	if err == nil {
		t.Fatal("expected the append error to propagate")
	}
	// Propagated unchanged: the exact sentinel, neither wrapped nor replaced.
	if !errors.Is(err, sentinel) || err != sentinel { //nolint:errorlint // asserting no wrapping
		t.Fatalf("append error was altered: got %v (want exactly %v)", err, sentinel)
	}
	if spy.calls != 1 {
		t.Fatalf("AppendTx calls = %d, want 1", spy.calls)
	}
	// ADR-AUDIT-005: the audit failure rolls back the governance mutation.
	if s.tx.committed {
		t.Fatal("transaction committed despite audit-append failure")
	}
	if !s.tx.rolledBack {
		t.Fatal("transaction was not rolled back after audit-append failure")
	}
}

func TestExecute_ResolveFailureRollsBackMutation(t *testing.T) {
	// A missing OperationID cannot build a valid EventInput; Resolve fails inside
	// the transaction, before commit, and the mutation is rolled back with no
	// Append attempted — no orphaned mutation, no orphaned audit event.
	spy := &spyAuditor{}
	e, s := ratifyEngine(t, spy)
	cmd := ratifyCommand()
	cmd.OperationID = "" // audit.Resolve is the single enforcer of this rule.

	if _, err := e.Execute(context.Background(), cmd); err == nil {
		t.Fatal("expected a Resolve validation error")
	}
	if spy.calls != 0 {
		t.Fatalf("AppendTx attempted %d times after Resolve failure, want 0", spy.calls)
	}
	if s.tx.committed {
		t.Fatal("transaction committed despite Resolve failure")
	}
	if !s.tx.rolledBack {
		t.Fatal("transaction was not rolled back after Resolve failure")
	}
}

func TestExecute_UsesInjectedAuditorInstance(t *testing.T) {
	// The specific injected instance receives the call (DI is honored, not bypassed).
	spy := &spyAuditor{}
	e, _ := ratifyEngine(t, spy)
	if _, err := e.Execute(context.Background(), ratifyCommand()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if spy.calls != 1 {
		t.Fatalf("injected auditor received %d calls, want 1", spy.calls)
	}
}

// TestExecute_MutationAndAuditCommitAtomically proves the ADR-AUDIT-005 success
// path: the audit append runs on the engine's OWN transaction, before commit,
// and the single commit seals both together.
func TestExecute_MutationAndAuditCommitAtomically(t *testing.T) {
	spy := &spyAuditor{}
	e, s := ratifyEngine(t, spy)

	res, err := e.Execute(context.Background(), ratifyCommand())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if spy.calls != 1 {
		t.Fatalf("AppendTx calls = %d, want 1", spy.calls)
	}
	// Same transaction object as the one the store began and the engine mutated.
	if spy.lastTx != s.tx {
		t.Fatalf("audit ran on a different transaction than the mutation")
	}
	// Audit ran BEFORE the commit (inside the transaction).
	if spy.committedAtCall {
		t.Fatal("audit append ran after commit; must be inside the transaction")
	}
	// One atomic commit sealed both.
	if !s.tx.committed {
		t.Fatal("transaction was not committed on the success path")
	}
	if s.applyCalls != 1 {
		t.Fatalf("mutation applied %d times, want 1", s.applyCalls)
	}
	if res.RowVersion != ratifyRV {
		t.Fatalf("result row_version = %d, want %d", res.RowVersion, ratifyRV)
	}
}

// TestExecute_AuditFailureLeavesNoOrphans proves that when audit fails, the
// staged mutation is discarded: no orphaned governance mutation and no orphaned
// audit event can result from a failed operation.
func TestExecute_AuditFailureLeavesNoOrphans(t *testing.T) {
	spy := &spyAuditor{err: errors.New("audit down")}
	e, s := ratifyEngine(t, spy)

	if _, err := e.Execute(context.Background(), ratifyCommand()); err == nil {
		t.Fatal("expected an error")
	}
	// The mutation was staged (attempted) ...
	if s.applyCalls != 1 {
		t.Fatalf("apply calls = %d, want 1", s.applyCalls)
	}
	// ... the audit was attempted in the same transaction ...
	if spy.calls != 1 || spy.lastTx != s.tx {
		t.Fatalf("audit not attempted on the mutation's transaction (calls=%d)", spy.calls)
	}
	// ... and the whole transaction rolled back: no commit, so neither persists.
	if s.tx.committed {
		t.Fatal("no-orphan invariant broken: transaction committed after audit failure")
	}
	if !s.tx.rolledBack {
		t.Fatal("transaction was not rolled back")
	}
}
