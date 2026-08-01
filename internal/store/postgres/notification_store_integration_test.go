//go:build integration

package postgres

import (
	"errors"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/notification"
)

func TestNotificationStore_EnqueueGetRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	store := NewNotificationStore(pool)

	n, err := domain.NewNotification(domain.SystemTenantID, domain.NotificationInApp, "alice", "hi", "body")
	if err != nil {
		t.Fatalf("new notification: %v", err)
	}
	created, err := store.Enqueue(ctx, n)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if created.Status != domain.NotificationPending {
		t.Errorf("status = %q, want pending", created.Status)
	}
	if created.RowVersion != 1 {
		t.Errorf("row version = %d, want 1", created.RowVersion)
	}

	got, err := store.Get(ctx, created.NotificationID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Recipient != "alice" || got.Subject != "hi" || got.Body != "body" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}

func TestNotificationStore_GetUnknownIsNotFound(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	store := NewNotificationStore(pool)

	if _, err := store.Get(ctx, "no-such-notification"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("get unknown = %v, want ErrNotFound", err)
	}
}

// This is the test that bites: claim, fail with a retry scheduled, reclaim and
// succeed, then confirm the row is terminal and is never claimed again — the
// full at-least-once/retry/fencing lifecycle against a real database, not a
// fake.
func TestNotificationStore_ClaimDeliverRetryRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	store := NewNotificationStore(pool)

	n, err := domain.NewNotification(domain.SystemTenantID, domain.NotificationWebhook, "http://example.test/hook", "s", "b")
	if err != nil {
		t.Fatalf("new notification: %v", err)
	}
	if _, err := store.Enqueue(ctx, n); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	now := time.Now().UTC()

	// First claim: attempt charged, row due immediately.
	due, err := store.ClaimDue(ctx, now, time.Minute, 10)
	if err != nil {
		t.Fatalf("claim due: %v", err)
	}
	if len(due) != 1 || due[0].NotificationID != n.NotificationID {
		t.Fatalf("claimed %d rows, want exactly the one enqueued", len(due))
	}
	claimed := due[0]
	if claimed.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1 — the claim must charge the attempt", claimed.Attempts)
	}

	// A second, concurrent claim attempt must not also pick it up: the row is
	// freshly claimed and inside its lease.
	due2, err := store.ClaimDue(ctx, now, time.Minute, 10)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if len(due2) != 0 {
		t.Fatalf("a freshly claimed row was claimed again by a concurrent pass: %d rows", len(due2))
	}

	// Record a failure with a retry scheduled shortly.
	next := now.Add(10 * time.Millisecond)
	if err := store.MarkFailed(ctx, claimed.NotificationID, claimed.ClaimedAt, "boom", next); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	got, err := store.Get(ctx, claimed.NotificationID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != domain.NotificationFailed || got.LastError != "boom" {
		t.Fatalf("after failure: %+v", got)
	}
	if got.Terminal() {
		t.Fatal("a failure with a scheduled retry must not be terminal")
	}
	if !got.ClaimedAt.IsZero() {
		t.Fatal("MarkFailed must clear claimed_at, or the retry cannot be reclaimed before the full lease elapses")
	}

	// A stale claim token must be rejected (ADR-CONCURRENCY-005): the row was
	// already unclaimed by the successful MarkFailed above.
	if err := store.MarkSent(ctx, claimed.NotificationID, claimed.ClaimedAt, time.Now()); !errors.Is(err, notification.ErrStaleClaim) {
		t.Fatalf("mark sent with a stale token = %v, want ErrStaleClaim", err)
	}

	// The retry becomes due, and — crucially — is claimable again well within
	// the original lease, because MarkFailed cleared claimed_at.
	time.Sleep(15 * time.Millisecond)
	due3, err := store.ClaimDue(ctx, time.Now().UTC(), time.Minute, 10)
	if err != nil {
		t.Fatalf("claim retry: %v", err)
	}
	if len(due3) != 1 {
		t.Fatalf("retry claim returned %d rows, want 1", len(due3))
	}
	retried := due3[0]
	if retried.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2 on the retried claim", retried.Attempts)
	}

	// This attempt succeeds.
	if err := store.MarkSent(ctx, retried.NotificationID, retried.ClaimedAt, time.Now()); err != nil {
		t.Fatalf("mark sent: %v", err)
	}
	final, err := store.Get(ctx, retried.NotificationID)
	if err != nil {
		t.Fatalf("get final: %v", err)
	}
	if final.Status != domain.NotificationSent || !final.Terminal() {
		t.Fatalf("final state = %+v, want sent/terminal", final)
	}

	// A sent notification is never claimed again.
	due4, err := store.ClaimDue(ctx, time.Now().UTC().Add(time.Hour), time.Minute, 10)
	if err != nil {
		t.Fatalf("claim after sent: %v", err)
	}
	for _, d := range due4 {
		if d.NotificationID == n.NotificationID {
			t.Fatal("a sent notification must never be claimed again")
		}
	}
}

