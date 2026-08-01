package policy

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

// callLog records the order and count of action runs, so a test can prove
// exactly which steps of a composed Sequence ran — the property a double-run
// or a skipped-gate bug would violate.
type callLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *callLog) record(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, name)
}

func (l *callLog) count(name string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, c := range l.calls {
		if c == name {
			n++
		}
	}
	return n
}

func (l *callLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.calls))
	copy(out, l.calls)
	return out
}

// namedAction is an Action that records its own name every time it runs, and
// optionally fails.
type namedAction struct {
	name string
	log  *callLog
	fail bool
}

var errBoom = errors.New("boom")

func (a namedAction) Run(_ context.Context, _ Event, _ json.RawMessage) error {
	a.log.record(a.name)
	if a.fail {
		return errBoom
	}
	return nil
}

// ---- start: a composed policy's match mints a run and enqueues step 0 only --

func TestConsumer_Evaluate_ComposedPolicy_StartsRunAtStepZeroOnly(t *testing.T) {
	pol := Policy{
		ID: "p-run", TenantID: testTenant, Enabled: true, MaxRetries: 3,
		Condition: Condition{Operations: []string{"ratification"}},
		Steps: Sequence{
			{Action: ActionSpec{Type: "a0"}},
			{Action: ActionSpec{Type: "a1"}},
		},
	}
	c := NewConsumer(&fakeSource{}, newFakeCursor(), newFakePolicies(pol), newFakeExecs(), nil, quiet(), ConsumerConfig{})
	ev := Event{TenantID: testTenant, Operation: "ratification", CfgID: "c1", Seq: 1}

	execs := c.Evaluate(ev, []Policy{pol}, time.Unix(0, 0))
	if len(execs) != 1 {
		t.Fatalf("Evaluate produced %d executions for a composed policy, want exactly 1 (step 0 only)", len(execs))
	}
	got := execs[0]
	if got.RunID == "" {
		t.Fatal("a composed policy's execution must carry a RunID")
	}
	if got.StepIndex != 0 {
		t.Errorf("StepIndex = %d, want 0 (only the first step is enqueued at start)", got.StepIndex)
	}

	// Re-evaluating the same event must mint the same run and the same step-0
	// id — a re-processed event (crash between enqueue and cursor advance)
	// must not start a second run (ADR-CONCURRENCY-003's shape, applied to runs).
	again := c.Evaluate(ev, []Policy{pol}, time.Unix(0, 0))
	if again[0].RunID != got.RunID || again[0].ID != got.ID {
		t.Fatalf("re-evaluating the same event minted a different run/id: %+v vs %+v", again[0], got)
	}
}

// ---- advance: a 2-step ungated sequence runs step0 -> step1 -> complete -----

