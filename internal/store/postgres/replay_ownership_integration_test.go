//go:build integration

package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/events"
)

// Trust Register audit (2026-07-29): entries 5 and 6 claim "privileged-worker
// ownership drift" as an eliminated class — every privileged consumer
// re-derives the work's owner from the audit log instead of trusting what it was
// handed (ADR-TENANCY-003/004).
//
// That was verified on the dispatcher and the policy executor. The replay worker
// is a third privileged consumer, and it has two paths:
//
//   - replayWindow reads the audit log and applies domain.SameOwner — correct,
//     and its comment says exactly why;
//   - replay-by-id calls DeliveryOps.Requeue with the ids from the job, which
//     came from the request body.
//
// Requeue is `UPDATE webhook_delivery SET status='pending', retry_count=0 …
// WHERE id = ANY($1)` on the privileged pool: no tenant predicate, no webhook
// predicate, and no ownership re-derivation. The job's own WebhookID is not even
// consulted.
//
// This test asserts the property entries 5/6 claim platform-wide: a privileged
// consumer does not act on rows it cannot prove belong to the work it was given.
func TestReplayRequeue_CannotTouchAnotherOwnersDeliveries(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	s := NewWebhookStore(pool)

	tenants := NewTenantStore(pool)
	attacker, err := tenants.Create(ctx, newTenant("replay-attacker", "ext-replay-attacker"))
	if err != nil {
		t.Fatalf("create attacker tenant: %v", err)
	}
	victim, err := tenants.Create(ctx, newTenant("replay-victim", "ext-replay-victim"))
	if err != nil {
		t.Fatalf("create victim tenant: %v", err)
	}

	suffix := time.Now().UnixNano()
	attackerWH := fmt.Sprintf("wh_atk_%d", suffix)
	victimWH := fmt.Sprintf("wh_vic_%d", suffix)
	for _, w := range []struct{ id, tenant string }{
		{attackerWH, attacker.TenantID}, {victimWH, victim.TenantID},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO webhook (id, tenant_id, url, secret, enabled, max_retries)
			VALUES ($1, $2, 'https://x.invalid/hook', 'shh', true, 3)`, w.id, w.tenant); err != nil {
			t.Fatalf("seed webhook %s: %v", w.id, err)
		}
	}

	// The victim has a delivery that has exhausted its budget and terminated.
	victimDelivery := fmt.Sprintf("dlv_victim_%d", suffix)
	chain := fmt.Sprintf("victim-chain-%d", suffix)
	if err := s.Enqueue(ctx, []events.Delivery{{
		ID: victimDelivery, WebhookID: victimWH, Status: events.StatusPending,
		NextAttemptAt: time.Now().UTC(),
		Event: events.Event{
			TenantID: victim.TenantID, ChainID: chain, CfgID: chain,
			Operation: "ratification", Seq: 1, EventID: victimDelivery,
		},
	}}); err != nil {
		t.Fatalf("enqueue victim delivery: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE webhook_delivery SET status='dead_letter', retry_count=3 WHERE id=$1`,
		victimDelivery); err != nil {
		t.Fatalf("terminate victim delivery: %v", err)
	}

	before := deliveryState(t, pool, victimDelivery)
	t.Logf("victim delivery before: status=%s retry_count=%d", before.status, before.retry)

	// The attacker asks to replay *their own* webhook, naming the victim's
	// delivery id. This is exactly what the worker does with job.DeliveryIDs.
	n, err := s.Requeue(ctx, attackerWH, []string{victimDelivery})
	if err != nil {
		t.Fatalf("requeue: %v", err)
	}

	after := deliveryState(t, pool, victimDelivery)
	t.Logf("requeue affected %d row(s); victim delivery after: status=%s retry_count=%d",
		n, after.status, after.retry)

	if after.status != before.status || after.retry != before.retry {
		t.Errorf("CROSS-OWNER MUTATION: a replay naming another owner's delivery id reset it "+
			"(status %s→%s, retry_count %d→%d). The privileged consumer acted on a row it never "+
			"proved belonged to the work it was given — entries 5/6 record that class as "+
			"eliminated, but the replay-by-id path re-derives nothing",
			before.status, after.status, before.retry, after.retry)
	}
}

type dstate struct {
	status string
	retry  int
}

func deliveryState(t *testing.T, pool *pgxpool.Pool, id string) dstate {
	t.Helper()
	var d dstate
	if err := pool.QueryRow(context.Background(),
		`SELECT status, retry_count FROM webhook_delivery WHERE id=$1`, id).
		Scan(&d.status, &d.retry); err != nil {
		t.Fatalf("read delivery %s: %v", id, err)
	}
	return d
}