// The retry budget's exhaustion — MarkFailed with a zero `next` — must make
// the row permanently unclaimable, the dead_letter role without a fourth
// status.
func TestNotificationStore_ExhaustedRetryIsNeverClaimedAgain(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	store := NewNotificationStore(pool)

	n, err := domain.NewNotification(domain.SystemTenantID, domain.NotificationInApp, "alice", "s", "b")
	if err != nil {
		t.Fatalf("new notification: %v", err)
	}
	if _, err := store.Enqueue(ctx, n); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	now := time.Now().UTC()
	due, err := store.ClaimDue(ctx, now, time.Minute, 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("claim: due=%d err=%v", len(due), err)
	}
	if err := store.MarkFailed(ctx, due[0].NotificationID, due[0].ClaimedAt, "gave up", time.Time{}); err != nil {
		t.Fatalf("mark failed terminal: %v", err)
	}

	got, err := store.Get(ctx, due[0].NotificationID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.Terminal() {
		t.Fatal("a MarkFailed with a zero next-attempt must be terminal")
	}

	due2, err := store.ClaimDue(ctx, now.Add(24*time.Hour), time.Minute, 10)
	if err != nil {
		t.Fatalf("claim far in the future: %v", err)
	}
	for _, d := range due2 {
		if d.NotificationID == n.NotificationID {
			t.Fatal("an exhausted notification must never be claimed again, however much later")
		}
	}
}

// ReleaseClaim gives back a charged attempt so a worker stopped between
// claiming and attempting does not burn retry budget it never used.
func TestNotificationStore_ReleaseClaimGivesBackTheAttempt(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	store := NewNotificationStore(pool)

	n, err := domain.NewNotification(domain.SystemTenantID, domain.NotificationInApp, "alice", "s", "b")
	if err != nil {
		t.Fatalf("new notification: %v", err)
	}
	if _, err := store.Enqueue(ctx, n); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	now := time.Now().UTC()
	due, err := store.ClaimDue(ctx, now, time.Minute, 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("claim: due=%d err=%v", len(due), err)
	}
	if err := store.ReleaseClaim(ctx, due[0].NotificationID, due[0].ClaimedAt); err != nil {
		t.Fatalf("release: %v", err)
	}

	got, err := store.Get(ctx, due[0].NotificationID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Attempts != 0 {
		t.Errorf("attempts = %d, want 0 (given back)", got.Attempts)
	}
	if !got.ClaimedAt.IsZero() {
		t.Error("a released claim must clear claimed_at")
	}

	// Immediately reclaimable, not blocked for the lease.
	due2, err := store.ClaimDue(ctx, now, time.Minute, 10)
	if err != nil || len(due2) != 1 {
		t.Fatalf("reclaim after release: due=%d err=%v", len(due2), err)
	}
}