func TestExecutor_ComposedRun_TwoStepUngated_AdvancesToCompletion(t *testing.T) {
	log := &callLog{}
	reg := NewRegistry()
	reg.Register("a0", namedAction{name: "a0", log: log})
	reg.Register("a1", namedAction{name: "a1", log: log})

	pol := Policy{
		ID: "p-run", TenantID: testTenant, Enabled: true, MaxRetries: 3,
		Steps: Sequence{
			{Action: ActionSpec{Type: "a0"}},
			{Action: ActionSpec{Type: "a1"}},
		},
	}
	pols := newFakePolicies(pol)
	execs := newFakeExecs()
	c := NewConsumer(&fakeSource{}, newFakeCursor(), pols, execs, nil, quiet(), ConsumerConfig{})
	ev := Event{TenantID: testTenant, CfgID: "c1", Seq: 1}
	start := c.Evaluate(ev, []Policy{pol}, time.Now().UTC())
	if err := execs.Enqueue(context.Background(), start); err != nil {
		t.Fatalf("enqueue run start: %v", err)
	}
	runID := start[0].RunID

	e := NewExecutor(execs, pols, newFakePolicyOwners(), reg, nil, quiet(), ExecutorConfig{})

	// Pass 1: claims and runs step 0, which advances and enqueues step 1.
	e.RunOnce(context.Background())
	if got := log.all(); len(got) != 1 || got[0] != "a0" {
		t.Fatalf("after pass 1, calls = %v, want [a0]", got)
	}
	run, _ := execs.ListByRun(context.Background(), runID)
	if len(run) != 2 {
		t.Fatalf("after pass 1, run has %d executions, want 2 (step 0 succeeded, step 1 enqueued)", len(run))
	}
	if run[0].Status != ExecSucceeded {
		t.Fatalf("step 0 status = %q, want succeeded", run[0].Status)
	}
	if run[1].Status != ExecPending || run[1].StepIndex != 1 {
		t.Fatalf("step 1 = %+v, want pending at StepIndex 1", run[1])
	}

	// Pass 2: claims and runs step 1, which completes the run (no further enqueue).
	e.RunOnce(context.Background())
	if got := log.all(); len(got) != 2 || got[1] != "a1" {
		t.Fatalf("after pass 2, calls = %v, want [a0 a1]", got)
	}
	run, _ = execs.ListByRun(context.Background(), runID)
	if len(run) != 2 {
		t.Fatalf("after pass 2, run has %d executions, want 2 (no third step exists)", len(run))
	}
	rp := RunProgress{RunID: runID, Seq: pol.Steps, Steps: run}
	if !rp.Complete() {
		t.Error("a run whose two steps both succeeded must report Complete")
	}

	// Neither action ran more than once across the whole run.
	if n := log.count("a0"); n != 1 {
		t.Errorf("a0 ran %d times, want exactly 1", n)
	}
	if n := log.count("a1"); n != 1 {
		t.Errorf("a1 ran %d times, want exactly 1", n)
	}

	// A further pass is a no-op: nothing left to claim, nothing new enqueued.
	e.RunOnce(context.Background())
	if got := log.all(); len(got) != 2 {
		t.Fatalf("an extra pass over a completed run ran actions again: %v", got)
	}
}

// ---- pause: a middle gate stops the run at the gated step, never enqueues later steps

func TestExecutor_ComposedRun_MiddleGate_PausesAndNeverEnqueuesLaterStep(t *testing.T) {
	log := &callLog{}
	reg := NewRegistry()
	reg.Register("a0", namedAction{name: "a0", log: log})
	reg.Register("a1", namedAction{name: "a1", log: log})
	reg.Register("a2", namedAction{name: "a2", log: log})

	pol := Policy{
		ID: "p-gate", TenantID: testTenant, Enabled: true, MaxRetries: 3,
		Steps: Sequence{
			{Action: ActionSpec{Type: "a0"}},
			{Action: ActionSpec{Type: "a1"}, Gate: &GateSpec{Type: "approval"}},
			{Action: ActionSpec{Type: "a2"}},
		},
	}
	pols := newFakePolicies(pol)
	execs := newFakeExecs()
	c := NewConsumer(&fakeSource{}, newFakeCursor(), pols, execs, nil, quiet(), ConsumerConfig{})
	ev := Event{TenantID: testTenant, CfgID: "c1", Seq: 1}
	start := c.Evaluate(ev, []Policy{pol}, time.Now().UTC())
	_ = execs.Enqueue(context.Background(), start)
	runID := start[0].RunID

	e := NewExecutor(execs, pols, newFakePolicyOwners(), reg, nil, quiet(), ExecutorConfig{})

	e.RunOnce(context.Background()) // step 0 succeeds, ungated -> enqueues step 1
	e.RunOnce(context.Background()) // step 1 (gated) succeeds -> pauses, does not enqueue step 2
	e.RunOnce(context.Background()) // extra pass: nothing new to claim

	if got := log.all(); len(got) != 2 || got[0] != "a0" || got[1] != "a1" {
		t.Fatalf("calls = %v, want exactly [a0 a1] — the gated step's action ran once, the step past the gate never ran", got)
	}
	if n := log.count("a2"); n != 0 {
		t.Fatalf("a2 ran %d times, want 0 — a step past an unresolved gate must never be enqueued or run", n)
	}

	run, _ := execs.ListByRun(context.Background(), runID)
	if len(run) != 2 {
		t.Fatalf("run has %d executions, want 2 (step 2 must never be enqueued while the gate is pending)", len(run))
	}
	if run[1].Status != ExecSucceeded {
		t.Fatalf("gated step status = %q, want succeeded (the action ran; only progression past it is paused)", run[1].Status)
	}
	if run[1].Gate != GatePending {
		t.Fatalf("gated step Gate = %q, want %q", run[1].Gate, GatePending)
	}

	rp := RunProgress{RunID: runID, Seq: pol.Steps, Steps: run}
	if rp.Complete() {
		t.Error("a run paused on a pending gate must not report Complete")
	}
	if got := rp.NextStepIndex(); got != 1 {
		t.Errorf("NextStepIndex = %d, want 1 (paused at the gated step)", got)
	}
}

