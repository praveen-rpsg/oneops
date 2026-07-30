//go:build integration

package postgres

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/events"
)

// Trust Register audit (2026-07-29): entries 14 and 18 claim the atomic claim and
// claim fencing as eliminated *classes*. They were verified on the delivery and
// policy-execution queues only. `webhook_replay_job` is a third claimed
// resource and was never swept.
//
// It has no `claimed_at` column at all, `ClaimPendingJobs` is a plain
// `SELECT ... WHERE status='pending'`, and the worker then issues a *separate*
// unconditional `UPDATE ... WHERE id=$1` to mark it running. That is verbatim
// the shape ADR-CONCURRENCY-002 eliminated:
//
//	"ClaimDue was a plain SELECT of pending/failed rows with no claim, so two
//	 workers running at once — the overlap window during a leadership handoff or
//	 a lock-loss — both selected the same rows and both performed the outbound
//	 action. Leadership makes that window small; it does not close it."
//
// The replay worker is leader-gated, so this is precisely that bounded overlap.
//
// This test asserts the property entries 14 and 18 claim platform-wide: two
// workers claiming at once never receive the same unit of work.
func TestReplayJobClaim_IsExclusive(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	s := NewWebhookStore(pool)

	suffix := time.Now().UnixNano()
	whID := fmt.Sprintf("wh_replay_%d", suffix)
	if _, err := pool.Exec(ctx, `
		INSERT INTO webhook (id, tenant_id, url, secret, enabled, max_retries)
		VALUES ($1, $2, 'https://replay.invalid/hook', 'shh', true, 5)`,
		whID, domain.SystemTenantID); err != nil {
		t.Fatalf("seed webhook: %v", err)
	}

	const jobs = 8
	for i := 0; i < jobs; i++ {
		j := events.ReplayJob{
			ID: fmt.Sprintf("rj_%d_%d", suffix, i), WebhookID: whID,
			Status: events.JobPending, CreatedAt: time.Now().UTC(),
		}
		if err := s.CreateJob(ctx, j); err != nil {
			t.Fatalf("create job %d: %v", i, err)
		}
	}

	// Two workers claim at the same instant — the leadership-overlap window.
	var wg sync.WaitGroup
	results := make([][]events.ReplayJob, 2)
	start := make(chan struct{})
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			<-start
			got, err := s.ClaimPendingJobs(ctx, jobs)
			if err != nil {
				t.Errorf("worker %d claim: %v", w, err)
				return
			}
			results[w] = got
		}(w)
	}
	close(start)
	wg.Wait()

	seen := map[string]int{}
	for _, batch := range results {
		for _, j := range batch {
			seen[j.ID]++
		}
	}
	var doubled []string
	for id, n := range seen {
		if n > 1 {
			doubled = append(doubled, id)
		}
	}
	t.Logf("replay jobs handed out: %d distinct, %d claimed by BOTH workers",
		len(seen), len(doubled))

	if len(doubled) > 0 {
		t.Errorf("NON-EXCLUSIVE CLAIM: %d replay job(s) were handed to two workers at once "+
			"(e.g. %s). Entries 14/18 record the atomic claim and claim fencing as eliminated "+
			"classes, but this third claimed resource has neither — the class is OPEN",
			len(doubled), doubled[0])
	}
}

