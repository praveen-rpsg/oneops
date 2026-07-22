package governance

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/rpsg/oneops/internal/domain"
)

// fakeTx satisfies pgx.Tx; only Commit/Rollback are exercised by the engine.
type fakeTx struct {
	pgx.Tx
	committed  bool
	rolledBack bool
}

func (f *fakeTx) Commit(context.Context) error   { f.committed = true; return nil }
func (f *fakeTx) Rollback(context.Context) error { f.rolledBack = true; return nil }

type mockStore struct {
	tx          *fakeTx
	getObj      *domain.ConfigObject
	getErr      error
	applyRV     int64
	applyErr    error
	applyCalls  int
	depCount    int
	removeErr   error
	removeCalls int
}

func (m *mockStore) Begin(context.Context) (pgx.Tx, error) {
	m.tx = &fakeTx{}
	return m.tx, nil
}
func (m *mockStore) GetForUpdate(context.Context, pgx.Tx, string) (*domain.ConfigObject, error) {
	return m.getObj, m.getErr
}
func (m *mockStore) ApplyDimensions(_ context.Context, _ pgx.Tx, _ string, _ int64, _ domain.Lifecycle, _ domain.RetentionClass, _ domain.Authority) (int64, error) {
	m.applyCalls++
	return m.applyRV, m.applyErr
}
func (m *mockStore) CountDependents(context.Context, pgx.Tx, string) (int, error) {
	return m.depCount, nil
}
func (m *mockStore) RemoveObject(context.Context, pgx.Tx, string, int64) error {
	m.removeCalls++
	return m.removeErr
}

type denyAuthorizer struct{ err error }

func (d denyAuthorizer) Authorize(context.Context, domain.ConfigurationOperation, string, *domain.ConfigObject) error {
	return d.err
}

// newEngine composes an engine with a discard audit spy for tests that do not
// assert on audit behaviour. Audit-focused tests use newEngineWithAudit.
func newEngine(t *testing.T, s Store, a Authorizer) *Engine {
	t.Helper()
	e, err := NewEngine(s, a, &spyAuditor{})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return e
}

func TestNewEngineNilDependencies(t *testing.T) {
	if _, err := NewEngine(nil, AllowAllAuthorizer{}, &spyAuditor{}); err == nil {
		t.Error("expected error for nil store")
	}
	if _, err := NewEngine(&mockStore{}, nil, &spyAuditor{}); err == nil {
		t.Error("expected error for nil authorizer")
	}
	if _, err := NewEngine(&mockStore{}, AllowAllAuthorizer{}, nil); err == nil {
		t.Error("expected error for nil auditor")
	}
}

func TestExecuteCommandValidation(t *testing.T) {
	e := newEngine(t, &mockStore{}, AllowAllAuthorizer{})
	for _, c := range []Command{
		{Operation: domain.OpRatification, Actor: "a"},                             // missing cfg
		{Operation: domain.OpRatification, CfgID: "c"},                             // missing actor
		{Operation: domain.ConfigurationOperation("nope"), CfgID: "c", Actor: "a"}, // bad op
	} {
		if _, err := e.Execute(context.Background(), c); err == nil {
			t.Errorf("expected validation error for %+v", c)
		}
	}
}

func TestExecuteRatifyHappyPath(t *testing.T) {
	s := &mockStore{getObj: obj(domain.LifecycleDraft, domain.RetentionWorkingMaterial, domain.AuthorityNonNormative), applyRV: 2}
	e := newEngine(t, s, AllowAllAuthorizer{})
	res, err := e.Execute(context.Background(), Command{Operation: domain.OpRatification, CfgID: "c1", Actor: "actor-1", OperationID: "op-1"})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.NewLifecycle != domain.LifecycleRatified || res.NewRetention != domain.RetentionCurrentBaseline ||
		res.NewAuthority != domain.AuthorityActive || res.RowVersion != 2 || res.Actor != "actor-1" {
		t.Fatalf("result = %+v", res)
	}
	if s.applyCalls != 1 || !s.tx.committed {
		t.Fatalf("apply=%d committed=%v", s.applyCalls, s.tx.committed)
	}
}

func TestExecuteAuthorizationDeniedRollsBack(t *testing.T) {
	s := &mockStore{getObj: obj(domain.LifecycleDraft, domain.RetentionWorkingMaterial, domain.AuthorityNonNormative)}
	e := newEngine(t, s, denyAuthorizer{err: errors.New("forbidden")})
	if _, err := e.Execute(context.Background(), Command{Operation: domain.OpRatification, CfgID: "c1", Actor: "a"}); err == nil {
		t.Fatal("expected authorization error")
	}
	if s.applyCalls != 0 || s.tx.committed {
		t.Fatalf("mutation happened despite denial: apply=%d committed=%v", s.applyCalls, s.tx.committed)
	}
}

