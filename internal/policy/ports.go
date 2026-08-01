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
	MarkResult(ctx context.Context, id string, claimToken time.Time, status ExecutionStatus, retry int, errMsg string, started, ended, next time.Time) error
	// ReleaseClaim returns an unused claim and the attempt it consumed, for a
	// worker stopped between claiming and running (ADR-CONCURRENCY-006).
	ReleaseClaim(ctx context.Context, id string, claimToken time.Time) error
	ListByPolicy(ctx context.Context, policyID string, limit int) ([]Execution, error)
	// ListByRun returns every Execution sharing runID, ordered by StepIndex —
	// the persistence side of RunProgress (ADR-POLICY-001). There is no
	// run-progress table: a run's Executions ARE its progress.
	ListByRun(ctx context.Context, runID string) ([]Execution, error)
	// SetGate records a step's Decision gate resolution. The executor calls it
	// with GatePending immediately after a gated step's action succeeds,
	// pausing the run at that step (ADR-POLICY-001 §3); a future gate
	// evaluator (W3) is what flips it to GatePassed. Unfenced by a claim
	// token: gate resolution is a separate write from the execution's own
	// claim/retry lifecycle, and by the time it is called the step's own
	// outcome is already durably recorded as succeeded.
	SetGate(ctx context.Context, id string, gate GateState) error
	// ListStalledRuns returns the ids of composed runs whose frontier step
	// succeeded with a resolved gate (none, or passed) but whose immediate
	// successor step has no Execution row at all — the permanent-stall gap a
	// worker crash between MarkResult(succeeded) and Enqueue(next step)
	// leaves behind, since ClaimDue only ever reselects pending/failed/
	// stuck-running rows, never a succeeded one. It over-includes a run whose
	// frontier is genuinely its LAST step (nothing left to enqueue): that is
	// harmless, because the caller (Reconciler) re-derives the run's real
	// cursor via RunProgress before acting, and a run RunProgress already
	// reports Complete is always a no-op. Never returns a single-action
	// execution's run (run_id is NULL for those) or a run parked on a
	// pending gate.
	ListStalledRuns(ctx context.Context, limit int) ([]string, error)
	// ListPendingGates returns the Executions currently paused at an
	// unresolved Decision gate — a succeeded step whose Gate is GatePending
	// — the read side of gate EVALUATION (ADR-POLICY-002), exactly as
	// ListStalledRuns is the read side of run advancement. At most one such
	// row exists per run at a time: only the frontier step is ever set to
	// GatePending.
	ListPendingGates(ctx context.Context, limit int) ([]Execution, error)
}

// ApprovalGate is the read-only quorum check an "approval" Gate consults
// (ADR-POLICY-002): how many distinct approvers have recorded an approval of
// the governance object that triggered the composed run (the run's own
// Event.CfgID), and how many the tenant's governance_required_approvals
// Setting currently requires (ADR-GOV-005). It reads exactly the data the
// Governance Engine's own ApprovalRecorder/EffectiveRequiredApprovals
// already decide Approval on — this is a second, read-only caller of that
// data, never a second quorum implementation.
//
// The gate evaluator (reconciler.go) runs on the same admin/background pool
// PolicyStore's own background instance uses (main.go) — every tenant's
// runs, not one request's bound connection — so tenantID is an explicit
// argument on every call rather than relied on from RLS (ADR-TENANCY-003).
type ApprovalGate interface {
	// CountDistinct returns the number of distinct approvers recorded for
	// (tenantID, governanceID).
	CountDistinct(ctx context.Context, tenantID, governanceID string) (int, error)
	// RequiredApprovals returns tenantID's configured Approval quorum
	// threshold, falling back to the Setting's own default when unset.
	RequiredApprovals(ctx context.Context, tenantID string) (int, error)
}
