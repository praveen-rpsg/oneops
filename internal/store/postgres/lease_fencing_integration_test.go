//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/events"
	"github.com/rpsg/oneops/internal/policy"
)

// A worker whose lease expires and whose row is reclaimed by another must not be
// able to write that row: its outbound call may have outlived the lease, and an
// unfenced completion resurrects a delivered row into a retry state or overwrites
// the reclaimer's outcome — an extra delivery caused purely by the stale write.
// This is the fencing residual ADR-CONCURRENCY-002 left open; ADR-CONCURRENCY-005
// closes it by fencing MarkResult on the claim token (claimed_at).
//
// Pre-fix this failed: the evicted worker's `failed` mark resurrected a delivered
// row. Post-fix the evicted write is fenced (ErrStaleClaim) and the row keeps the
// reclaiming owner's terminal state.
func TestLeaseFencing_WebhookEvictedWorkerIsFenced(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewWebhookStore(pool)
	const lease = time.Minute
	chain := fmt.Sprintf("fence-wh-%d", time.Now().UnixNano())
	id := "dlv_" + chain

	t0 := time.Now().UTC()
	if err := s.Enqueue(ctx, []events.Delivery{{
		ID: id, WebhookID: "wh", Status: events.StatusPending, NextAttemptAt: t0.Add(-time.Second),
		Event: events.Event{TenantID: domain.SystemTenantID, ChainID: chain, CfgID: chain, Operation: "ratification", Seq: 1, EventID: id},
	}}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	w1, err := s.ClaimDue(ctx, t0, lease, 1)
	if err != nil || len(w1) != 1 {
		t.Fatalf("W1 claim: n=%d err=%v", len(w1), err)
	}
	token1 := w1[0].ClaimedAt
	if token1.IsZero() {
		t.Fatal("claim did not surface a fencing token (ClaimedAt is zero)")
	}

	w2, err := s.ClaimDue(ctx, t0.Add(lease+time.Second), lease, 1)
	if err != nil || len(w2) != 1 {
		t.Fatalf("W2 reclaim: n=%d err=%v", len(w2), err)
	}
	token2 := w2[0].ClaimedAt
	if !token2.After(token1) {
		t.Fatalf("reclaim did not advance the fencing token: token1=%v token2=%v", token1, token2)
	}

	// W2 (current owner) delivers.
	if err := s.MarkResult(ctx, id, token2, events.StatusDelivered, 0, 200, t0.Add(lease+2*time.Second), time.Time{}); err != nil {
		t.Fatalf("W2 mark delivered: %v", err)
	}

	// W1 (evicted) tries to fail+reschedule with its stale token → must be fenced.
	err = s.MarkResult(ctx, id, token1, events.StatusFailed, 1, 500, t0.Add(lease+3*time.Second), t0.Add(lease+time.Minute))
	if !errors.Is(err, events.ErrStaleClaim) {
		t.Fatalf("evicted worker's write was NOT fenced: err=%v (want ErrStaleClaim)", err)
	}

	// The row keeps the reclaimer's terminal state — not resurrected. A landed
	// stale write would show status='failed' with next_attempt_at moved to the
	// evicted worker's reschedule; the fence keeps it 'delivered' and leaves the
	// reschedule untouched.
	var status string
	var next *time.Time
	if err := pool.QueryRow(ctx, `SELECT status, next_attempt_at FROM webhook_delivery WHERE id=$1`, id).Scan(&status, &next); err != nil {
		t.Fatalf("read row: %v", err)
	}
	staleReschedule := t0.Add(lease + time.Minute)
	if status != "delivered" {
		t.Fatalf("row resurrected by evicted worker: status=%q (want delivered)", status)
	}
	if next != nil && next.Truncate(time.Second).Equal(staleReschedule.Truncate(time.Second)) {
		t.Fatalf("evicted worker rescheduled the delivered row to %v — the stale write landed", next)
	}

	// The admin/direct path (no claim token) still writes unfenced.
	if err := s.MarkResult(ctx, id, time.Time{}, events.StatusDelivered, 0, 200, t0, time.Time{}); err != nil {
		t.Fatalf("unfenced (zero-token) write should still succeed: %v", err)
	}
}

// The same fence protects policy executions.
func TestLeaseFencing_PolicyEvictedWorkerIsFenced(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewPolicyStore(pool)
	const lease = time.Minute
	chain := fmt.Sprintf("fence-pol-%d", time.Now().UnixNano())
	id := "exec_" + chain

	t0 := time.Now().UTC()
	if err := s.Enqueue(ctx, []policy.Execution{{
		ID: id, PolicyID: "pol", Status: policy.ExecPending, NextAttemptAt: t0.Add(-time.Second),
		Event: policy.Event{TenantID: domain.SystemTenantID, CfgID: chain, Operation: "ratification", Seq: 1, EventID: id},
	}}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	w1, err := s.ClaimDue(ctx, t0, lease, 1)
	if err != nil || len(w1) != 1 {
		t.Fatalf("W1 claim: n=%d err=%v", len(w1), err)
	}
	token1 := w1[0].ClaimedAt
	if token1.IsZero() {
		t.Fatal("policy claim did not surface a fencing token")
	}

	w2, err := s.ClaimDue(ctx, t0.Add(lease+time.Second), lease, 1)
	if err != nil || len(w2) != 1 {
		t.Fatalf("W2 reclaim: n=%d err=%v", len(w2), err)
	}
	token2 := w2[0].ClaimedAt

	if err := s.MarkResult(ctx, id, token2, policy.ExecSucceeded, 0, "", t0, t0.Add(lease+2*time.Second), time.Time{}); err != nil {
		t.Fatalf("W2 mark succeeded: %v", err)
	}
	err = s.MarkResult(ctx, id, token1, policy.ExecFailed, 1, "late failure", t0, t0.Add(lease+3*time.Second), t0.Add(lease+time.Minute))
	if !errors.Is(err, policy.ErrStaleClaim) {
		t.Fatalf("evicted policy worker's write was NOT fenced: err=%v (want ErrStaleClaim)", err)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM policy_execution WHERE id=$1`, id).Scan(&status); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if status != "succeeded" {
		t.Fatalf("policy row corrupted by evicted worker: status=%q (want succeeded)", status)
	}
}
