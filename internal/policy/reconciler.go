package policy

import (
	"context"
	"log/slog"
	"time"
)

// ReconcilerConfig parameterises the reconciliation sweep.
type ReconcilerConfig struct {
	Interval   time.Duration // between sweep passes (default 30s)
	BatchLimit int           // stalled runs re-derived per pass (default 100)
}

func (c *ReconcilerConfig) withDefaults() {
	if c.Interval <= 0 {
		c.Interval = 30 * time.Second
	}
	if c.BatchLimit <= 0 {
		c.BatchLimit = 100
	}
}

// Reconciler closes the permanent-stall gap advanceRun's at-commit design
// leaves open (executor.go, ADR-POLICY-001 §3): advanceRun runs AFTER
// MarkResult(succeeded) commits, so a worker that crashes between that
// commit and Enqueue(next step) leaves the run wedged forever — its frontier
// Execution is durably succeeded, but ClaimDue only ever reselects
// pending/failed/stuck-running rows, so nothing re-claims it and nothing
// re-derives the missing next step. Unlike the accepted at-least-once
// retry gap, this one does not self-heal.
//
// Reconciler periodically finds runs in exactly that state (a cheap,
// index-scoped query — ListStalledRuns, postgres.PolicyStore) and re-drives
// each one through advanceRunSteps, the SAME function advanceRun itself
// calls. There is exactly one place a composed run's next step is ever
// created; this is a second caller of it, not a second implementation.
//
// Leader-gated: wired under the same ops.RunAsLeader closure as every other
// singleton worker (cmd/controlplane/main.go), so only one replica sweeps at
// a time. Even if two replicas somehow ran concurrently, or the sweep raced
// a live executor advancing the same run, this is still safe —
// advanceRunSteps recomputes RunProgress fresh from the run's own
// Executions on every call and enqueues under a deterministic id with
// `ON CONFLICT (id) DO NOTHING`, so any duplicate decision enqueues the same
// row at most once.
type Reconciler struct {
	execs    ExecutionStore
	policies Store
	log      *slog.Logger
	now      func() time.Time
	cfg      ReconcilerConfig
	// approvals is the optional Decision-gate evaluator. When set, RunOnce
	// resolves pending "approval" gates (ADR-POLICY-002) before its stalled-run
	// scan; when nil, gate evaluation is skipped entirely and the Reconciler is
	// exactly the W2b durability sweep.
	approvals ApprovalGate
}

// NewReconciler builds the sweep. execs and policies are required.
func NewReconciler(execs ExecutionStore, policies Store, log *slog.Logger, cfg ReconcilerConfig) *Reconciler {
	if log == nil {
		log = slog.Default()
	}
	cfg.withDefaults()
	return &Reconciler{
		execs: execs, policies: policies, log: log,
		now: func() time.Time { return time.Now().UTC() }, cfg: cfg,
	}
}

// SetApprovalGate wires the approval-quorum evaluator (ADR-POLICY-002). Without
// it, RunOnce performs only stalled-run reconciliation and no gate ever resolves.
func (r *Reconciler) SetApprovalGate(a ApprovalGate) { r.approvals = a }

// Run sweeps on Interval until ctx is cancelled.
func (r *Reconciler) Run(ctx context.Context) error {
	r.log.Info("policy reconciler started", "interval", r.cfg.Interval.String())
	r.RunOnce(ctx)
	t := time.NewTicker(r.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			r.log.Info("policy reconciler stopped", "reason", ctx.Err())
			return ctx.Err()
		case <-t.C:
			r.RunOnce(ctx)
		}
	}
}

// RunOnce finds stalled composed runs and re-derives each one's next step.
// A candidate that has already healed by the time this pass reaches it
// (enqueued by a live worker, or by an earlier iteration of this same
// sweep) is simply a no-op here — advanceRunSteps recomputes the cursor
// fresh and finds nothing left to do.
func (r *Reconciler) RunOnce(ctx context.Context) {
	// Phase 1: resolve any Decision gates whose quorum now holds. A gate that
	// flips GatePending->GatePassed here makes its run match the stalled-run
	// scan below, so resolution and advancement compose through the row in the
	// same pass — never through a direct call.
	r.evaluateGates(ctx)

	runIDs, err := r.execs.ListStalledRuns(ctx, r.cfg.BatchLimit)
	if err != nil {
		r.log.Error("policy reconciler: list stalled runs", "err", err)
		return
	}
	for _, runID := range runIDs {
		if ctx.Err() != nil {
			return
		}
		r.reconcileRun(ctx, runID)
	}
}

