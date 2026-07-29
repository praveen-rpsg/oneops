//go:build integration

package postgres

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/events"
)

// seedWebhook creates the subscription a delivery's retry budget is read from.
// The claim enforces that budget (ADR-CONCURRENCY-006), so a delivery whose
// webhook does not exist has no budget and terminates immediately — correct, but
// not what the claim/lease/fencing tests are about, so they seed a real one with
// a generous budget.
func seedWebhook(ctx context.Context, t *testing.T, pool interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, id string, maxRetries int,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO webhook (id, tenant_id, url, secret, enabled, max_retries)
		VALUES ($1, $2, 'https://example.invalid/hook', 'shh', true, $3)
		ON CONFLICT (id) DO UPDATE SET max_retries = EXCLUDED.max_retries`,
		id, domain.SystemTenantID, maxRetries); err != nil {
		t.Fatalf("seed webhook %s: %v", id, err)
	}
}

// seedPolicy is the executor-queue counterpart of seedWebhook.
func seedPolicy(ctx context.Context, t *testing.T, pool interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, id string, maxRetries int,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO policy (id, tenant_id, name, enabled, action_type, action_config, max_retries)
		VALUES ($1, $2, 'seeded', true, 'webhook', '{}'::jsonb, $3)
		ON CONFLICT (id) DO UPDATE SET max_retries = EXCLUDED.max_retries`,
		id, domain.SystemTenantID, maxRetries); err != nil {
		t.Fatalf("seed policy %s: %v", id, err)
	}
}

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
	seedWebhook(ctx, t, pool, "wh", 100)

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
	seedWebhook(ctx, t, pool, "wh", 100)
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
	seedWebhook(ctx, t, pool, "wh", 100)
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
