//go:build integration

package postgres

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/policy"
)

// TestPolicyStore_Integration exercises the policy registry and execution history
// against a real PostgreSQL: jsonb condition/action/event columns, batch inserts,
// due-claim ordering, and status transitions.
func TestPolicyStore_Integration(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	if _, err := pool.Exec(ctx, `TRUNCATE policy, policy_execution, policy_cursor CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	s := NewPolicyStore(pool)

	// --- registry with jsonb condition/action ---------------------------------
	p := policy.Policy{
		ID: "pol_1", Name: "notify", Enabled: true, MaxRetries: 3,
		Condition: policy.Condition{Operations: []string{"ratification"}, Metadata: map[string]string{"new_lifecycle": "ratified"}},
		Action:    policy.ActionSpec{Type: "http", Config: json.RawMessage(`{"url":"https://x"}`)},
	}
	if err := s.Create(ctx, p); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.Get(ctx, "pol_1")
	if err != nil || len(got.Condition.Operations) != 1 || got.Condition.Metadata["new_lifecycle"] != "ratified" || got.Action.Type != "http" {
		t.Fatalf("jsonb round-trip failed: %+v %v", got, err)
	}
	if en, err := s.ListEnabled(ctx); err != nil || len(en) != 1 {
		t.Fatalf("ListEnabled: %d %v", len(en), err)
	}
	p.Enabled = false
	if err := s.Update(ctx, p); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if en, _ := s.ListEnabled(ctx); len(en) != 0 {
		t.Fatalf("disable not persisted: %d", len(en))
	}
	p.Enabled = true
	_ = s.Update(ctx, p)

	// --- execution history (event jsonb) --------------------------------------
	now := time.Now().UTC()
	ev := policy.Event{EventID: "evt_1", OperationID: "op_1", Operation: "ratification", Actor: "u", CfgID: "c1", Seq: 1, OccurredAt: now, Metadata: map[string]string{"new_lifecycle": "ratified"}}
	if err := s.Enqueue(ctx, []policy.Execution{
		{ID: "pex_1", PolicyID: "pol_1", Event: ev, Status: policy.ExecPending, NextAttemptAt: now.Add(-time.Minute)},
		{ID: "pex_2", PolicyID: "pol_1", Event: ev, Status: policy.ExecPending, NextAttemptAt: now.Add(-time.Minute)},
	}); err != nil {
		t.Fatalf("Enqueue batch: %v", err)
	}
	due, err := s.ClaimDue(ctx, now, time.Minute, 10)
	if err != nil || len(due) != 2 {
		t.Fatalf("ClaimDue: %d %v", len(due), err)
	}
	if due[0].Event.EventID != "evt_1" || due[0].Event.Metadata["new_lifecycle"] != "ratified" {
		t.Fatalf("event jsonb not restored: %+v", due[0].Event)
	}
	if err := s.MarkResult(ctx, "pex_1", time.Time{}, policy.ExecSucceeded, 0, "", now, now, time.Time{}); err != nil {
		t.Fatalf("MarkResult: %v", err)
	}
	if err := s.MarkResult(ctx, "pex_2", time.Time{}, policy.ExecDeadLetter, 3, "boom", now, now, time.Time{}); err != nil {
		t.Fatalf("MarkResult dl: %v", err)
	}
	hist, err := s.ListByPolicy(ctx, "pol_1", 10)
	if err != nil || len(hist) != 2 {
		t.Fatalf("ListByPolicy: %d %v", len(hist), err)
	}

	// --- consumer cursor ------------------------------------------------------
	if err := s.SetPolicyCursor(ctx, "c1", 9); err != nil {
		t.Fatalf("SetPolicyCursor: %v", err)
	}
	if seq, err := s.GetPolicyCursor(ctx, "c1"); err != nil || seq != 9 {
		t.Fatalf("GetPolicyCursor: %d %v", seq, err)
	}

	if err := s.Delete(ctx, "pol_1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

// TestPolicyStore_CompositionRoundTrip proves the ADR-POLICY-001 composition
// model round-trips through real PostgreSQL: a policy's Sequence (steps
// jsonb), and a composed run's Executions (run_id/step_index/gate),
// retrievable in step order via ListByRun.
func TestPolicyStore_CompositionRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	if _, err := pool.Exec(ctx, `TRUNCATE policy, policy_execution, policy_cursor CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	s := NewPolicyStore(pool)

	// --- a policy with NO steps still round-trips exactly as before ------------
	single := policy.Policy{
		ID: "pol_single", Name: "single-action", Enabled: true, MaxRetries: 1,
		Action: policy.ActionSpec{Type: "notification"},
	}
	if err := s.Create(ctx, single); err != nil {
		t.Fatalf("create single-action policy: %v", err)
	}
	got, err := s.Get(ctx, "pol_single")
	if err != nil || len(got.Steps) != 0 || got.Action.Type != "notification" {
		t.Fatalf("single-action policy must round-trip with no steps: %+v %v", got, err)
	}

	// --- a composed policy persists its Sequence --------------------------------
	seq := policy.Sequence{
		{Action: policy.ActionSpec{Type: "http", Config: json.RawMessage(`{"url":"https://x"}`)}},
		{Action: policy.ActionSpec{Type: "notification"}, Gate: &policy.GateSpec{Type: "approval"}},
	}
	composed := policy.Policy{
		ID: "pol_composed", Name: "composed", Enabled: true, MaxRetries: 3, Steps: seq,
	}
	if err := s.Create(ctx, composed); err != nil {
		t.Fatalf("create composed policy: %v", err)
	}
	got, err = s.Get(ctx, "pol_composed")
	if err != nil {
		t.Fatalf("get composed policy: %v", err)
	}
	if len(got.Steps) != 2 || got.Steps[0].Action.Type != "http" ||
		got.Steps[1].Gate == nil || got.Steps[1].Gate.Type != "approval" {
		t.Fatalf("steps jsonb did not round-trip: %+v", got.Steps)
	}

	// --- an updated Sequence persists too ---------------------------------------
	composed.Steps = append(composed.Steps, policy.ActionStep{Action: policy.ActionSpec{Type: "command"}})
	if err := s.Update(ctx, composed); err != nil {
		t.Fatalf("update composed policy: %v", err)
	}
	if got, err = s.Get(ctx, "pol_composed"); err != nil || len(got.Steps) != 3 {
		t.Fatalf("updated steps did not persist: %+v %v", got.Steps, err)
	}

	// --- a run's steps are Executions sharing a RunID, in step order ------------
	now := time.Now().UTC()
	ev := policy.Event{EventID: "evt_run", Operation: "ratification", CfgID: "c1", Seq: 1, OccurredAt: now}
	if err := s.Enqueue(ctx, []policy.Execution{
		{ID: "run1_step1", PolicyID: "pol_composed", Event: ev, Status: policy.ExecPending,
			NextAttemptAt: now, RunID: "run1", StepIndex: 1},
		{ID: "run1_step0", PolicyID: "pol_composed", Event: ev, Status: policy.ExecPending,
			NextAttemptAt: now, RunID: "run1", StepIndex: 0},
	}); err != nil {
		t.Fatalf("enqueue run steps: %v", err)
	}
	if err := s.MarkResult(ctx, "run1_step0", time.Time{}, policy.ExecSucceeded, 0, "", now, now, time.Time{}); err != nil {
		t.Fatalf("mark step 0 succeeded: %v", err)
	}
	run, err := s.ListByRun(ctx, "run1")
	if err != nil {
		t.Fatalf("ListByRun: %v", err)
	}
	if len(run) != 2 || run[0].StepIndex != 0 || run[1].StepIndex != 1 {
		t.Fatalf("ListByRun must return the run's Executions ordered by step index: %+v", run)
	}
	if run[0].Status != policy.ExecSucceeded {
		t.Fatalf("step 0 status not persisted: %+v", run[0])
	}

	// A single-action execution carries no RunID and does not appear in any run.
	if err := s.Enqueue(ctx, []policy.Execution{
		{ID: "solo", PolicyID: "pol_single", Event: ev, Status: policy.ExecPending, NextAttemptAt: now},
	}); err != nil {
		t.Fatalf("enqueue solo execution: %v", err)
	}
	soloRun, err := s.ListByRun(ctx, "")
	if err != nil {
		t.Fatalf("ListByRun(\"\"): %v", err)
	}
	if len(soloRun) != 0 {
		t.Fatalf("a single-action execution's NULL run_id must never match ListByRun(\"\"): got %+v", soloRun)
	}

	// --- SetGate: the executor's W2 pause-at-gate write --------------------------
	if err := s.SetGate(ctx, "run1_step0", policy.GatePending); err != nil {
		t.Fatalf("SetGate pending: %v", err)
	}
	run, err = s.ListByRun(ctx, "run1")
	if err != nil || len(run) != 2 || run[0].Gate != policy.GatePending {
		t.Fatalf("gate not persisted: %+v %v", run, err)
	}
	if err := s.SetGate(ctx, "run1_step0", policy.GatePassed); err != nil {
		t.Fatalf("SetGate passed: %v", err)
	}
	if run, err = s.ListByRun(ctx, "run1"); err != nil || run[0].Gate != policy.GatePassed {
		t.Fatalf("gate resolution not persisted: %+v %v", run, err)
	}
	if err := s.SetGate(ctx, "does-not-exist", policy.GatePending); err != domain.ErrNotFound {
		t.Fatalf("SetGate on a missing row = %v, want domain.ErrNotFound", err)
	}
}
