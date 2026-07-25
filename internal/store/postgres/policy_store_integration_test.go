//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/policy"
)

// TestPolicyStore_Integration exercises the policy registry and execution history
// against a real PostgreSQL: jsonb condition/action/event columns, batch inserts,
// due-claim ordering, and status transitions.
func TestPolicyStore_Integration(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
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
	if err := s.MarkResult(ctx, "pex_1", policy.ExecSucceeded, 0, "", now, now, time.Time{}); err != nil {
		t.Fatalf("MarkResult: %v", err)
	}
	if err := s.MarkResult(ctx, "pex_2", policy.ExecDeadLetter, 3, "boom", now, now, time.Time{}); err != nil {
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
