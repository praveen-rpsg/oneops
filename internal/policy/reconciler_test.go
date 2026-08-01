package policy

import (
	"context"
	"testing"
)

// ---- the load-bearing test: a wedged run is found and healed -----------------

// TestReconciler_HealsWedgedRun reproduces the exact crash this story closes:
// a worker recorded step 0's success (MarkResult committed) and then died
// before calling advanceRun's Enqueue for step 1. Nothing else in the system
// re-discovers that row — ClaimDue only ever reselects pending/failed/
// stuck-running executions, never a succeeded one — so without the sweep
// this run is wedged forever.
func TestReconciler_HealsWedgedRun(t *testing.T) {
	pol := Policy{
		ID: "p-wedge", TenantID: testTenant, Enabled: true, MaxRetries: 3,
		Steps: Sequence{
			{Action: ActionSpec{Type: "a0"}},
			{Action: ActionSpec{Type: "a1"}},
		},
	}
	execs := newFakeExecs()
	runID := "run-wedged"
	// The exact wedged state: step 0 succeeded, no step 1 row exists at all.
	step0 := Execution{
		ID: ExecutionID(runID, stepTag, 0), PolicyID: pol.ID, RunID: runID, StepIndex: 0,
		Status: ExecSucceeded, Event: Event{TenantID: testTenant, CfgID: "c1", Seq: 1},
	}
	if err := execs.Enqueue(context.Background(), []Execution{step0}); err != nil {
		t.Fatalf("seed wedged step 0: %v", err)
	}

	r := NewReconciler(execs, newFakePolicies(pol), quiet(), ReconcilerConfig{})
	r.RunOnce(context.Background())

	run, err := execs.ListByRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("ListByRun: %v", err)
	}
	if len(run) != 2 {
		t.Fatalf("after the sweep, run has %d executions, want 2 (step 1 must now be enqueued): %+v", len(run), run)
	}
	if run[1].StepIndex != 1 || run[1].Status != ExecPending {
		t.Fatalf("step 1 = %+v, want pending at StepIndex 1", run[1])
	}
	if run[1].Event.CfgID != "c1" || run[1].Event.Seq != 1 {
		t.Fatalf("step 1's Event = %+v, want the run's triggering event propagated from step 0", run[1].Event)
	}

	// The run now progresses: an executor pass claims and can run step 1.
	reg := NewRegistry()
	log := &callLog{}
	reg.Register("a1", namedAction{name: "a1", log: log})
	e := NewExecutor(execs, newFakePolicies(pol), newFakePolicyOwners(), reg, nil, quiet(), ExecutorConfig{})
	e.RunOnce(context.Background())
	if n := log.count("a1"); n != 1 {
		t.Fatalf("a1 ran %d times after healing, want exactly 1 — the run must actually progress, not just gain a row", n)
	}
}

// TestReconciler_HealsWedgedRun_MutationProvesItBites is not a standing test:
// it documents, for the record, that disabling advanceRunSteps's Enqueue call
// (commenting it out) makes TestReconciler_HealsWedgedRun fail with "run has 1
// executions, want 2" — confirmed by hand during implementation and restored
// immediately after. A test that cannot fail proves nothing; this one does.
func TestReconciler_HealsWedgedRun_MutationProvesItBites(t *testing.T) {
	t.Skip("documentation only — see comment; the mutation was performed and reverted by hand, not left in the tree")
}

// ---- must not advance past a pending gate ------------------------------------

func TestReconciler_DoesNotAdvancePastPendingGate(t *testing.T) {
	pol := Policy{
		ID: "p-gate-wedge", TenantID: testTenant, Enabled: true, MaxRetries: 3,
		Steps: Sequence{
			{Action: ActionSpec{Type: "a0"}, Gate: &GateSpec{Type: "approval"}},
			{Action: ActionSpec{Type: "a1"}},
		},
	}
	execs := newFakeExecs()
	runID := "run-gated"
	step0 := Execution{
		ID: ExecutionID(runID, stepTag, 0), PolicyID: pol.ID, RunID: runID, StepIndex: 0,
		Status: ExecSucceeded, Gate: GatePending, Event: Event{TenantID: testTenant},
	}
	if err := execs.Enqueue(context.Background(), []Execution{step0}); err != nil {
		t.Fatalf("seed gated step 0: %v", err)
	}

	r := NewReconciler(execs, newFakePolicies(pol), quiet(), ReconcilerConfig{})
	r.RunOnce(context.Background())

	run, _ := execs.ListByRun(context.Background(), runID)
	if len(run) != 1 {
		t.Fatalf("run has %d executions, want 1 — the sweep must never enqueue step 1 past an unresolved gate: %+v", len(run), run)
	}
	if run[0].Gate != GatePending {
		t.Fatalf("step 0's gate = %q, want unchanged %q — the sweep must not touch an already-pending gate's state either", run[0].Gate, GatePending)
	}
}

