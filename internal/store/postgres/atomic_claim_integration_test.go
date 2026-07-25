//go:build integration

package postgres

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/events"
)

func enqueueDelivery(ctx context.Context, t *testing.T, s *WebhookStore, id, chain string) {
	t.Helper()
	if err := s.Enqueue(ctx, []events.Delivery{{
		ID: id, WebhookID: "wh", Status: events.StatusPending,
		NextAttemptAt: time.Now().Add(-time.Minute),
		Event: events.Event{
			TenantID: domain.SystemTenantID, ChainID: chain, CfgID: chain,
			Operation: "ratification", Seq: 1, EventID: id,
		},
	}}); err != nil {
		t.Fatalf("enqueue %s: %v", id, err)
	}
}

// Two workers claiming at once must never receive the same delivery. Before the
// atomic claim, ClaimDue was a plain SELECT and an overlap window during a
// leadership handoff double-delivered. FOR UPDATE SKIP LOCKED plus the status
// transition makes the claim exclusive.
func TestAtomicClaim_ConcurrentClaimsAreDisjoint(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewWebhookStore(pool)

	const n = 20
	for i := 0; i < n; i++ {
		enqueueDelivery(ctx, t, s, "d"+string(rune('a'+i)), "chain-disjoint")
	}

	var wg sync.WaitGroup
	results := make([][]events.Delivery, 2)
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			got, err := s.ClaimDue(ctx, time.Now(), time.Minute, n)
			if err != nil {
				t.Errorf("worker %d claim: %v", w, err)
				return
			}
			results[w] = got
		}(w)
	}
	wg.Wait()

	seen := map[string]int{}
	for _, batch := range results {
		for _, d := range batch {
			seen[d.ID]++
		}
	}
	for id, c := range seen {
		if c > 1 {
			t.Errorf("delivery %s was claimed by %d workers at once", id, c)
		}
	}
	if len(seen) == 0 {
		t.Fatal("no deliveries claimed")
	}
}

// A claimed (inflight) delivery is not reclaimed while its lease is live, so a
// single worker never re-delivers a row it is still working.
func TestAtomicClaim_InflightNotReclaimedWithinLease(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewWebhookStore(pool)
	enqueueDelivery(ctx, t, s, "d-lease", "chain-lease")

	first, err := s.ClaimDue(ctx, time.Now(), time.Minute, 10)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("first claim got %d, want 1", len(first))
	}

	// Immediately claim again: the row is inflight and its lease is live.
	second, err := s.ClaimDue(ctx, time.Now(), time.Minute, 10)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	for _, d := range second {
		if d.ID == "d-lease" {
			t.Fatal("an inflight delivery was reclaimed within its lease")
		}
	}
}

// A delivery whose claimer crashed — an inflight row older than the lease — is
// reclaimed, so work is never lost. This is the failover-recovery path, bounded
// by the lease rather than replayed immediately.
func TestAtomicClaim_StaleInflightIsReclaimed(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewWebhookStore(pool)
	enqueueDelivery(ctx, t, s, "d-stale", "chain-stale")

	if _, err := s.ClaimDue(ctx, time.Now(), time.Minute, 10); err != nil {
		t.Fatalf("claim: %v", err)
	}
	// Age the claim beyond the lease, as a crashed worker leaves it.
	if _, err := pool.Exec(ctx,
		`UPDATE webhook_delivery SET claimed_at = now() - interval '10 minutes' WHERE id='d-stale'`); err != nil {
		t.Fatalf("age claim: %v", err)
	}

	reclaimed, err := s.ClaimDue(ctx, time.Now(), time.Minute, 10)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	found := false
	for _, d := range reclaimed {
		if d.ID == "d-stale" {
			found = true
		}
	}
	if !found {
		t.Fatal("a stale inflight delivery was not reclaimed — work would be lost after a crash")
	}
}
