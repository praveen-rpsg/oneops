//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/events"
)

// TestWebhookStore_Integration exercises the webhook registry, delivery queue,
// dead-letter recovery, relay cursor, and replay-job persistence against a real
// PostgreSQL: text[] arrays, batch inserts, ANY() filters, and status transitions.
func TestWebhookStore_Integration(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `TRUNCATE webhook, webhook_delivery, webhook_cursor, webhook_replay_job CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	s := NewWebhookStore(pool)

	// --- registry + text[] round-trip ----------------------------------------
	wh := events.Webhook{
		ID: "wh_1", URL: "https://sub/hook", Secret: "s3cr3t", Enabled: true,
		Operations: []string{"ratification", "approval"}, Resources: []string{"c1"}, MaxRetries: 3,
	}
	if err := s.Create(ctx, wh); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.Get(ctx, "wh_1")
	if err != nil || len(got.Operations) != 2 || got.Operations[0] != "ratification" || len(got.Resources) != 1 {
		t.Fatalf("array round-trip failed: %+v %v", got, err)
	}
	if en, err := s.ListEnabled(ctx); err != nil || len(en) != 1 {
		t.Fatalf("ListEnabled: %d %v", len(en), err)
	}
	// Secret rotation via Update.
	wh.Secret = "rotated"
	if err := s.Update(ctx, wh); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got, _ := s.Get(ctx, "wh_1"); got.Secret != "rotated" {
		t.Fatalf("secret not rotated: %q", got.Secret)
	}

	// --- delivery batch + claim + mark ----------------------------------------
	now := time.Now().UTC()
	ev := events.Event{ChainID: "c1", Seq: 1, EventID: "evt_1", OperationID: "op_1", Operation: "ratification", Actor: "u", CfgID: "c1", OccurredAt: now}
	if err := s.Enqueue(ctx, []events.Delivery{
		{ID: "d1", WebhookID: "wh_1", Event: ev, Status: events.StatusPending, NextAttemptAt: now.Add(-time.Minute)},
		{ID: "d2", WebhookID: "wh_1", Event: ev, Status: events.StatusPending, NextAttemptAt: now.Add(-time.Minute)},
	}); err != nil {
		t.Fatalf("Enqueue batch: %v", err)
	}
	// Idempotent re-enqueue (ON CONFLICT DO NOTHING).
	if err := s.Enqueue(ctx, []events.Delivery{{ID: "d1", WebhookID: "wh_1", Event: ev, Status: events.StatusPending, NextAttemptAt: now}}); err != nil {
		t.Fatalf("Enqueue idempotent: %v", err)
	}
	due, err := s.ClaimDue(ctx, now, 10)
	if err != nil || len(due) != 2 {
		t.Fatalf("ClaimDue: %d %v", len(due), err)
	}
	if err := s.MarkResult(ctx, "d1", events.StatusDelivered, 0, 200, now, time.Time{}); err != nil {
		t.Fatalf("MarkResult: %v", err)
	}
	if d, ok, _ := s.GetDelivery(ctx, "d1"); !ok || d.Status != events.StatusDelivered || d.LastStatusCode != 200 {
		t.Fatalf("GetDelivery: %+v ok=%v", d, ok)
	}
	if list, err := s.ListByWebhook(ctx, "wh_1", 10); err != nil || len(list) != 2 {
		t.Fatalf("ListByWebhook: %d %v", len(list), err)
	}

	// --- dead-letter recovery -------------------------------------------------
	if err := s.MarkResult(ctx, "d2", events.StatusDeadLetter, 3, 500, now, time.Time{}); err != nil {
		t.Fatalf("dead-letter: %v", err)
	}
	if dl, err := s.ListDeadLetters(ctx, "wh_1", 10); err != nil || len(dl) != 1 {
		t.Fatalf("ListDeadLetters: %d %v", len(dl), err)
	}
	if n, err := s.RequeueDeadLetters(ctx, "wh_1"); err != nil || n != 1 {
		t.Fatalf("RequeueDeadLetters: %d %v", n, err)
	}
	if d, _, _ := s.GetDelivery(ctx, "d2"); d.Status != events.StatusPending {
		t.Fatalf("requeue did not reset status: %q", d.Status)
	}
	if n, err := s.Requeue(ctx, []string{"d1"}); err != nil || n != 1 {
		t.Fatalf("Requeue: %d %v", n, err)
	}
	if c, err := s.CountByStatus(ctx, events.StatusPending); err != nil || c != 2 {
		t.Fatalf("CountByStatus: %d %v", c, err)
	}

	// --- retention delete (terminal only) -------------------------------------
	if err := s.MarkResult(ctx, "d1", events.StatusDelivered, 0, 200, now.Add(-72*time.Hour), time.Time{}); err != nil {
		t.Fatalf("mark old: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE webhook_delivery SET created_at=$1 WHERE id='d1'`, now.Add(-72*time.Hour)); err != nil {
		t.Fatalf("age: %v", err)
	}
	if n, err := s.DeleteOlderThan(ctx, now.Add(-24*time.Hour), []events.DeliveryStatus{events.StatusDelivered}); err != nil || n != 1 {
		t.Fatalf("DeleteOlderThan: %d %v", n, err)
	}

	// --- relay cursor ---------------------------------------------------------
	if err := s.SetCursor(ctx, "c1", 5); err != nil {
		t.Fatalf("SetCursor: %v", err)
	}
	if seq, err := s.GetCursor(ctx, "c1"); err != nil || seq != 5 {
		t.Fatalf("GetCursor: %d %v", seq, err)
	}

	// --- replay-job persistence -----------------------------------------------
	job := events.ReplayJob{ID: "rpl_1", WebhookID: "wh_1", From: now.Add(-time.Hour), To: now, Status: events.JobPending}
	if err := s.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if pend, err := s.ClaimPendingJobs(ctx, 10); err != nil || len(pend) != 1 {
		t.Fatalf("ClaimPendingJobs: %d %v", len(pend), err)
	}
	job.Status = events.JobCompleted
	job.EventsReplayed = 7
	if err := s.UpdateJob(ctx, job); err != nil {
		t.Fatalf("UpdateJob: %v", err)
	}
	if g, ok, _ := s.GetJob(ctx, "rpl_1"); !ok || g.Status != events.JobCompleted || g.EventsReplayed != 7 {
		t.Fatalf("GetJob: %+v ok=%v", g, ok)
	}
	if jobs, err := s.ListJobs(ctx, 10); err != nil || len(jobs) != 1 {
		t.Fatalf("ListJobs: %d %v", len(jobs), err)
	}

	// --- delete webhook -------------------------------------------------------
	if err := s.Delete(ctx, "wh_1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}
