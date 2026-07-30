//go:build integration

package postgres

import (
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/events"
)

// TestWebhookStore_Integration exercises the webhook registry, delivery queue,
// dead-letter recovery, relay cursor, and replay-job persistence against a real
// PostgreSQL: text[] arrays, batch inserts, ANY() filters, and status transitions.
func TestWebhookStore_Integration(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
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
	ev := events.Event{TenantID: domain.SystemTenantID, ChainID: "c1", Seq: 1, EventID: "evt_1", OperationID: "op_1", Operation: "ratification", Actor: "u", CfgID: "c1", OccurredAt: now}
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
	due, err := s.ClaimDue(ctx, now, time.Minute, 10)
	if err != nil || len(due) != 2 {
		t.Fatalf("ClaimDue: %d %v", len(due), err)
	}
	if err := s.MarkResult(ctx, "d1", time.Time{}, events.StatusDelivered, 0, 200, now, time.Time{}, events.AttemptFacts{Destination: "https://d1.invalid/hook", SignedTS: 1700000000}); err != nil {
		t.Fatalf("MarkResult: %v", err)
	}
	if d, ok, _ := s.GetDelivery(ctx, "d1"); !ok || d.Status != events.StatusDelivered || d.LastStatusCode != 200 {
		t.Fatalf("GetDelivery: %+v ok=%v", d, ok)
	}
	if list, err := s.ListByWebhook(ctx, "wh_1", 10); err != nil || len(list) != 2 {
		t.Fatalf("ListByWebhook: %d %v", len(list), err)
	}

	// --- dead-letter recovery -------------------------------------------------
	if err := s.MarkResult(ctx, "d2", time.Time{}, events.StatusDeadLetter, 3, 500, now, time.Time{}, events.AttemptFacts{Destination: "https://d2.invalid/hook", SignedTS: 1700000000}); err != nil {
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
	if n, err := s.Requeue(ctx, "wh_1", []string{"d1"}); err != nil || n != 1 {
		t.Fatalf("Requeue: %d %v", n, err)
	}
	if c, err := s.CountByStatus(ctx, events.StatusPending); err != nil || c != 2 {
		t.Fatalf("CountByStatus: %d %v", c, err)
	}

	// --- retention delete (terminal only) -------------------------------------
	if err := s.MarkResult(ctx, "d1", time.Time{}, events.StatusDelivered, 0, 200, now.Add(-72*time.Hour), time.Time{}, events.AttemptFacts{Destination: "https://recorded.invalid/hook", SignedTS: 1700000000}); err != nil {
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

// TestWebhookStore_NilSlicesPersist is the regression test for the nil-slice
// defect shipped in v1.0.0: pgx encodes a NIL Go slice as SQL NULL, and an
// explicit NULL bypasses `DEFAULT '{}'`, so every `text[] NOT NULL` insert bound
// to a nil slice failed with SQLSTATE 23502.
//
// Nil is the NORMAL case at each of these call sites, which is exactly why the
// defect reached production:
//   - a webhook with no Operations/Resources means "subscribe to all events"
//   - a replay job with no DeliveryIDs means time-window mode
//
// Every prior test supplied populated slices, so none of them caught it.
func TestWebhookStore_NilSlicesPersist(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	if _, err := pool.Exec(ctx, `TRUNCATE webhook, webhook_delivery, webhook_cursor, webhook_replay_job CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	s := NewWebhookStore(pool)

	// Create with nil Operations AND nil Resources — "all events".
	wh := events.Webhook{
		ID: "wh_nil", URL: "https://sub/all", Secret: "s", Enabled: true,
		Operations: nil, Resources: nil, MaxRetries: 3,
	}
	if err := s.Create(ctx, wh); err != nil {
		t.Fatalf("Create with nil slices: %v", err)
	}
	got, err := s.Get(ctx, "wh_nil")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Operations) != 0 || len(got.Resources) != 0 {
		t.Fatalf("nil slices round-tripped as %+v, want empty", got)
	}
	// The semantic contract survives: empty means "match everything".
	if !got.Matches(events.Event{TenantID: domain.SystemTenantID, Operation: "ratification", CfgID: "any"}) {
		t.Error("a webhook with no filters must match every event")
	}

	// Update back to nil after having been populated.
	wh.Operations, wh.Resources = []string{"ratification"}, []string{"c1"}
	if err := s.Update(ctx, wh); err != nil {
		t.Fatalf("Update populated: %v", err)
	}
	wh.Operations, wh.Resources = nil, nil
	if err := s.Update(ctx, wh); err != nil {
		t.Fatalf("Update back to nil slices: %v", err)
	}

	// Replay job with nil DeliveryIDs — time-window mode.
	job := events.ReplayJob{
		ID: "rpl_nil", WebhookID: "wh_nil",
		From: time.Now().Add(-time.Hour), To: time.Now(),
		DeliveryIDs: nil, Status: events.JobPending,
	}
	if err := s.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob with nil DeliveryIDs: %v", err)
	}
	gotJob, ok, err := s.GetJob(ctx, "rpl_nil")
	if err != nil || !ok {
		t.Fatalf("GetJob: %v ok=%v", err, ok)
	}
	if len(gotJob.DeliveryIDs) != 0 {
		t.Fatalf("DeliveryIDs = %v, want empty", gotJob.DeliveryIDs)
	}
}