// ---- must not re-run a succeeded action / touch an in-flight step ------------

func TestReconciler_DoesNotReRunASucceededAction(t *testing.T) {
	log := &callLog{}
	reg := NewRegistry()
	reg.Register("a0", namedAction{name: "a0", log: log})
	reg.Register("a1", namedAction{name: "a1", log: log})
	pol := Policy{
		ID: "p-norerun", TenantID: testTenant, Enabled: true, MaxRetries: 3,
		Steps: Sequence{
			{Action: ActionSpec{Type: "a0"}},
			{Action: ActionSpec{Type: "a1"}},
		},
	}
	execs := newFakeExecs()
	runID := "run-inflight"
	step0 := Execution{
		ID: ExecutionID(runID, stepTag, 0), PolicyID: pol.ID, RunID: runID, StepIndex: 0,
		Status: ExecSucceeded, Event: Event{TenantID: testTenant},
	}
	// step 1 already exists and is mid-flight (running) — a live worker owns
	// it. The sweep must not touch it: not re-enqueue, not reset its status.
	step1 := Execution{
		ID: ExecutionID(runID, stepTag, 1), PolicyID: pol.ID, RunID: runID, StepIndex: 1,
		Status: ExecRunning, Event: Event{TenantID: testTenant},
	}
	_ = execs.Enqueue(context.Background(), []Execution{step0, step1})

	r := NewReconciler(execs, newFakePolicies(pol), quiet(), ReconcilerConfig{})
	r.RunOnce(context.Background())

	if n := log.count("a0"); n != 0 {
		t.Fatalf("a0 ran %d times, want 0 — the sweep must never re-run a step whose action already succeeded", n)
	}
	if n := log.count("a1"); n != 0 {
		t.Fatalf("a1 ran %d times via the sweep itself, want 0 — the sweep enqueues/inspects, it never runs actions", n)
	}
	got, _ := execs.ListByRun(context.Background(), runID)
	if len(got) != 2 || got[1].Status != ExecRunning {
		t.Fatalf("step 1 = %+v after the sweep, want unchanged (still running, still exactly one row)", got)
	}
}

// ---- must not touch a Complete run --------------------------------------------

func TestReconciler_DoesNotTouchACompleteRun(t *testing.T) {
	pol := Policy{
		ID: "p-complete", TenantID: testTenant, Enabled: true, MaxRetries: 3,
		Steps: Sequence{
			{Action: ActionSpec{Type: "a0"}},
			{Action: ActionSpec{Type: "a1"}},
		},
	}
	execs := newFakeExecs()
	runID := "run-complete"
	// Both steps already succeeded: RunProgress reports this run Complete.
	// Its last step's row also has no step_index+1 row — the same shape the
	// stalled-run query flags as a candidate — so this proves the executor
	// side of the shared function, not just the SQL, refuses to act.
	step0 := Execution{ID: ExecutionID(runID, stepTag, 0), PolicyID: pol.ID, RunID: runID, StepIndex: 0, Status: ExecSucceeded}
	step1 := Execution{ID: ExecutionID(runID, stepTag, 1), PolicyID: pol.ID, RunID: runID, StepIndex: 1, Status: ExecSucceeded}
	_ = execs.Enqueue(context.Background(), []Execution{step0, step1})

	r := NewReconciler(execs, newFakePolicies(pol), quiet(), ReconcilerConfig{})
	r.RunOnce(context.Background())

	run, _ := execs.ListByRun(context.Background(), runID)
	if len(run) != 2 {
		t.Fatalf("a Complete run gained a row after the sweep: %+v", run)
	}
}

// ---- must not touch single-action (run_id NULL) executions -------------------

