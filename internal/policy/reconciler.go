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
