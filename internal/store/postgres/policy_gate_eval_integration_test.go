//go:build integration

package postgres

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/policy"
)

// fixedOwner is a stand-in domain.EventOwnerResolver that vouches for one
// tenant regardless of chain/seq — the composed run below is triggered by a
// real configuration_object, but ownership re-derivation (ADR-TENANCY-003) is
// not itself the subject of this test.
type fixedOwner struct{ tenantID string }

func (f fixedOwner) ResolveEventOwner(context.Context, string, int64) (string, error) {
	return f.tenantID, nil
}

// TestGateEvaluator_ApprovalGate_PausesBelowQuorum_ThenResolvesAndResumes_Integration
// is the end-to-end proof this story exists to deliver, against real
// PostgreSQL: a 3-step composed run with an approval gate on step 1 runs
// step 0 and step 1, pauses at the gate while the tenant's real
// governance_required_approvals quorum is unmet — across several sweeps —
// and only flips to GatePassed and enqueues step 2 once the required
// distinct approvals are actually recorded in approval_record, the SAME
// table and threshold ADR-GOV-005's Approval quorum enforces. It then proves
// the run actually completes, and that disabling the gate flip leaves it
// paused forever.
func TestGateEvaluator_ApprovalGate_PausesBelowQuorum_ThenResolvesAndResumes_Integration(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	if _, err := pool.Exec(ctx, `TRUNCATE policy, policy_execution, policy_cursor CASCADE`); err != nil {
		t.Fatalf("truncate policy tables: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM approval_record`); err != nil {
		t.Fatalf("clear approval_record: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM setting WHERE tenant_id = $1 AND key = $2`,
		domain.SystemTenantID, domain.GovernanceRequiredApprovalsKey); err != nil {
		t.Fatalf("clear required-approvals setting: %v", err)
	}
	// The tenant requires 2 distinct approvals — the same Setting ADR-GOV-005
	// reads for §8 Approval itself.
	if _, err := pool.Exec(ctx, `INSERT INTO setting (tenant_id, key, value) VALUES ($1,$2,'2')`,
		domain.SystemTenantID, domain.GovernanceRequiredApprovalsKey); err != nil {
		t.Fatalf("seed required-approvals setting: %v", err)
	}

	co := NewConfigObjectRepo(pool)
	governanceID := mkObj(t, co, "gov-gate-eval.md", domain.RetentionWorkingMaterial)

	s := NewPolicyStore(pool)
	pol := policy.Policy{
		ID: "pol_gate_eval", Name: "gate-eval", Enabled: true, MaxRetries: 3,
		Steps: policy.Sequence{
			{Action: policy.ActionSpec{Type: "a0"}},
			{Action: policy.ActionSpec{Type: "a1"}, Gate: &policy.GateSpec{Type: policy.GateTypeApproval}},
			{Action: policy.ActionSpec{Type: "a2"}},
		},
	}
	if err := s.Create(ctx, pol); err != nil {
		t.Fatalf("create policy: %v", err)
	}
	// adminTestCtx carries no bound tenant, so Create's domain.TenantIDFrom
	// falls back to SystemTenantID — the same tenant fixedOwner below vouches
	// for, and the same tenant the required-approvals setting above was seeded
	// against.
	pol, err := s.Get(ctx, pol.ID)
	if err != nil {
		t.Fatalf("get policy: %v", err)
	}
	if pol.TenantID != domain.SystemTenantID {
		t.Fatalf("policy tenant = %q, want %q", pol.TenantID, domain.SystemTenantID)
	}

	runID := "run_gate_eval_integration"
	now := time.Now().UTC()
	if err := s.Enqueue(ctx, []policy.Execution{
		{ID: policy.ExecutionID(runID, "step", 0), PolicyID: pol.ID, Status: policy.ExecPending, NextAttemptAt: now, RunID: runID, StepIndex: 0,
			Event: policy.Event{TenantID: pol.TenantID, CfgID: governanceID, Seq: 1, EventID: "evt_gate_eval"}},
	}); err != nil {
		t.Fatalf("enqueue step0: %v", err)
	}

	reg := policy.NewRegistry()
	reg.Register("a0", noopAction{})
	reg.Register("a1", noopAction{})
	reg.Register("a2", noopAction{})
	log := slog.New(slog.NewTextHandler(logDiscard{}, nil))
	owners := fixedOwner{tenantID: pol.TenantID}
	exec := policy.NewExecutor(s, s, owners, reg, nil, log, policy.ExecutorConfig{})

	exec.RunOnce(ctx) // step 0 succeeds, ungated -> enqueues step 1
	exec.RunOnce(ctx) // step 1 (gated) succeeds -> pauses at the gate

	run, err := s.ListByRun(ctx, runID)
	if err != nil {
		t.Fatalf("ListByRun after step 1: %v", err)
	}
	if len(run) != 2 || run[1].Gate != policy.GatePending {
		t.Fatalf("after step 1 against real Postgres, run = %+v, want 2 executions with step 1 GatePending", run)
	}

	approvalStore := NewApprovalStore(pool)
	gateStore := NewApprovalGateStore(pool)
	r := policy.NewReconciler(s, s, log, policy.ReconcilerConfig{})
	r.SetApprovalGate(gateStore)

	// --- below quorum: repeated sweeps must never flip the gate or advance ---
	record(t, ctx, pool, approvalStore, pol.TenantID, governanceID, "alice")
	for i := 0; i < 3; i++ {
		r.RunOnce(ctx)
		run, err = s.ListByRun(ctx, runID)
		if err != nil {
			t.Fatalf("ListByRun sweep %d: %v", i, err)
		}
		if len(run) != 2 {
			t.Fatalf("sweep %d against real Postgres: run gained a step while below quorum: %+v", i, run)
		}
		if run[1].Gate != policy.GatePending {
			t.Fatalf("sweep %d against real Postgres: gate = %q, want unchanged %q while below quorum (1 of 2 required)", i, run[1].Gate, policy.GatePending)
		}
	}
	if n, err := gateStore.CountDistinct(ctx, pol.TenantID, governanceID); err != nil || n != 1 {
		t.Fatalf("CountDistinct = %d, %v, want 1", n, err)
	}

	// --- prove the sweep's flip is load-bearing: disable it, confirm the run
	//     stays paused even at quorum, then restore and confirm it resolves ---
	record(t, ctx, pool, approvalStore, pol.TenantID, governanceID, "bob") // now at quorum (2)
	if n, err := gateStore.CountDistinct(ctx, pol.TenantID, governanceID); err != nil || n != 2 {
		t.Fatalf("CountDistinct = %d, %v, want 2 (quorum met)", n, err)
	}
	unwired := policy.NewReconciler(s, s, log, policy.ReconcilerConfig{}) // no SetApprovalGate call
	unwired.RunOnce(ctx)
	run, err = s.ListByRun(ctx, runID)
	if err != nil {
		t.Fatalf("ListByRun after unwired sweep: %v", err)
	}
	if len(run) != 2 || run[1].Gate != policy.GatePending {
		t.Fatalf("an unwired reconciler advanced a gated run against real Postgres: %+v — the flip must be the ONLY path past a gate", run)
	}

	// --- quorum met, evaluator wired: the SAME sweep flips the gate AND
	//     enqueues step 2 (evaluateGates runs before the stalled-run scan) ---
	r.RunOnce(ctx)
	run, err = s.ListByRun(ctx, runID)
	if err != nil {
		t.Fatalf("ListByRun after quorum met: %v", err)
	}
	if len(run) != 3 {
		t.Fatalf("after quorum met against real Postgres, run has %d executions, want 3 (step 2 enqueued): %+v", len(run), run)
	}
	if run[1].Gate != policy.GatePassed {
		t.Fatalf("step 1's gate = %q, want %q once quorum is met", run[1].Gate, policy.GatePassed)
	}
	if run[2].StepIndex != 2 || run[2].Status != policy.ExecPending {
		t.Fatalf("step 2 = %+v, want pending at StepIndex 2", run[2])
	}

	// --- idempotency: a second sweep over an already-passed gate is a no-op ---
	r.RunOnce(ctx)
	run2, err := s.ListByRun(ctx, runID)
	if err != nil {
		t.Fatalf("ListByRun after second sweep: %v", err)
	}
	if len(run2) != 3 || run2[1].Gate != policy.GatePassed {
		t.Fatalf("a second sweep against real Postgres changed the run: %+v, want unchanged 3 executions with step 1 still passed", run2)
	}

	// --- resumption completes the run ---
	exec.RunOnce(ctx)
	run, err = s.ListByRun(ctx, runID)
	if err != nil {
		t.Fatalf("ListByRun after resume: %v", err)
	}
	rp := policy.RunProgress{RunID: runID, Seq: pol.Steps, Steps: run}
	if !rp.Complete() {
		t.Fatalf("run did not complete after gate resolution + one executor pass against real Postgres: %+v", run)
	}
}

// record wraps ApprovalStore.Record in its own transaction, mirroring how the
// Governance Engine itself calls it (one approval per transaction) — the
// fixture path for this test, not the subject under test.
func record(t *testing.T, ctx context.Context, pool *pgxpool.Pool, store *ApprovalStore, tenantID, governanceID, approver string) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin approval tx: %v", err)
	}
	if err := store.Record(ctx, tx, tenantID, governanceID, approver); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("record approval for %s: %v", approver, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit approval tx: %v", err)
	}
}