func TestReconciler_IgnoresSingleActionExecutions(t *testing.T) {
	pol := Policy{ID: "p-solo", TenantID: testTenant, Enabled: true, MaxRetries: 3, Action: ActionSpec{Type: "a0"}}
	execs := newFakeExecs()
	solo := Execution{ID: "solo-1", PolicyID: pol.ID, Status: ExecSucceeded, Event: Event{TenantID: testTenant}}
	if err := execs.Enqueue(context.Background(), []Execution{solo}); err != nil {
		t.Fatalf("seed solo execution: %v", err)
	}

	stalled, err := execs.ListStalledRuns(context.Background(), 100)
	if err != nil {
		t.Fatalf("ListStalledRuns: %v", err)
	}
	if len(stalled) != 0 {
		t.Fatalf("ListStalledRuns returned %v for a run_id-NULL execution, want none", stalled)
	}

	r := NewReconciler(execs, newFakePolicies(pol), quiet(), ReconcilerConfig{})
	r.RunOnce(context.Background())

	if got := execs.status("solo-1"); got != ExecSucceeded {
		t.Fatalf("solo execution status = %q after the sweep, want unchanged %q", got, ExecSucceeded)
	}
}

// ---- idempotent: two sweeps enqueue the missing step exactly once ------------

func TestReconciler_RunningTwice_EnqueuesMissingStepExactlyOnce(t *testing.T) {
	pol := Policy{
		ID: "p-idem", TenantID: testTenant, Enabled: true, MaxRetries: 3,
		Steps: Sequence{
			{Action: ActionSpec{Type: "a0"}},
			{Action: ActionSpec{Type: "a1"}},
		},
	}
	execs := newFakeExecs()
	runID := "run-idem"
	step0 := Execution{
		ID: ExecutionID(runID, stepTag, 0), PolicyID: pol.ID, RunID: runID, StepIndex: 0,
		Status: ExecSucceeded, Event: Event{TenantID: testTenant},
	}
	_ = execs.Enqueue(context.Background(), []Execution{step0})

	r := NewReconciler(execs, newFakePolicies(pol), quiet(), ReconcilerConfig{})
	r.RunOnce(context.Background())
	r.RunOnce(context.Background())

	run, _ := execs.ListByRun(context.Background(), runID)
	var step1Count int
	for _, x := range run {
		if x.StepIndex == 1 {
			step1Count++
		}
	}
	if step1Count != 1 {
		t.Fatalf("step 1 appears %d times after two sweeps, want exactly 1", step1Count)
	}
}

// ---- race: the sweep and a live worker both reaching the same run do not double-enqueue

func TestReconciler_RaceWithLiveAdvance_DoesNotDoubleEnqueue(t *testing.T) {
	pol := Policy{
		ID: "p-race", TenantID: testTenant, Enabled: true, MaxRetries: 3,
		Steps: Sequence{
			{Action: ActionSpec{Type: "a0"}},
			{Action: ActionSpec{Type: "a1"}},
		},
	}
	execs := newFakeExecs()
	runID := "run-race"
	step0 := Execution{
		ID: ExecutionID(runID, stepTag, 0), PolicyID: pol.ID, RunID: runID, StepIndex: 0,
		Status: ExecSucceeded, Event: Event{TenantID: testTenant},
	}
	_ = execs.Enqueue(context.Background(), []Execution{step0})

	pols := newFakePolicies(pol)
	r := NewReconciler(execs, pols, quiet(), ReconcilerConfig{})
	e := NewExecutor(execs, pols, newFakePolicyOwners(), NewRegistry(), nil, quiet(), ExecutorConfig{})

	// Both paths race to advance the same succeeded step concurrently: the
	// sweep via ListStalledRuns->advanceRunSteps, and a direct advanceRun
	// call standing in for a live worker that just recorded step 0's success
	// (the same call MarkResult(succeeded) triggers in attempt()).
	done := make(chan struct{}, 2)
	go func() { r.RunOnce(context.Background()); done <- struct{}{} }()
	go func() { e.advanceRun(context.Background(), pol, step0); done <- struct{}{} }()
	<-done
	<-done

	run, _ := execs.ListByRun(context.Background(), runID)
	var step1Count int
	for _, x := range run {
		if x.StepIndex == 1 {
			step1Count++
		}
	}
	if step1Count != 1 {
		t.Fatalf("step 1 appears %d times after a racing sweep+advance, want exactly 1", step1Count)
	}
}
