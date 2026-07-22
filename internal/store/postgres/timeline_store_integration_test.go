//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/audit"
	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/events"
	"github.com/rpsg/oneops/internal/policy"
)

// TestTimelineStore_Integration seeds a real committed audit event plus a webhook
// delivery and a policy execution correlated by its event id, then verifies the
// read-only timeline queries return them from PostgreSQL (SELECT-only; ANY() filters).
func TestTimelineStore_Integration(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	// audit_event is append-only (a TRUNCATE trigger blocks it), so we isolate via
	// a unique chain id and truncate only the non-audit tables.
	if _, err := pool.Exec(ctx, `TRUNCATE webhook_delivery, policy_execution, webhook_replay_job CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	chainID := fmt.Sprintf("tlc-%d", time.Now().UnixNano())

	// Seed a committed audit event via the real appender (event_id derived).
	auditStore := NewAuditStore(pool)
	in, err := audit.Resolve(domain.EventInput{
		ChainID: chainID, OperationID: "op-" + chainID, Operation: domain.OpRatification,
		Payload: json.RawMessage(`{"new_lifecycle":"ratified"}`),
	}, "user:alice", time.Now().UTC())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	ae, err := NewAuditAppender(pool, auditStore).Append(ctx, in)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	eventID := ae.EventID

	// Seed a webhook delivery and a policy execution correlated by that event id.
	wh := NewWebhookStore(pool)
	if err := wh.Enqueue(ctx, []events.Delivery{{
		ID: "d1", WebhookID: "wh_1", Status: events.StatusDelivered, NextAttemptAt: time.Now(),
		Event: events.Event{ChainID: chainID, Seq: ae.Seq, EventID: eventID, OperationID: "op_1", Operation: "ratification", Actor: "u", CfgID: chainID, OccurredAt: ae.OccurredAt},
	}}); err != nil {
		t.Fatalf("seed delivery: %v", err)
	}
	ps := NewPolicyStore(pool)
	if err := ps.Enqueue(ctx, []policy.Execution{{
		ID: "pex_1", PolicyID: "pol_1", Status: policy.ExecSucceeded, NextAttemptAt: time.Now(),
		Event: policy.Event{EventID: eventID, Operation: "ratification", CfgID: chainID, Seq: ae.Seq},
	}}); err != nil {
		t.Fatalf("seed exec: %v", err)
	}
	if err := wh.CreateJob(ctx, events.ReplayJob{ID: "rpl_1", WebhookID: "wh_1", Status: events.JobCompleted}); err != nil {
		t.Fatalf("seed job: %v", err)
	}

	// Query the read-only timeline source.
	ts := NewTimelineStore(pool)
	if rows, err := ts.AuditByEventID(ctx, eventID); err != nil || len(rows) != 1 || rows[0].Operation != "ratification" {
		t.Fatalf("AuditByEventID: %+v %v", rows, err)
	}
	if rows, err := ts.AuditByChain(ctx, chainID, 10); err != nil || len(rows) != 1 {
		t.Fatalf("AuditByChain: %d %v", len(rows), err)
	}
	if dels, err := ts.DeliveriesByEventIDs(ctx, []string{eventID}); err != nil || len(dels) != 1 || dels[0].Status != "delivered" {
		t.Fatalf("DeliveriesByEventIDs: %+v %v", dels, err)
	}
	if pes, err := ts.PolicyExecutionsByEventIDs(ctx, []string{eventID}); err != nil || len(pes) != 1 || pes[0].Status != "succeeded" {
		t.Fatalf("PolicyExecutionsByEventIDs: %+v %v", pes, err)
	}
	if job, ok, err := ts.ReplayJob(ctx, "rpl_1"); err != nil || !ok || job.Status != "completed" {
		t.Fatalf("ReplayJob: %+v ok=%v %v", job, ok, err)
	}
	if pe, ok, err := ts.PolicyExecution(ctx, "pex_1"); err != nil || !ok || pe.EventID != eventID {
		t.Fatalf("PolicyExecution: %+v ok=%v %v", pe, ok, err)
	}
	if _, ok, _ := ts.PolicyExecution(ctx, "nope"); ok {
		t.Fatal("expected missing execution to be not found")
	}
}
