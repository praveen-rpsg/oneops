package governance

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

// M4 WP-2 — the §8 Extension operation.
//
// The guarantee under test is the one whose absence produced the CVP error:
// an Extension records that a successor extends a base and changes NOTHING
// about the base. Most importantly it never demotes the base to Historical —
// that is Replacement's effect, and Replacement is a different operation gated
// by a four-part test.

const (
	extBase      = "ONEOPS-CFG-0001"
	extSuccessor = "ONEOPS-CFG-0002"
	extActor     = "architect@oneops"
)

func extendCmd(base, successor string) Command {
	return Command{
		Operation:   domain.OpExtension,
		CfgID:       base,
		SuccessorID: successor,
		Actor:       extActor,
		OperationID: "op-extend-1",
	}
}

// TestExtensionLeavesBaseActive is the anti-CVP regression test. An Active base
// that is extended stays Active — in every dimension.
func TestExtensionLeavesBaseActive(t *testing.T) {
	base := obj(domain.LifecycleRatified, domain.RetentionCurrentBaseline, domain.AuthorityActive)
	s := &mockStore{getObj: base, applyRV: 7}
	e := newEngine(t, s, AllowAllAuthorizer{})

	res, err := e.Execute(context.Background(), extendCmd(extBase, extSuccessor))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.NewAuthority != domain.AuthorityActive {
		t.Errorf("Authority = %q, want %q — Extension must never demote its base",
			res.NewAuthority, domain.AuthorityActive)
	}
	if res.NewLifecycle != base.Lifecycle {
		t.Errorf("Lifecycle = %q, want %q (unchanged)", res.NewLifecycle, base.Lifecycle)
	}
	if res.NewRetention != base.RetentionClass {
		t.Errorf("Retention = %q, want %q (unchanged)", res.NewRetention, base.RetentionClass)
	}
	if res.SuccessorID != extSuccessor {
		t.Errorf("SuccessorID = %q, want %q", res.SuccessorID, extSuccessor)
	}
}

// TestExtensionNeverProducesHistorical proves the CVP error is structurally
// impossible from every starting authority: Extension is dimension-preserving,
// so it can never yield Historical unless the base already was.
func TestExtensionNeverProducesHistorical(t *testing.T) {
	for _, au := range []domain.Authority{
		domain.AuthorityActive, domain.AuthorityNonNormative, domain.AuthorityHistorical,
	} {
		base := obj(domain.LifecycleRatified, domain.RetentionCurrentBaseline, au)
		p, err := planTransition(domain.OpExtension, base, extendCmd(extBase, extSuccessor))
		if err != nil {
			t.Fatalf("%s: planTransition: %v", au, err)
		}
		if p.Authority != au {
			t.Errorf("from %s: Authority = %q, want unchanged %q", au, p.Authority, au)
		}
	}
}

