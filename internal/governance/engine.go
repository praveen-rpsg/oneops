package governance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rpsg/oneops/internal/audit"
	"github.com/rpsg/oneops/internal/domain"
)

// Engine errors.
var (
	// ErrUnsupportedOperation is returned for a §8 operation not yet implemented
	// by this engine (Extension, Replacement, Amendment, Baseline Freeze,
	// Historical Preservation).
	ErrUnsupportedOperation = errors.New("governance: operation not supported")
	// ErrHasDependents is returned when Deletion is attempted on an object that
	// still has dependents (State Model §8: deletion requires no dependents).
	ErrHasDependents = errors.New("governance: object has dependents")
)

// Command is one constitutional configuration operation request (State Model §8).
type Command struct {
	// Operation is the §8 operation to perform.
	Operation domain.ConfigurationOperation
	// CfgID is the target Configuration Object.
	CfgID string
	// Actor is the authenticated identity performing the operation.
	Actor string
	// OperationID is the idempotency key of this operation, supplied by the
	// request boundary (PRS-009A). It is carried verbatim into the audit event
	// and drives the deterministic EventID, so a retried operation is audited at
	// most once. Its presence is enforced by audit.Resolve (the single owner of
	// that rule); the engine does not re-validate it.
	OperationID string
	// ExpectedRowVersion, when non-zero, enforces optimistic concurrency.
	ExpectedRowVersion int64
	// TargetRetention is the archival retention class for Archiving.
	TargetRetention domain.RetentionClass
}

func (c Command) validate() error {
	if c.CfgID == "" {
		return errors.New("governance: cfg_id is required")
	}
	if c.Actor == "" {
		return errors.New("governance: actor is required")
	}
	if !c.Operation.Valid() {
		return fmt.Errorf("governance: unknown operation %q", c.Operation)
	}
	return nil
}

// Result is the authoritative outcome of a successful operation. It is produced
// once the mutation is staged; the engine emits exactly one audit event from it
// within the same transaction, and mutation and audit event commit atomically
// (ADR-AUDIT-005).
type Result struct {
	Operation    domain.ConfigurationOperation
	CfgID        string
	Actor        string
	OccurredAt   time.Time
	NewLifecycle domain.Lifecycle
	NewRetention domain.RetentionClass
	NewAuthority domain.Authority
	Removed      bool
	RowVersion   int64
}

// auditPayload is the governance-owned JSON descriptor of a committed result:
// the semantic content recorded in its audit event. It is deliberately raw
// (non-canonical) JSON — the audit subsystem canonicalizes it inside
// audit.Resolve, so governance never performs canonicalization. Marshalling a
// fixed struct of committed fields is deterministic for a given Result.
func (r Result) auditPayload() (json.RawMessage, error) {
	return json.Marshal(struct {
		Operation    domain.ConfigurationOperation `json:"operation"`
		CfgID        string                        `json:"cfg_id"`
		Removed      bool                          `json:"removed"`
		NewLifecycle domain.Lifecycle              `json:"new_lifecycle,omitempty"`
		NewRetention domain.RetentionClass         `json:"new_retention,omitempty"`
		NewAuthority domain.Authority              `json:"new_authority,omitempty"`
		RowVersion   int64                         `json:"row_version"`
	}{
		Operation:    r.Operation,
		CfgID:        r.CfgID,
		Removed:      r.Removed,
		NewLifecycle: r.NewLifecycle,
		NewRetention: r.NewRetention,
		NewAuthority: r.NewAuthority,
		RowVersion:   r.RowVersion,
	})
}

// Store is the transaction-scoped persistence port. The engine owns the
// transaction and passes it in; the store never begins or commits.
type Store interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	GetForUpdate(ctx context.Context, tx pgx.Tx, cfgID string) (*domain.ConfigObject, error)
	ApplyDimensions(ctx context.Context, tx pgx.Tx, cfgID string, expected int64, lifecycle domain.Lifecycle, retention domain.RetentionClass, authority domain.Authority) (rowVersion int64, err error)
	CountDependents(ctx context.Context, tx pgx.Tx, cfgID string) (int, error)
	RemoveObject(ctx context.Context, tx pgx.Tx, cfgID string, expected int64) error
}

// Authorizer decides whether an actor may perform an operation on an object
// (State Model §8 "Authority (who)"). It is a hook; a permissive default is
// provided for wiring.
type Authorizer interface {
	Authorize(ctx context.Context, op domain.ConfigurationOperation, actor string, obj *domain.ConfigObject) error
}

// AllowAllAuthorizer permits every operation. It is the default hook until a
// policy-backed authorizer is wired.
type AllowAllAuthorizer struct{}

// Authorize always allows.
func (AllowAllAuthorizer) Authorize(context.Context, domain.ConfigurationOperation, string, *domain.ConfigObject) error {
	return nil
}