func (r *Reconciler) reconcileRun(ctx context.Context, runID string) {
	steps, err := r.execs.ListByRun(ctx, runID)
	if err != nil {
		r.log.Error("policy reconciler: list run executions", "run_id", runID, "err", err)
		return
	}
	if len(steps) == 0 {
		// Healed or pruned between the candidate scan and this pass.
		return
	}
	// The Policy is authoritative for the run's current Sequence, fetched by
	// id — never trusted from an Execution row — exactly as the executor's
	// own attempt() re-fetches it before running a step.
	p, err := r.policies.Get(ctx, steps[0].PolicyID)
	if err != nil {
		r.log.Error("policy reconciler: get policy", "run_id", runID, "policy_id", steps[0].PolicyID, "err", err)
		return
	}
	advanceRunSteps(ctx, r.execs, r.log, r.now, p, runID, steps)
}

// evaluateGates resolves steps paused at a pending Decision gate. It is a no-op
// unless an ApprovalGate has been wired. Every failure mode leaves the gate
// PENDING — the only way past a gate is a genuinely satisfied Decision, so a
// missing subject, an unreadable count, or an unrecognised gate type must never
// advance the run (a gate that opens without its Decision is an approval bypass).
func (r *Reconciler) evaluateGates(ctx context.Context) {
	if r.approvals == nil {
		return
	}
	gates, err := r.execs.ListPendingGates(ctx, r.cfg.BatchLimit)
	if err != nil {
		r.log.Error("policy reconciler: list pending gates", "err", err)
		return
	}
	for i := range gates {
		if ctx.Err() != nil {
			return
		}
		r.evaluateGate(ctx, gates[i])
	}
}

// evaluateGate dispatches on the gate's declared type, resolved from the owning
// Policy (never trusted from the Execution row, which records only the gate's
// state, not its spec). An unrecognised type is left pending and logged — never
// guessed at.
func (r *Reconciler) evaluateGate(ctx context.Context, ex Execution) {
	p, err := r.policies.Get(ctx, ex.PolicyID)
	if err != nil {
		r.log.Error("policy reconciler: get policy for gate", "run_id", ex.RunID, "policy_id", ex.PolicyID, "err", err)
		return
	}
	if ex.StepIndex < 0 || ex.StepIndex >= len(p.Steps) {
		r.log.Warn("policy reconciler: gate step out of range", "run_id", ex.RunID, "step_index", ex.StepIndex)
		return
	}
	gate := p.Steps[ex.StepIndex].Gate
	if gate == nil {
		// The row says pending but the current Sequence has no gate here
		// (policy edited mid-run); leave it — advancement will reconcile.
		return
	}
	switch gate.Type {
	case GateTypeApproval:
		r.evaluateApprovalGate(ctx, ex)
	default:
		r.log.Warn("policy reconciler: unrecognised gate type, leaving pending",
			"run_id", ex.RunID, "gate_type", gate.Type)
	}
}

// evaluateApprovalGate flips the gate to GatePassed only when the run's own
// triggering subject (Event.CfgID, the governance object this run is about) has
// at least the tenant's required distinct approvals (ADR-GOV-005). It reuses the
// exact quorum data Approval owns; it never counts quorum itself. An empty
// subject, or any read error, leaves the gate pending.
func (r *Reconciler) evaluateApprovalGate(ctx context.Context, ex Execution) {
	tenantID := ex.Event.TenantID
	governanceID := ex.Event.CfgID
	if governanceID == "" {
		// A run not triggered by an approvable object can never satisfy an
		// approval gate — fail-safe, it simply stays paused.
		return
	}
	required, err := r.approvals.RequiredApprovals(ctx, tenantID)
	if err != nil {
		r.log.Error("policy reconciler: read required approvals", "run_id", ex.RunID, "err", err)
		return
	}
	count, err := r.approvals.CountDistinct(ctx, tenantID, governanceID)
	if err != nil {
		r.log.Error("policy reconciler: count approvals", "run_id", ex.RunID, "err", err)
		return
	}
	if count < required {
		return // below quorum — leave pending
	}
	if err := r.execs.SetGate(ctx, ex.ID, GatePassed); err != nil {
		r.log.Error("policy reconciler: set gate passed", "run_id", ex.RunID, "err", err)
	}
}
