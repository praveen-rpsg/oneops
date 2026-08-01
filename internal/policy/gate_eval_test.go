package policy

import (
	"context"
	"testing"
	"time"
)

// ---- the load-bearing test: pause below quorum, then resolve and resume ------

// TestGateEvaluator_ApprovalGate_PausesBelowQuorum_ThenResolvesAndResumes is
// the end-to-end shape this story exists to prove: a 3-step run with an
// approval gate on step 1 runs step 0 and step 1, pauses at the gate while
// below the tenant's required quorum — repeatedly, across several sweeps —
// and only advances to step 2 once the required distinct approvals are
// actually recorded, in the very same sweep that flips the gate. If
// evaluateGates were disabled (or never called), this run would stay paused
// forever: nothing else in this package ever calls SetGate(GatePassed).
func TestGateEvaluator_ApprovalGate_PausesBelowQuorum_ThenResolvesAndResumes(t *testing.T) {
	log := &callLog{}
	reg := NewRegistry()
	reg.Register("a0", namedAction{name: "a0", log: log})
	reg.Register("a1", namedAction{name: "a1", log: log})
	reg.Register("a2", namedAction{name: "a2", log: log})

	pol := Policy{
		ID: "p-approval-gate", TenantID: testTenant, Enabled: true, MaxRetries: 3,
		Steps: Sequence{
			{Action: ActionSpec{Type: "a0"}},
			{Action: ActionSpec{Type: "a1"}, Gate: &GateSpec{Type: GateTypeApproval}},
			{Action: ActionSpec{Type: "a2"}},
		},
	}
	pols := newFakePolicies(pol)
	execs := newFakeExecs()
	c := NewConsumer(&fakeSource{}, newFakeCursor(), pols, execs, nil, quiet(), ConsumerConfig{})
	governanceID := "gov-1"
	ev := Event{TenantID: testTenant, CfgID: governanceID, Seq: 1}
	start := c.Evaluate(ev, []Policy{pol}, time.Now().UTC())
	if err := execs.Enqueue(context.Background(), start); err != nil {
		t.Fatalf("enqueue run start: %v", err)
	}
	runID := start[0].RunID

	e := NewExecutor(execs, pols, newFakePolicyOwners(), reg, nil, quiet(), ExecutorConfig{})
	e.RunOnce(context.Background()) // step 0 succeeds, ungated -> enqueues step 1
	e.RunOnce(context.Background()) // step 1 (gated) succeeds -> pauses at the gate

	run, _ := execs.ListByRun(context.Background(), runID)
	if len(run) != 2 || run[1].Gate != GatePending {
		t.Fatalf("after step 1, run = %+v, want 2 executions with step 1 GatePending", run)
	}

	approvals := newFakeApprovalGate()
	approvals.setRequired(testTenant, 2)
	r := NewReconciler(execs, pols, quiet(), ReconcilerConfig{})
	r.SetApprovalGate(approvals)

	// Below quorum: several sweeps in a row must never flip the gate or
	// enqueue step 2. This is the fail-safe biting, not merely asserted once.
	approvals.approve(testTenant, governanceID, "alice")
	for i := 0; i < 3; i++ {
		r.RunOnce(context.Background())
		run, _ = execs.ListByRun(context.Background(), runID)
		if len(run) != 2 {
			t.Fatalf("sweep %d: run gained a step while below quorum: %+v", i, run)
		}
		if run[1].Gate != GatePending {
			t.Fatalf("sweep %d: gate = %q, want unchanged %q while below quorum", i, run[1].Gate, GatePending)
		}
	}
	if n := log.count("a2"); n != 0 {
		t.Fatalf("a2 ran %d times while below quorum, want 0", n)
	}

	// Quorum met: the SAME sweep both flips the gate and enqueues step 2 —
	// evaluateGates runs before the stalled-run scan in one RunOnce call.
	approvals.approve(testTenant, governanceID, "bob")
	r.RunOnce(context.Background())

	run, _ = execs.ListByRun(context.Background(), runID)
	if len(run) != 3 {
		t.Fatalf("after quorum met, run has %d executions, want 3 (step 2 enqueued): %+v", len(run), run)
	}
	if run[1].Gate != GatePassed {
		t.Fatalf("step 1's gate = %q, want %q once quorum is met", run[1].Gate, GatePassed)
	}
	if run[2].StepIndex != 2 || run[2].Status != ExecPending {
		t.Fatalf("step 2 = %+v, want pending at StepIndex 2", run[2])
	}

	// The run actually completes: a resolved gate is meaningless if nothing
	// ever runs the step it unblocked.
	e.RunOnce(context.Background())
	if n := log.count("a2"); n != 1 {
		t.Fatalf("a2 ran %d times after resume, want exactly 1", n)
	}
	run, _ = execs.ListByRun(context.Background(), runID)
	rp := RunProgress{RunID: runID, Seq: pol.Steps, Steps: run}
	if !rp.Complete() {
		t.Errorf("run should be Complete once step 2 succeeds: %+v", run)
	}
}

// ---- idempotency: double evaluation flips the gate once ----------------------

