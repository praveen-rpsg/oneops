package policy

import (
	"context"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// EventSource is the read-only committed audit log the consumer tails. It is
// satisfied by *postgres.AuditStore (ListChainIDs + ListEvents) — the policy
// consumer adds no new read path and never touches the write/verify path.
type EventSource interface {
	ListChainIDs(ctx context.Context) ([]string, error)
	ListEvents(ctx context.Context, chainID string, cursor int64, desc bool, limit int, operation string) ([]domain.AuditEvent, error)
}

// CursorStore persists the consumer's own per-chain progress (separate from the
// webhook relay cursor; never the audit schema).
type CursorStore interface {
	GetPolicyCursor(ctx context.Context, chainID string) (int64, error)
	SetPolicyCursor(ctx context.Context, chainID string, seq int64) error
}

// Store persists the policy registry.
type Store interface {
	Create(ctx context.Context, p Policy) error
	Get(ctx context.Context, id string) (Policy, error)
	List(ctx context.Context) ([]Policy, error)
	ListEnabled(ctx context.Context) ([]Policy, error)
	Update(ctx context.Context, p Policy) error
	Delete(ctx context.Context, id string) error
}

// ExecutionStore persists policy executions and their status transitions.
type ExecutionStore interface {
	Enqueue(ctx context.Context, execs []Execution) error
	ClaimDue(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]Execution, error)
	MarkResult(ctx context.Context, id string, status ExecutionStatus, retry int, errMsg string, started, ended, next time.Time) error
	ListByPolicy(ctx context.Context, policyID string, limit int) ([]Execution, error)
}