// The fencing half of the same audit. With the claim now exclusive, two workers
// can no longer hold the same job through the worker path — but the outcome
// write must still be fenced, so a write carrying a token the job no longer
// bears changes nothing. Without it, any late or replayed write overwrites the
// owner's verdict, which is what happened live: a completed job with 42 events
// replayed became `failed` with 0.
func TestReplayJobUpdate_IsFenced(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	s := NewWebhookStore(pool)

	suffix := time.Now().UnixNano()
	whID := fmt.Sprintf("wh_fence_%d", suffix)
	if _, err := pool.Exec(ctx, `
		INSERT INTO webhook (id, tenant_id, url, secret, enabled, max_retries)
		VALUES ($1, $2, 'https://replay.invalid/hook', 'shh', true, 5)`,
		whID, domain.SystemTenantID); err != nil {
		t.Fatalf("seed webhook: %v", err)
	}
	id := fmt.Sprintf("rj_fence_%d", suffix)
	if err := s.CreateJob(ctx, events.ReplayJob{
		ID: id, WebhookID: whID, Status: events.JobPending, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create job: %v", err)
	}

	claimed, err := s.ClaimPendingJobs(ctx, 10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	var owner events.ReplayJob
	for _, j := range claimed {
		if j.ID == id {
			owner = j
		}
	}
	if owner.ClaimedAt.IsZero() {
		t.Fatal("the claim surfaced no fencing token — a stale write could not be distinguished " +
			"from the owner's (ADR-CONCURRENCY-007)")
	}

	// The owner records its outcome.
	owner.Status, owner.EventsReplayed = events.JobCompleted, 42
	if err := s.UpdateJob(ctx, owner); err != nil {
		t.Fatalf("owner complete: %v", err)
	}

	// A write bearing a token the job no longer carries must change nothing.
	stale := owner
	stale.ClaimedAt = owner.ClaimedAt.Add(-time.Minute)
	stale.Status, stale.EventsReplayed, stale.Error = events.JobFailed, 0, "stale worker's verdict"
	staleErr := s.UpdateJob(ctx, stale)

	got, _, err := s.GetJob(ctx, id)
	if err != nil {
		t.Fatalf("read job: %v", err)
	}
	t.Logf("after the stale write: status=%s events_replayed=%d error=%q (err=%v)",
		got.Status, got.EventsReplayed, got.Error, staleErr)

	if !errors.Is(staleErr, events.ErrStaleClaim) {
		t.Errorf("a write with a stale token was accepted (err=%v, want ErrStaleClaim)", staleErr)
	}
	if got.Status != events.JobCompleted || got.EventsReplayed != 42 {
		t.Errorf("UNFENCED COMPLETION: a stale write overwrote the owner's outcome "+
			"(status=%s events_replayed=%d error=%q) — entry 18's class is open on this "+
			"resource", got.Status, got.EventsReplayed, got.Error)
	}
}

// Audit of entry 20's class (dead-letter liveness) on this third resource: a job
// whose worker died while running is never reclaimed, because ClaimPendingJobs
// selects only `pending`. It is not retried and not terminated — it is stuck.
//
// This is recorded rather than asserted as a failure: unlike the delivery queue,
// a stuck replay job produces no repeated outbound effect, so it is a liveness
// gap, not a correctness one. It is stated in the Trust Register as a residual.
func TestReplayJob_StuckRunningIsNotReclaimed(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	s := NewWebhookStore(pool)

	suffix := time.Now().UnixNano()
	whID := fmt.Sprintf("wh_stuck_%d", suffix)
	if _, err := pool.Exec(ctx, `
		INSERT INTO webhook (id, tenant_id, url, secret, enabled, max_retries)
		VALUES ($1, $2, 'https://replay.invalid/hook', 'shh', true, 5)`,
		whID, domain.SystemTenantID); err != nil {
		t.Fatalf("seed webhook: %v", err)
	}
	id := fmt.Sprintf("rj_stuck_%d", suffix)
	if err := s.CreateJob(ctx, events.ReplayJob{
		ID: id, WebhookID: whID, Status: events.JobPending, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := s.ClaimPendingJobs(ctx, 10); err != nil {
		t.Fatalf("claim: %v", err)
	}
	// The worker dies here. Age the claim well beyond any plausible lease.
	if _, err := pool.Exec(ctx,
		`UPDATE webhook_replay_job SET claimed_at = now() - interval '24 hours' WHERE id=$1`, id); err != nil {
		t.Fatalf("age claim: %v", err)
	}

	again, err := s.ClaimPendingJobs(ctx, 10)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	reclaimed := false
	for _, j := range again {
		if j.ID == id {
			reclaimed = true
		}
	}
	got, _, _ := s.GetJob(ctx, id)
	t.Logf("a replay job abandoned 24h ago: reclaimed=%v status=%s — no lease recovery exists "+
		"for this queue, so it is neither retried nor terminated (recorded as a residual)",
		reclaimed, got.Status)
}