func TestExecuteOptimisticConcurrency(t *testing.T) {
	s := &mockStore{getObj: obj(domain.LifecycleDraft, domain.RetentionWorkingMaterial, domain.AuthorityNonNormative)} // RowVersion 1
	e := newEngine(t, s, AllowAllAuthorizer{})
	_, err := e.Execute(context.Background(), Command{Operation: domain.OpRatification, CfgID: "c1", Actor: "a", ExpectedRowVersion: 99})
	if !errors.Is(err, domain.ErrVersionMismatch) {
		t.Fatalf("expected version mismatch, got %v", err)
	}
	if s.applyCalls != 0 || s.tx.committed {
		t.Fatal("mutation happened on stale version")
	}
}

func TestExecuteNotFoundPropagates(t *testing.T) {
	s := &mockStore{getErr: domain.ErrNotFound}
	e := newEngine(t, s, AllowAllAuthorizer{})
	if _, err := e.Execute(context.Background(), Command{Operation: domain.OpRatification, CfgID: "x", Actor: "a"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestExecuteStoreErrorRollsBack(t *testing.T) {
	s := &mockStore{getObj: obj(domain.LifecycleDraft, domain.RetentionWorkingMaterial, domain.AuthorityNonNormative), applyErr: errors.New("db down")}
	e := newEngine(t, s, AllowAllAuthorizer{})
	if _, err := e.Execute(context.Background(), Command{Operation: domain.OpRatification, CfgID: "c1", Actor: "a"}); err == nil {
		t.Fatal("expected store error")
	}
	if s.tx.committed {
		t.Fatal("committed despite store error (no rollback)")
	}
}

func TestExecuteDeleteHappyAndDependents(t *testing.T) {
	// happy: working material, no dependents
	s := &mockStore{getObj: obj(domain.LifecycleDraft, domain.RetentionWorkingMaterial, domain.AuthorityNonNormative), depCount: 0}
	e := newEngine(t, s, AllowAllAuthorizer{})
	res, err := e.Execute(context.Background(), Command{Operation: domain.OpDeletion, CfgID: "c1", Actor: "a", OperationID: "op-del-1"})
	if err != nil || !res.Removed || s.removeCalls != 1 || !s.tx.committed {
		t.Fatalf("delete: res=%+v err=%v removeCalls=%d", res, err, s.removeCalls)
	}
	// with dependents → refused, no removal, no commit
	s2 := &mockStore{getObj: obj(domain.LifecycleDraft, domain.RetentionWorkingMaterial, domain.AuthorityNonNormative), depCount: 1}
	e2 := newEngine(t, s2, AllowAllAuthorizer{})
	if _, err := e2.Execute(context.Background(), Command{Operation: domain.OpDeletion, CfgID: "c1", Actor: "a"}); !errors.Is(err, ErrHasDependents) {
		t.Fatalf("expected ErrHasDependents, got %v", err)
	}
	if s2.removeCalls != 0 || s2.tx.committed {
		t.Fatal("removed/committed despite dependents")
	}
}

func TestExecuteUnsupportedOperation(t *testing.T) {
	s := &mockStore{getObj: obj(domain.LifecycleRatified, domain.RetentionCurrentBaseline, domain.AuthorityActive)}
	e := newEngine(t, s, AllowAllAuthorizer{})
	if _, err := e.Execute(context.Background(), Command{Operation: domain.OpReplacement, CfgID: "c1", Actor: "a"}); !errors.Is(err, ErrUnsupportedOperation) {
		t.Fatalf("expected ErrUnsupportedOperation, got %v", err)
	}
	if s.tx.committed {
		t.Fatal("committed an unsupported operation")
	}
}

func TestExecuteInvalidTransition(t *testing.T) {
	s := &mockStore{getObj: obj(domain.LifecycleRatified, domain.RetentionCurrentBaseline, domain.AuthorityActive)}
	e := newEngine(t, s, AllowAllAuthorizer{})
	_, err := e.Execute(context.Background(), Command{Operation: domain.OpRatification, CfgID: "c1", Actor: "a"})
	var te *TransitionError
	if !errors.As(err, &te) {
		t.Fatalf("expected TransitionError, got %v", err)
	}
	if s.applyCalls != 0 || s.tx.committed {
		t.Fatal("mutated on invalid transition")
	}
}
