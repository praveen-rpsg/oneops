//go:build integration

package postgres

import (
	"fmt"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/events"
	"github.com/rpsg/oneops/internal/policy"
)

// Retry accounting must survive a worker that never reports back.
//
// ADR-CONCURRENCY-002 recovers a crashed worker's row by reclaiming it once the
// lease expires. ADR-CONCURRENCY-005 fences the evicted worker so its late write
// cannot corrupt the row. Both are about *safety*. Neither is about *liveness*:
// nothing in either ADR bounds how many times a row may be reclaimed.
//
// The retry budget (webhook.max_retries) is only ever consulted by the worker,
// after an attempt it survived. A row whose attempt kills the worker — OOM, node
// loss, SIGKILL, a crash-looping pod, or a demotion that cancels the context the
// outcome write needs — is reclaimed with retry_count UNCHANGED. The budget
// never depletes, the row never reaches dead_letter, and every reclaim is
// another outbound POST to the subscriber. That is an unbounded poison row: the
// queue has no terminating state for it.
//
// This test asserts the property the platform must have: a delivery is attempted
// at most max_retries times *in total*, counting attempts whose outcome was
// never recorded, and then terminates in dead_letter.
func TestRetryLiveness_WebhookReclaimIsBounded(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	s := NewWebhookStore(pool)

	const (
		lease      = time.Minute
		maxRetries = 3
	)
	suffix := time.Now().UnixNano()
	whID := fmt.Sprintf("wh_poison_%d", suffix)
	id := fmt.Sprintf("dlv_poison_%d", suffix)

	seedWebhook(ctx, t, pool, whID, maxRetries)

	t0 := time.Now().UTC()
	chain := fmt.Sprintf("poison-wh-%d", suffix)
	if err := s.Enqueue(ctx, []events.Delivery{{
		ID: id, WebhookID: whID, Status: events.StatusPending, NextAttemptAt: t0.Add(-time.Second),
		Event: events.Event{
			TenantID: domain.SystemTenantID, ChainID: chain, CfgID: chain,
			Operation: "ratification", Seq: 1, EventID: id,
		},
	}}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Simulate a crash-looping worker: each cycle claims the row and then dies
	// before MarkResult. The next cycle runs a full lease later, so the row is
	// reclaimable. An honest queue stops handing this row out after max_retries
	// attempts; we give it twice that many chances to prove it does not.
	claims := 0
	for cycle := 0; cycle < maxRetries*2; cycle++ {
		now := t0.Add(time.Duration(cycle) * (lease + time.Second))
		got, err := s.ClaimDue(ctx, now, lease, 10)
		if err != nil {
			t.Fatalf("cycle %d: claim: %v", cycle, err)
		}
		for _, d := range got {
			if d.ID == id {
				claims++
			}
		}
		// The worker dies here — no MarkResult, ever.
	}

	var status string
	var retry int
	if err := pool.QueryRow(ctx,
		`SELECT status, retry_count FROM webhook_delivery WHERE id=$1`, id).
		Scan(&status, &retry); err != nil {
		t.Fatalf("read row: %v", err)
	}

	t.Logf("after %d crash cycles: claims_handed_out=%d status=%q retry_count=%d",
		maxRetries*2, claims, status, retry)

	if claims > maxRetries {
		t.Errorf("UNBOUNDED RETRY: the row was handed to a worker %d times with a budget of %d — "+
			"every reclaim is another outbound delivery and the budget never depletes", claims, maxRetries)
	}
	if status != string(events.StatusDeadLetter) {
		t.Errorf("NO TERMINATING STATE: status=%q after exhausting the retry budget (want %q)",
			status, events.StatusDeadLetter)
	}
}

// A delivery whose subscriber no longer exists can never succeed, so it must
// terminate rather than circulate. The claim reads the budget from the webhook
// row; a missing webhook is budget 0 and the row is dead-lettered on the first
// claim, without an outbound attempt. Previously the row was claimed, the worker
// failed to load the webhook, and dead-lettered it a cycle later — same terminal
// state, one wasted claim. This pins the earlier, cheaper termination so an
// orphan can never become an unbounded loop (ADR-CONCURRENCY-006).
func TestRetryLiveness_OrphanedDeliveryTerminates(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	s := NewWebhookStore(pool)

	suffix := time.Now().UnixNano()
	id := fmt.Sprintf("dlv_orphan_%d", suffix)
	chain := fmt.Sprintf("orphan-%d", suffix)

	t0 := time.Now().UTC()
	if err := s.Enqueue(ctx, []events.Delivery{{
		ID: id, WebhookID: fmt.Sprintf("wh_gone_%d", suffix),
		Status: events.StatusPending, NextAttemptAt: t0.Add(-time.Second),
		Event: events.Event{
			TenantID: domain.SystemTenantID, ChainID: chain, CfgID: chain,
			Operation: "ratification", Seq: 1, EventID: id,
		},
	}}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	claimed := 0
	for cycle := 0; cycle < 3; cycle++ {
		got, err := s.ClaimDue(ctx, t0.Add(time.Duration(cycle)*(time.Minute+time.Second)), time.Minute, 10)
		if err != nil {
			t.Fatalf("cycle %d: claim: %v", cycle, err)
		}
		for _, d := range got {
			if d.ID == id {
				claimed++
			}
		}
	}

	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM webhook_delivery WHERE id=$1`, id).Scan(&status); err != nil {
		t.Fatalf("read row: %v", err)
	}
	t.Logf("orphaned delivery: claims_handed_out=%d status=%q", claimed, status)

	if claimed != 0 {
		t.Errorf("an orphaned delivery was handed to a worker %d time(s); it has no subscriber "+
			"and can never succeed", claimed)
	}
	if status != string(events.StatusDeadLetter) {
		t.Errorf("orphaned delivery status=%q, want %q — it must terminate, not circulate",
			status, events.StatusDeadLetter)
	}
}

// A worker stopped between claiming and attempting must give back the attempt
// the claim charged it, or repeated restarts would exhaust the budget of
// deliveries that were never actually sent.
func TestRetryLiveness_ReleasedClaimRefundsTheAttempt(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	s := NewWebhookStore(pool)

	suffix := time.Now().UnixNano()
	whID := fmt.Sprintf("wh_release_%d", suffix)
	id := fmt.Sprintf("dlv_release_%d", suffix)
	chain := fmt.Sprintf("release-%d", suffix)
	seedWebhook(ctx, t, pool, whID, 3)

	t0 := time.Now().UTC()
	if err := s.Enqueue(ctx, []events.Delivery{{
		ID: id, WebhookID: whID, Status: events.StatusPending, NextAttemptAt: t0.Add(-time.Second),
		Event: events.Event{
			TenantID: domain.SystemTenantID, ChainID: chain, CfgID: chain,
			Operation: "ratification", Seq: 1, EventID: id,
		},
	}}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Claim and release, repeatedly — as a pod restart loop would.
	for cycle := 0; cycle < 5; cycle++ {
		got, err := s.ClaimDue(ctx, t0, time.Minute, 10)
		if err != nil || len(got) != 1 {
			t.Fatalf("cycle %d: claim n=%d err=%v", cycle, len(got), err)
		}
		if err := s.ReleaseClaim(ctx, id, got[0].ClaimedAt); err != nil {
			t.Fatalf("cycle %d: release: %v", cycle, err)
		}
	}

	var status string
	var retry int
	if err := pool.QueryRow(ctx,
		`SELECT status, retry_count FROM webhook_delivery WHERE id=$1`, id).Scan(&status, &retry); err != nil {
		t.Fatalf("read row: %v", err)
	}
	t.Logf("after 5 claim/release cycles: status=%q retry_count=%d", status, retry)

	if retry != 0 {
		t.Errorf("retry_count=%d after 5 claim/release cycles — a claim that was never attempted "+
			"must not consume budget", retry)
	}
	if status != string(events.StatusPending) {
		t.Errorf("status=%q after release, want %q — a released row must be immediately claimable",
			status, events.StatusPending)
	}
}

// A release must not be able to refund a claim the worker no longer holds:
// otherwise an evicted worker could hand budget back to (and reset the state of)
// the row its reclaimer now owns — the fencing rule of ADR-CONCURRENCY-005
// applied to the release path.
func TestRetryLiveness_ReleaseIsFencedOnTheClaim(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	s := NewWebhookStore(pool)

	suffix := time.Now().UnixNano()
	whID := fmt.Sprintf("wh_relfence_%d", suffix)
	id := fmt.Sprintf("dlv_relfence_%d", suffix)
	chain := fmt.Sprintf("relfence-%d", suffix)
	seedWebhook(ctx, t, pool, whID, 10)

	t0 := time.Now().UTC()
	if err := s.Enqueue(ctx, []events.Delivery{{
		ID: id, WebhookID: whID, Status: events.StatusPending, NextAttemptAt: t0.Add(-time.Second),
		Event: events.Event{
			TenantID: domain.SystemTenantID, ChainID: chain, CfgID: chain,
			Operation: "ratification", Seq: 1, EventID: id,
		},
	}}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	const lease = time.Minute
	w1, err := s.ClaimDue(ctx, t0, lease, 1)
	if err != nil || len(w1) != 1 {
		t.Fatalf("W1 claim: n=%d err=%v", len(w1), err)
	}
	// W1 is evicted; W2 reclaims after the lease.
	w2, err := s.ClaimDue(ctx, t0.Add(lease+time.Second), lease, 1)
	if err != nil || len(w2) != 1 {
		t.Fatalf("W2 reclaim: n=%d err=%v", len(w2), err)
	}

	// W1 now tries to release with its stale token — it must change nothing.
	if err := s.ReleaseClaim(ctx, id, w1[0].ClaimedAt); err != nil {
		t.Fatalf("W1 stale release returned an error: %v", err)
	}

	var status string
	var retry int
	if err := pool.QueryRow(ctx,
		`SELECT status, retry_count FROM webhook_delivery WHERE id=$1`, id).Scan(&status, &retry); err != nil {
		t.Fatalf("read row: %v", err)
	}
	t.Logf("after evicted worker's release: status=%q retry_count=%d (W2 still owns the row)", status, retry)

	if status != "inflight" || retry != w2[0].RetryCount {
		t.Errorf("an evicted worker's release mutated the reclaimer's row: status=%q retry_count=%d "+
			"(want inflight/%d) — the release must be fenced on the claim token",
			status, retry, w2[0].RetryCount)
	}
}

// Terminating a poison row is only acceptable if an operator can bring it back.
// Under claim-time accounting the claim refuses a row with no budget left, so
// the dead-letter requeue must refill the budget — otherwise the escape hatch
// would silently do nothing and the platform would have traded an unbounded loop
// for unrecoverable data loss.
func TestRetryLiveness_RequeuedDeadLetterIsDeliverableAgain(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	s := NewWebhookStore(pool)

	const (
		lease      = time.Minute
		maxRetries = 2
	)
	suffix := time.Now().UnixNano()
	whID := fmt.Sprintf("wh_requeue_%d", suffix)
	id := fmt.Sprintf("dlv_requeue_%d", suffix)
	chain := fmt.Sprintf("requeue-%d", suffix)
	seedWebhook(ctx, t, pool, whID, maxRetries)

	t0 := time.Now().UTC()
	if err := s.Enqueue(ctx, []events.Delivery{{
		ID: id, WebhookID: whID, Status: events.StatusPending, NextAttemptAt: t0.Add(-time.Second),
		Event: events.Event{
			TenantID: domain.SystemTenantID, ChainID: chain, CfgID: chain,
			Operation: "ratification", Seq: 1, EventID: id,
		},
	}}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Burn the budget with a crash-looping worker until the row terminates.
	for cycle := 0; cycle < maxRetries+1; cycle++ {
		if _, err := s.ClaimDue(ctx, t0.Add(time.Duration(cycle)*(lease+time.Second)), lease, 10); err != nil {
			t.Fatalf("cycle %d: claim: %v", cycle, err)
		}
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM webhook_delivery WHERE id=$1`, id).Scan(&status); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if status != string(events.StatusDeadLetter) {
		t.Fatalf("precondition: row is %q, want %q", status, events.StatusDeadLetter)
	}

	// The operator requeues it.
	n, err := s.RequeueDeadLetters(ctx, whID)
	if err != nil {
		t.Fatalf("requeue: %v", err)
	}
	if n != 1 {
		t.Fatalf("requeue affected %d rows, want 1", n)
	}

	// It must be claimable again — a fresh budget, not a row the claim refuses.
	got, err := s.ClaimDue(ctx, time.Now().UTC(), lease, 10)
	if err != nil {
		t.Fatalf("claim after requeue: %v", err)
	}
	found := false
	for _, d := range got {
		if d.ID == id {
			found = true
			t.Logf("requeued delivery reclaimed: retry_count=%d", d.RetryCount)
		}
	}
	if !found {
		t.Error("a requeued dead-letter was not handed to a worker — the operator escape hatch is " +
			"a no-op under claim-time budget enforcement, making dead-lettering unrecoverable")
	}
}

// The policy-execution queue has the identical claim/reclaim shape, so it has
// the identical liveness hole: a crash-looping action is re-executed forever.
func TestRetryLiveness_PolicyReclaimIsBounded(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	s := NewPolicyStore(pool)

	const (
		lease      = time.Minute
		maxRetries = 3
	)
	suffix := time.Now().UnixNano()
	polID := fmt.Sprintf("pol_poison_%d", suffix)
	id := fmt.Sprintf("exec_poison_%d", suffix)

	seedPolicy(ctx, t, pool, polID, maxRetries)

	t0 := time.Now().UTC()
	chain := fmt.Sprintf("poison-pol-%d", suffix)
	if err := s.Enqueue(ctx, []policy.Execution{{
		ID: id, PolicyID: polID, Status: policy.ExecPending, NextAttemptAt: t0.Add(-time.Second),
		Event: policy.Event{
			TenantID: domain.SystemTenantID, CfgID: chain, Operation: "ratification", Seq: 1, EventID: id,
		},
	}}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	claims := 0
	for cycle := 0; cycle < maxRetries*2; cycle++ {
		now := t0.Add(time.Duration(cycle) * (lease + time.Second))
		got, err := s.ClaimDue(ctx, now, lease, 10)
		if err != nil {
			t.Fatalf("cycle %d: claim: %v", cycle, err)
		}
		for _, e := range got {
			if e.ID == id {
				claims++
			}
		}
	}

	var status string
	var retry int
	if err := pool.QueryRow(ctx,
		`SELECT status, retry_count FROM policy_execution WHERE id=$1`, id).
		Scan(&status, &retry); err != nil {
		t.Fatalf("read row: %v", err)
	}

	t.Logf("after %d crash cycles: claims_handed_out=%d status=%q retry_count=%d",
		maxRetries*2, claims, status, retry)

	if claims > maxRetries {
		t.Errorf("UNBOUNDED RETRY: the execution was handed to a worker %d times with a budget of %d — "+
			"every reclaim re-runs the action", claims, maxRetries)
	}
	if status != string(policy.ExecDeadLetter) {
		t.Errorf("NO TERMINATING STATE: status=%q after exhausting the retry budget (want %q)",
			status, policy.ExecDeadLetter)
	}
}
