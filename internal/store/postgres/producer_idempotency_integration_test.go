//go:build integration

package postgres

import (
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/events"
	"github.com/rpsg/oneops/internal/policy"
)

// A non-idempotent producer duplicated: a re-processed event — a crash before
// the relay's cursor advanced, or two relays during a leadership overlap — was
// enqueued a second time with a fresh random id, a duplicate the receiver could
// not deduplicate. This was proven live: a cursor reset yielded two delivery
// rows with two ids.
//
// The fix (ADR-CONCURRENCY-003) gives the row a content-derived identity that is
// stable across re-production, so the second enqueue collides on the primary key
// (ON CONFLICT (id) DO NOTHING) and no duplicate row appears. This test enqueues
// the SAME logical delivery twice, exactly as a re-processed event would, and
// proves a single row survives with the id the receiver dedups on.
func TestProducerIdempotency_DeliveryReenqueueCollapses(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	s := NewWebhookStore(pool)

	const wh, chain = "wh_idem", "chain_idem"
	const seq int64 = 7
	id := events.DeliveryID(wh, chain, seq) // what the relay would mint, both times

	del := func() events.Delivery {
		return events.Delivery{
			ID: id, WebhookID: wh, Status: events.StatusPending,
			NextAttemptAt: time.Now().Add(-time.Minute),
			Event: events.Event{
				TenantID: domain.SystemTenantID, ChainID: chain, CfgID: chain,
				Operation: "ratification", Seq: seq, EventID: id,
			},
		}
	}

	// First production.
	if err := s.Enqueue(ctx, []events.Delivery{del()}); err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	// Re-production of the SAME logical delivery (cursor reset / overlap).
	if err := s.Enqueue(ctx, []events.Delivery{del()}); err != nil {
		t.Fatalf("re-enqueue: %v", err)
	}

	var rows, distinct int
	if err := pool.QueryRow(ctx,
		"SELECT count(*), count(DISTINCT id) FROM webhook_delivery WHERE chain_id=$1", chain,
	).Scan(&rows, &distinct); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 || distinct != 1 {
		t.Fatalf("re-produced delivery duplicated: rows=%d distinct_ids=%d; want 1/1 — production is not idempotent", rows, distinct)
	}
}

// The same property for policy executions: a re-processed event must not run the
// action twice. The execution id is content-derived, so re-enqueue collides.
func TestProducerIdempotency_ExecutionReenqueueCollapses(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	s := NewPolicyStore(pool)

	const pol, chain = "pol_idem", "chain_idem_exec"
	const seq int64 = 11
	id := policy.ExecutionID(pol, chain, seq)
	now := time.Now()

	ex := func() policy.Execution {
		return policy.Execution{
			ID: id, PolicyID: pol, Status: policy.ExecPending, NextAttemptAt: now.Add(-time.Minute),
			Event: policy.Event{
				TenantID: domain.SystemTenantID, CfgID: chain,
				Operation: "ratification", Seq: seq, EventID: id,
			},
		}
	}

	if err := s.Enqueue(ctx, []policy.Execution{ex()}); err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	if err := s.Enqueue(ctx, []policy.Execution{ex()}); err != nil {
		t.Fatalf("re-enqueue: %v", err)
	}

	var rows, distinct int
	if err := pool.QueryRow(ctx,
		"SELECT count(*), count(DISTINCT id) FROM policy_execution WHERE policy_id=$1", pol,
	).Scan(&rows, &distinct); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 || distinct != 1 {
		t.Fatalf("re-produced execution duplicated: rows=%d distinct_ids=%d; want 1/1", rows, distinct)
	}
}