func TestGateEvaluator_ApprovalGate_Idempotent_DoubleEvalFlipsOnce(t *testing.T) {
	pol := Policy{
		ID: "p-idem-gate", TenantID: testTenant, Enabled: true, MaxRetries: 3,
		Steps: Sequence{
			{Action: ActionSpec{Type: "a0"}, Gate: &GateSpec{Type: GateTypeApproval}},
			{Action: ActionSpec{Type: "a1"}},
		},
	}
	execs := newFakeExecs()
	runID := "run-idem-gate"
	step0 := Execution{
		ID: ExecutionID(runID, stepTag, 0), PolicyID: pol.ID, RunID: runID, StepIndex: 0,
		Status: ExecSucceeded, Gate: GatePending, Event: Event{TenantID: testTenant, CfgID: "gov-idem"},
	}
	if err := execs.Enqueue(context.Background(), []Execution{step0}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	approvals := newFakeApprovalGate()
	approvals.approve(testTenant, "gov-idem", "alice") // required defaults to 1: quorum already met
	r := NewReconciler(execs, newFakePolicies(pol), quiet(), ReconcilerConfig{})
	r.SetApprovalGate(approvals)

	r.RunOnce(context.Background())
	run, _ := execs.ListByRun(context.Background(), runID)
	if len(run) != 2 || run[0].Gate != GatePassed {
		t.Fatalf("after first sweep, run = %+v, want step 0 passed and step 1 enqueued", run)
	}

	// A second sweep must not touch the already-passed gate or double-enqueue
	// step 1 (ExecutionID is deterministic; Enqueue's ON CONFLICT is a no-op).
	r.RunOnce(context.Background())
	run2, _ := execs.ListByRun(context.Background(), runID)
	if len(run2) != 2 {
		t.Fatalf("after second sweep, run has %d executions, want 2 (idempotent)", len(run2))
	}
	if run2[0].Gate != GatePassed {
		t.Fatalf("after second sweep, gate = %q, want unchanged %q", run2[0].Gate, GatePassed)
	}
}

// ---- fail-safe: missing subject, evaluator errors, unrecognised type ---------

func TestGateEvaluator_ApprovalGate_MissingSubject_LeavesPending(t *testing.T) {
	pol := Policy{
		ID: "p-nosubject", TenantID: testTenant, Enabled: true, MaxRetries: 3,
		Steps: Sequence{{Action: ActionSpec{Type: "a0"}, Gate: &GateSpec{Type: GateTypeApproval}}},
	}
	execs := newFakeExecs()
	runID := "run-nosubject"
	step0 := Execution{
		ID: ExecutionID(runID, stepTag, 0), PolicyID: pol.ID, RunID: runID, StepIndex: 0,
		Status: ExecSucceeded, Gate: GatePending,
		// No CfgID: the run's triggering event carries no governance subject.
		Event: Event{TenantID: testTenant},
	}
	_ = execs.Enqueue(context.Background(), []Execution{step0})

	approvals := newFakeApprovalGate()
	// Even if some OTHER (tenant, "") pair happens to be "approved", the
	// evaluator must refuse before ever consulting it: an empty subject is
	// never a real one.
	approvals.approve(testTenant, "", "attacker")
	r := NewReconciler(execs, newFakePolicies(pol), quiet(), ReconcilerConfig{})
	r.SetApprovalGate(approvals)

	r.RunOnce(context.Background())

	run, _ := execs.ListByRun(context.Background(), runID)
	if run[0].Gate != GatePending {
		t.Fatalf("gate = %q, want unchanged %q when the run has no governance subject", run[0].Gate, GatePending)
	}
}

func TestGateEvaluator_ApprovalGate_CountError_LeavesPending(t *testing.T) {
	pol := Policy{
		ID: "p-counterr", TenantID: testTenant, Enabled: true, MaxRetries: 3,
		Steps: Sequence{{Action: ActionSpec{Type: "a0"}, Gate: &GateSpec{Type: GateTypeApproval}}},
	}
	execs := newFakeExecs()
	runID := "run-counterr"
	step0 := Execution{
		ID: ExecutionID(runID, stepTag, 0), PolicyID: pol.ID, RunID: runID, StepIndex: 0,
		Status: ExecSucceeded, Gate: GatePending, Event: Event{TenantID: testTenant, CfgID: "gov-err"},
	}
	_ = execs.Enqueue(context.Background(), []Execution{step0})

	approvals := newFakeApprovalGate()
	approvals.countErr = errBoom
	r := NewReconciler(execs, newFakePolicies(pol), quiet(), ReconcilerConfig{})
	r.SetApprovalGate(approvals)

	r.RunOnce(context.Background())

	run, _ := execs.ListByRun(context.Background(), runID)
	if run[0].Gate != GatePending {
		t.Fatalf("gate = %q, want unchanged %q when the quorum count cannot be read", run[0].Gate, GatePending)
	}
}

func TestGateEvaluator_UnrecognisedGateType_LeavesPending(t *testing.T) {
	pol := Policy{
		ID: "p-unknown-gate", TenantID: testTenant, Enabled: true, MaxRetries: 3,
		Steps: Sequence{{Action: ActionSpec{Type: "a0"}, Gate: &GateSpec{Type: "timer"}}},
	}
	execs := newFakeExecs()
	runID := "run-unknown-gate"
	step0 := Execution{
		ID: ExecutionID(runID, stepTag, 0), PolicyID: pol.ID, RunID: runID, StepIndex: 0,
		Status: ExecSucceeded, Gate: GatePending, Event: Event{TenantID: testTenant, CfgID: "gov-timer"},
	}
	_ = execs.Enqueue(context.Background(), []Execution{step0})

	approvals := newFakeApprovalGate() // quorum trivially met (0 required 0? default 1, 0 approvers -> not met anyway)
	approvals.approve(testTenant, "gov-timer", "alice")
	r := NewReconciler(execs, newFakePolicies(pol), quiet(), ReconcilerConfig{})
	r.SetApprovalGate(approvals)

	r.RunOnce(context.Background())

	run, _ := execs.ListByRun(context.Background(), runID)
	if run[0].Gate != GatePending {
		t.Fatalf("gate = %q, want unchanged %q for an unrecognised gate type — never guessed at", run[0].Gate, GatePending)
	}
}