// ---- failure: a step's failure does not advance the run ---------------------

func TestExecutor_ComposedRun_StepFailure_DoesNotAdvance(t *testing.T) {
	log := &callLog{}
	reg := NewRegistry()
	reg.Register("a0", namedAction{name: "a0", log: log, fail: true})
	reg.Register("a1", namedAction{name: "a1", log: log})

	pol := Policy{
		ID: "p-fail", TenantID: testTenant, Enabled: true, MaxRetries: 3,
		Steps: Sequence{
			{Action: ActionSpec{Type: "a0"}},
			{Action: ActionSpec{Type: "a1"}},
		},
	}
	pols := newFakePolicies(pol)
	execs := newFakeExecs()
	c := NewConsumer(&fakeSource{}, newFakeCursor(), pols, execs, nil, quiet(), ConsumerConfig{})
	ev := Event{TenantID: testTenant, CfgID: "c1", Seq: 1}
	start := c.Evaluate(ev, []Policy{pol}, time.Now().UTC())
	_ = execs.Enqueue(context.Background(), start)
	runID := start[0].RunID

	e := NewExecutor(execs, pols, newFakePolicyOwners(), reg, nil, quiet(), ExecutorConfig{BaseBackoff: time.Hour})

	e.RunOnce(context.Background())

	if n := log.count("a1"); n != 0 {
		t.Fatalf("a1 ran %d times, want 0 — a failed step must never advance the run", n)
	}
	run, _ := execs.ListByRun(context.Background(), runID)
	if len(run) != 1 {
		t.Fatalf("run has %d executions, want 1 (only the failed step 0, never step 1)", len(run))
	}
	if run[0].Status != ExecFailed {
		t.Fatalf("step 0 status = %q, want failed", run[0].Status)
	}
}

// ---- idempotency: advancing the same succeeded step twice must not double-enqueue or double-run

func TestExecutor_ComposedRun_DuplicateAdvance_DoesNotDoubleEnqueueOrDoubleRun(t *testing.T) {
	log := &callLog{}
	reg := NewRegistry()
	reg.Register("a1", namedAction{name: "a1", log: log})

	pol := Policy{
		ID: "p-dup", TenantID: testTenant, Enabled: true, MaxRetries: 3,
		Steps: Sequence{
			{Action: ActionSpec{Type: "a0"}},
			{Action: ActionSpec{Type: "a1"}},
		},
	}
	execs := newFakeExecs()
	runID := "run-dup"
	step0 := Execution{
		ID: ExecutionID(runID, stepTag, 0), PolicyID: pol.ID, RunID: runID, StepIndex: 0,
		Status: ExecSucceeded, Event: Event{TenantID: testTenant},
	}
	_ = execs.Enqueue(context.Background(), []Execution{step0})

	e := NewExecutor(execs, newFakePolicies(pol), newFakePolicyOwners(), reg, nil, quiet(), ExecutorConfig{})

	// Call advanceRun twice for the same succeeded step, as a duplicated
	// advance (e.g. an at-least-once redelivery) would.
	e.advanceRun(context.Background(), pol, step0)
	e.advanceRun(context.Background(), pol, step0)

	run, _ := execs.ListByRun(context.Background(), runID)
	if len(run) != 2 {
		t.Fatalf("run has %d executions after a duplicated advance, want exactly 2 — the second advance must be a no-op", len(run))
	}
	var step1Count int
	for _, x := range run {
		if x.StepIndex == 1 {
			step1Count++
		}
	}
	if step1Count != 1 {
		t.Fatalf("step 1 appears %d times, want exactly 1 (deterministic id + ON CONFLICT DO NOTHING must coalesce the duplicate)", step1Count)
	}

	// Running the (single, deduplicated) step 1 must execute its action exactly once.
	e.RunOnce(context.Background())
	if n := log.count("a1"); n != 1 {
		t.Fatalf("a1 ran %d times, want exactly 1 despite the duplicated advance", n)
	}
}
