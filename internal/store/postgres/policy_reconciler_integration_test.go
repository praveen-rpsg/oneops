//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/policy"
)

// TestPolicyStore_ListStalledRuns_Integration proves ListStalledRuns' query
// against real PostgreSQL: it finds a run whose frontier step succeeded with
// no successor row and no unresolved gate, and it excludes a gate-pending
// run, a fully-succeeded (Complete) run's own frontier is still reported (by
// design — the caller re-derives via RunProgress), and a single-action
// (run_id NULL) execution never appears.
func TestPolicyStore_ListStalledRuns_Integration(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	if _, err := pool.Exec(ctx, `TRUNCATE policy, policy_execution, policy_cursor CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	s := NewPolicyStore(pool)
	now := time.Now().UTC()

	// A wedged run: step 0 succeeded, ungated, no step 1 row.
	wedged := policy.Policy{ID: "pol_wedged", Name: "wedged", Enabled: true, MaxRetries: 3,
		Steps: policy.Sequence{{Action: policy.ActionSpec{Type: "a0"}}, {Action: policy.ActionSpec{Type: "a1"}}}}
	if err := s.Create(ctx, wedged); err != nil {
		t.Fatalf("create wedged policy: %v", err)
	}
	if err := s.Enqueue(ctx, []policy.Execution{
		{ID: "wedged_step0", PolicyID: wedged.ID, Status: policy.ExecPending, NextAttemptAt: now, RunID: "run_wedged", StepIndex: 0},
	}); err != nil {
		t.Fatalf("enqueue wedged step0: %v", err)
	}
	if err := s.MarkResult(ctx, "wedged_step0", time.Time{}, policy.ExecSucceeded, 0, "", now, now, time.Time{}); err != nil {
		t.Fatalf("mark wedged step0 succeeded: %v", err)
	}

	// A gate-pending run: must never be reported.
	gated := policy.Policy{ID: "pol_gated", Name: "gated", Enabled: true, MaxRetries: 3,
		Steps: policy.Sequence{{Action: policy.ActionSpec{Type: "a0"}, Gate: &policy.GateSpec{Type: "approval"}}, {Action: policy.ActionSpec{Type: "a1"}}}}
	if err := s.Create(ctx, gated); err != nil {
		t.Fatalf("create gated policy: %v", err)
	}
	if err := s.Enqueue(ctx, []policy.Execution{
		{ID: "gated_step0", PolicyID: gated.ID, Status: policy.ExecPending, NextAttemptAt: now, RunID: "run_gated", StepIndex: 0},
	}); err != nil {
		t.Fatalf("enqueue gated step0: %v", err)
	}
	if err := s.MarkResult(ctx, "gated_step0", time.Time{}, policy.ExecSucceeded, 0, "", now, now, time.Time{}); err != nil {
		t.Fatalf("mark gated step0 succeeded: %v", err)
	}
	if err := s.SetGate(ctx, "gated_step0", policy.GatePending); err != nil {
		t.Fatalf("set gate pending: %v", err)
	}

	// A single-action execution (run_id NULL): must never be reported.
	single := policy.Policy{ID: "pol_single", Name: "single", Enabled: true, MaxRetries: 3, Action: policy.ActionSpec{Type: "a0"}}
	if err := s.Create(ctx, single); err != nil {
		t.Fatalf("create single policy: %v", err)
	}
	if err := s.Enqueue(ctx, []policy.Execution{{ID: "solo_exec", PolicyID: single.ID, Status: policy.ExecPending, NextAttemptAt: now}}); err != nil {
		t.Fatalf("enqueue solo: %v", err)
	}
	if err := s.MarkResult(ctx, "solo_exec", time.Time{}, policy.ExecSucceeded, 0, "", now, now, time.Time{}); err != nil {
		t.Fatalf("mark solo succeeded: %v", err)
	}

	stalled, err := s.ListStalledRuns(ctx, 100)
	if err != nil {
		t.Fatalf("ListStalledRuns: %v", err)
	}
	if len(stalled) != 1 || stalled[0] != "run_wedged" {
		t.Fatalf("ListStalledRuns = %v, want exactly [run_wedged]", stalled)
	}
}

// TestPolicyReconciler_WedgedRun_HealsAgainstRealPostgres reproduces the
// crash this story closes end-to-end against real PostgreSQL: a worker's
// MarkResult(succeeded) for step 0 commits, then it crashes before calling
// advanceRun's Enqueue for step 1 — the exact permanent-stall gap ClaimDue
// cannot self-heal, because it never reselects a succeeded row. Running
// policy.Reconciler once must re-derive and enqueue the missing step 1, and
// the run must then actually progress under a live Executor pass.
func TestPolicyReconciler_WedgedRun_HealsAgainstRealPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	if _, err := pool.Exec(ctx, `TRUNCATE policy, policy_execution, policy_cursor CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	s := NewPolicyStore(pool)
	now := time.Now().UTC()

	pol := policy.Policy{
		ID: "pol_run_wedge", Name: "wedge", Enabled: true, MaxRetries: 3,
		Steps: policy.Sequence{{Action: policy.ActionSpec{Type: "noop"}}, {Action: policy.ActionSpec{Type: "noop"}}},
	}
	if err := s.Create(ctx, pol); err != nil {
		t.Fatalf("create policy: %v", err)
	}
	runID := "run_e2e_wedge"
	if err := s.Enqueue(ctx, []policy.Execution{
		{ID: policy.ExecutionID(runID, "step", 0), PolicyID: pol.ID, Status: policy.ExecPending, NextAttemptAt: now, RunID: runID, StepIndex: 0,
			Event: policy.Event{EventID: "evt_wedge", CfgID: "c1", Seq: 1, OccurredAt: now}},
	}); err != nil {
		t.Fatalf("enqueue step0: %v", err)
	}
	// The exact wedged state: MarkResult(succeeded) committed, no Enqueue for
	// step 1 followed it.
	if err := s.MarkResult(ctx, policy.ExecutionID(runID, "step", 0), time.Time{}, policy.ExecSucceeded, 0, "", now, now, time.Time{}); err != nil {
		t.Fatalf("mark step0 succeeded: %v", err)
	}
	if run, _ := s.ListByRun(ctx, runID); len(run) != 1 {
		t.Fatalf("precondition: run must be wedged with exactly 1 execution, got %d", len(run))
	}

	log := slog.New(slog.NewTextHandler(logDiscard{}, nil))
	r := policy.NewReconciler(s, s, log, policy.ReconcilerConfig{})
	r.RunOnce(ctx)

	run, err := s.ListByRun(ctx, runID)
	if err != nil {
		t.Fatalf("ListByRun after sweep: %v", err)
	}
	if len(run) != 2 {
		t.Fatalf("after the sweep, run has %d executions against real PostgreSQL, want 2 (step 1 enqueued): %+v", len(run), run)
	}
	if run[1].Status != policy.ExecPending || run[1].StepIndex != 1 {
		t.Fatalf("step 1 = %+v, want pending at StepIndex 1", run[1])
	}
	if run[1].Event.EventID != "evt_wedge" {
		t.Fatalf("step 1's Event = %+v, want the run's triggering event propagated from step 0", run[1].Event)
	}

	// Idempotent against real Postgres too: a second sweep must not duplicate the row.
	r.RunOnce(ctx)
	if run, _ = s.ListByRun(ctx, runID); len(run) != 2 {
		t.Fatalf("a second sweep produced %d executions, want still 2 (ON CONFLICT DO NOTHING)", len(run))
	}

	// The run now actually progresses: an executor pass claims and runs step 1.
	reg := policy.NewRegistry()
	reg.Register("noop", noopAction{})
	exec := policy.NewExecutor(s, s, noopOwners{}, reg, nil, log, policy.ExecutorConfig{})
	exec.RunOnce(ctx)
	run, err = s.ListByRun(ctx, runID)
	if err != nil {
		t.Fatalf("ListByRun after executor pass: %v", err)
	}
	rp := policy.RunProgress{RunID: runID, Seq: pol.Steps, Steps: run}
	if !rp.Complete() {
		t.Fatalf("run did not complete after healing + one executor pass: %+v", run)
	}
}

type noopAction struct{}

func (noopAction) Run(context.Context, policy.Event, json.RawMessage) error { return nil }

type noopOwners struct{}

func (noopOwners) ResolveEventOwner(context.Context, string, int64) (string, error) {
	return "system", nil
}

type logDiscard struct{}

func (logDiscard) Write(p []byte) (int, error) { return len(p), nil }