// Auditor is the audit-emission port: it appends exactly one sealed audit event
// for an operation, using the engine's OWN transaction so the mutation and its
// audit record commit atomically (ADR-AUDIT-005). The transaction-scoped
// *postgres.AuditAppender satisfies it. Declaring the port here — rather than
// importing a concrete type — keeps the engine's dependencies interface-only,
// matching Store and Authorizer.
type Auditor interface {
	AppendTx(ctx context.Context, tx pgx.Tx, in audit.AppendInput) (domain.AuditEvent, error)
}

// Engine executes constitutional configuration operations, owning exactly one
// transaction per operation. It depends only on the Store and Authorizer ports.
type Engine struct {
	store      Store
	authorizer Authorizer
	audit      Auditor
	now        func() time.Time
}

// NewEngine composes the engine. store, authorizer, and auditor are all required.
func NewEngine(store Store, authorizer Authorizer, auditor Auditor) (*Engine, error) {
	if store == nil {
		return nil, errors.New("governance: engine requires a non-nil Store")
	}
	if authorizer == nil {
		return nil, errors.New("governance: engine requires a non-nil Authorizer")
	}
	if auditor == nil {
		return nil, errors.New("governance: engine requires a non-nil Auditor")
	}
	return &Engine{store: store, authorizer: authorizer, audit: auditor, now: func() time.Time { return time.Now().UTC() }}, nil
}

// Execute performs one constitutional operation atomically: load-for-update →
// optimistic-concurrency check → authorize → plan the §8 transition → apply →
// commit. Any failure rolls the single transaction back, so a failed operation
// mutates nothing. On success it returns the Result at the one completion point.
func (e *Engine) Execute(ctx context.Context, cmd Command) (Result, error) {
	if err := cmd.validate(); err != nil {
		return Result{}, err
	}

	tx, err := e.store.Begin(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("governance: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful commit

	obj, err := e.store.GetForUpdate(ctx, tx, cmd.CfgID)
	if err != nil {
		return Result{}, err // domain.ErrNotFound propagates unchanged
	}
	if cmd.ExpectedRowVersion != 0 && obj.RowVersion != cmd.ExpectedRowVersion {
		return Result{}, domain.ErrVersionMismatch
	}
	if err := e.authorizer.Authorize(ctx, cmd.Operation, cmd.Actor, obj); err != nil {
		return Result{}, err
	}

	p, err := planTransition(cmd.Operation, obj, cmd)
	if err != nil {
		return Result{}, err
	}

	res := Result{Operation: cmd.Operation, CfgID: cmd.CfgID, Actor: cmd.Actor, OccurredAt: e.now()}
	if p.Remove {
		n, err := e.store.CountDependents(ctx, tx, cmd.CfgID)
		if err != nil {
			return Result{}, err
		}
		if n > 0 {
			return Result{}, ErrHasDependents
		}
		if err := e.store.RemoveObject(ctx, tx, cmd.CfgID, obj.RowVersion); err != nil {
			return Result{}, err
		}
		res.Removed = true
	} else {
		// Enforce cross-dimension invariants (§9.3) on the resulting object.
		next := *obj
		next.Lifecycle, next.RetentionClass, next.Authority = p.Lifecycle, p.Retention, p.Authority
		if err := next.Validate(); err != nil {
			return Result{}, err
		}
		rv, err := e.store.ApplyDimensions(ctx, tx, cmd.CfgID, obj.RowVersion, p.Lifecycle, p.Retention, p.Authority)
		if err != nil {
			return Result{}, err
		}
		res.NewLifecycle, res.NewRetention, res.NewAuthority, res.RowVersion = p.Lifecycle, p.Retention, p.Authority, rv
	}

	// --- ATOMIC AUDIT EMISSION (ADR-AUDIT-005) --------------------------------
	// The mutation is staged in this transaction but NOT yet committed. Build and
	// append exactly one audit event within the SAME transaction, then commit both
	// atomically below. Any failure here (Resolve or AppendTx) returns before the
	// commit, so the deferred Rollback undoes the mutation: a governance mutation
	// can never exist without its audit event, nor an audit event without its
	// mutation. Every pre-commit failure above likewise emits zero audit events.
	payload, err := res.auditPayload()
	if err != nil {
		return Result{}, err
	}
	in, err := audit.Resolve(domain.EventInput{
		ChainID:     domain.AuditChainID(res.CfgID),
		OperationID: cmd.OperationID,
		Operation:   res.Operation,
		Payload:     payload,
	}, res.Actor, res.OccurredAt)
	if err != nil {
		return Result{}, err
	}
	if _, err := e.audit.AppendTx(ctx, tx, in); err != nil {
		return Result{}, err // propagate unchanged; the deferred Rollback undoes the mutation
	}

	// --- SINGLE ATOMIC COMMIT POINT -------------------------------------------
	// One commit seals the governance mutation and its audit event together.
	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("governance: commit: %w", err)
	}
	return res, nil
}