// TestExtensionRecordsExtendsEdge asserts the operation's actual constitutional
// effect: an `extends` edge from successor to base (§8 "base Extended By +=
// successor"), in the engine's own transaction.
func TestExtensionRecordsExtendsEdge(t *testing.T) {
	s := &mockStore{
		getObj:  obj(domain.LifecycleRatified, domain.RetentionCurrentBaseline, domain.AuthorityActive),
		applyRV: 2,
	}
	e := newEngine(t, s, AllowAllAuthorizer{})

	if _, err := e.Execute(context.Background(), extendCmd(extBase, extSuccessor)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if s.edgeCalls != 1 {
		t.Fatalf("RecordEdge called %d times, want exactly 1", s.edgeCalls)
	}
	if s.lastEdge.Kind != domain.EdgeKindExtends {
		t.Errorf("edge kind = %q, want %q", s.lastEdge.Kind, domain.EdgeKindExtends)
	}
	// Direction matters: the SOURCE extends the TARGET (domain.EdgeKindExtends).
	if s.lastEdge.From != extSuccessor || s.lastEdge.To != extBase {
		t.Errorf("edge = %s -> %s, want %s -> %s",
			s.lastEdge.From, s.lastEdge.To, extSuccessor, extBase)
	}
	if !s.tx.committed {
		t.Error("transaction was not committed")
	}
}

// TestExtensionIsNotSupersedes guards the distinction directly: Extension must
// never write a supersedes edge.
func TestExtensionIsNotSupersedes(t *testing.T) {
	s := &mockStore{
		getObj:  obj(domain.LifecycleRatified, domain.RetentionCurrentBaseline, domain.AuthorityActive),
		applyRV: 2,
	}
	e := newEngine(t, s, AllowAllAuthorizer{})
	if _, err := e.Execute(context.Background(), extendCmd(extBase, extSuccessor)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if s.lastEdge.Kind == domain.EdgeKindSupersedes {
		t.Fatal("Extension wrote a supersedes edge — that is Replacement")
	}
}

func TestExtensionPreconditions(t *testing.T) {
	base := obj(domain.LifecycleRatified, domain.RetentionCurrentBaseline, domain.AuthorityActive)
	tests := []struct {
		name string
		cmd  Command
	}{
		{"successor required", extendCmd(extBase, "")},
		{"self-extension rejected", extendCmd(extBase, extBase)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var te *TransitionError
			if _, err := planTransition(domain.OpExtension, base, tt.cmd); !errors.As(err, &te) {
				t.Fatalf("expected *TransitionError, got %v", err)
			}
		})
	}
}

// TestExtensionEdgeFailureRollsBack proves the edge participates in the atomic
// guarantee (ADR-AUDIT-005): if the edge cannot be written, nothing commits and
// no audit event is emitted.
func TestExtensionEdgeFailureRollsBack(t *testing.T) {
	sentinel := errors.New("edge write failed")
	spy := &spyAuditor{}
	s := &mockStore{
		getObj:  obj(domain.LifecycleRatified, domain.RetentionCurrentBaseline, domain.AuthorityActive),
		applyRV: 2,
		edgeErr: sentinel,
	}
	e, err := NewEngine(s, AllowAllAuthorizer{}, spy)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	if _, err := e.Execute(context.Background(), extendCmd(extBase, extSuccessor)); !errors.Is(err, sentinel) {
		t.Fatalf("Execute error = %v, want %v", err, sentinel)
	}
	if spy.calls != 0 {
		t.Errorf("audit appended %d events, want 0 — a failed edge must emit none", spy.calls)
	}
	if s.tx.committed {
		t.Error("transaction committed despite the edge failure")
	}
	if !s.tx.rolledBack {
		t.Error("transaction was not rolled back")
	}
}

// TestExtensionDuplicateIsConflict: the schema's uniqueness constraint surfaces a
// repeated extension as domain.ErrConflict, unchanged by the engine.
func TestExtensionDuplicateIsConflict(t *testing.T) {
	s := &mockStore{
		getObj:  obj(domain.LifecycleRatified, domain.RetentionCurrentBaseline, domain.AuthorityActive),
		applyRV: 2,
		edgeErr: domain.ErrConflict,
	}
	e := newEngine(t, s, AllowAllAuthorizer{})
	if _, err := e.Execute(context.Background(), extendCmd(extBase, extSuccessor)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("Execute error = %v, want domain.ErrConflict", err)
	}
}

// TestExtensionAuditPayloadCarriesSuccessor: the audit record must say WHAT
// extended the base, or the event is not reconstructable.
func TestExtensionAuditPayloadCarriesSuccessor(t *testing.T) {
	res := Result{Operation: domain.OpExtension, CfgID: extBase, SuccessorID: extSuccessor, RowVersion: 2}
	payload, err := res.auditPayload()
	if err != nil {
		t.Fatalf("auditPayload: %v", err)
	}
	if !strings.Contains(string(payload), `"successor_id":"`+extSuccessor+`"`) {
		t.Errorf("payload %s does not carry successor_id", payload)
	}
}

// TestNonExtensionPayloadOmitsSuccessor pins the backward-compatibility
// guarantee: every pre-existing operation marshals exactly as before, so the
// hash chain over already-committed events is untouched (Law 12).
func TestNonExtensionPayloadOmitsSuccessor(t *testing.T) {
	res := Result{
		Operation:    domain.OpRatification,
		CfgID:        extBase,
		NewLifecycle: domain.LifecycleRatified,
		NewRetention: domain.RetentionCurrentBaseline,
		NewAuthority: domain.AuthorityActive,
		RowVersion:   1,
	}
	payload, err := res.auditPayload()
	if err != nil {
		t.Fatalf("auditPayload: %v", err)
	}
	if strings.Contains(string(payload), "successor_id") {
		t.Errorf("payload %s must not contain successor_id for a non-extension operation", payload)
	}
}
