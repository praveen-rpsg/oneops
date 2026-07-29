//go:build integration

package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/events"
)

// The recorded destination of a past delivery must not change when the
// subscription is repointed.
//
// Proven live before this: `webhook_delivery` held only `webhook_id`, so the
// destination was obtainable only by joining to `webhook.url`. One PATCH — 200,
// no audit event — retroactively rewrote where every delivery through that
// subscription had gone. An investigator asking "where was this event sent?"
// would be told the attacker's URL for events delivered to the approved one, and
// an attacker who repointed, collected, and repointed back would leave the
// history reading as the approved destination throughout (ADR-GOV-004).
func TestDeliveryDestination_SurvivesWebhookRepointing(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewWebhookStore(pool)

	suffix := time.Now().UnixNano()
	whID := fmt.Sprintf("wh_dest_%d", suffix)
	id := fmt.Sprintf("dlv_dest_%d", suffix)
	chain := fmt.Sprintf("dest-%d", suffix)
	const approved = "https://approved.invalid/hook"
	const attacker = "https://attacker.invalid/steal"

	if _, err := pool.Exec(ctx, `
		INSERT INTO webhook (id, tenant_id, url, secret, enabled, max_retries)
		VALUES ($1, $2, $3, 'shh', true, 5)`,
		whID, domain.SystemTenantID, approved); err != nil {
		t.Fatalf("seed webhook: %v", err)
	}

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

	// The attempt happens and is recorded, destination included.
	if err := s.MarkResult(ctx, id, time.Time{}, events.StatusDelivered, 1, 200, t0, time.Time{}, events.AttemptFacts{Destination: approved, SignedTS: 1700000000}); err != nil {
		t.Fatalf("mark delivered: %v", err)
	}

	got := deliveredTo(t, pool, id)
	if got != approved {
		t.Fatalf("destination not recorded at attempt time: got %q, want %q", got, approved)
	}
	t.Logf("recorded destination: %s", got)

	// The subscription is repointed — the exploit.
	if _, err := pool.Exec(ctx, `UPDATE webhook SET url=$2 WHERE id=$1`, whID, attacker); err != nil {
		t.Fatalf("repoint webhook: %v", err)
	}

	got = deliveredTo(t, pool, id)
	t.Logf("after repointing the subscription to %s, the delivery still records: %s", attacker, got)
	if got != approved {
		t.Errorf("RETROACTIVE REWRITE: the historical delivery now reports destination %q after "+
			"the subscription was repointed; the record of where governed data was sent must be "+
			"a fact captured at the time, not a pointer into mutable state", got)
	}

	// And the delivery must be readable through the store with the fact intact.
	ds, err := s.ListByWebhook(ctx, whID, 10)
	if err != nil || len(ds) != 1 {
		t.Fatalf("list: n=%d err=%v", len(ds), err)
	}
	if ds[0].DeliveredTo != approved {
		t.Errorf("store surfaced destination %q, want %q", ds[0].DeliveredTo, approved)
	}
}

// An outcome reached without an attempt must neither invent a destination nor
// erase one already recorded. A refused delivery (subscriber gone, ownership
// refused) never left the platform, so it has no destination fact to report.
func TestDeliveryDestination_UnattemptedOutcomeRecordsNothing(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewWebhookStore(pool)

	suffix := time.Now().UnixNano()
	whID := fmt.Sprintf("wh_noatt_%d", suffix)
	id := fmt.Sprintf("dlv_noatt_%d", suffix)
	chain := fmt.Sprintf("noatt-%d", suffix)

	if _, err := pool.Exec(ctx, `
		INSERT INTO webhook (id, tenant_id, url, secret, enabled, max_retries)
		VALUES ($1, $2, 'https://x.invalid/h', 'shh', true, 5)`,
		whID, domain.SystemTenantID); err != nil {
		t.Fatalf("seed webhook: %v", err)
	}
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

	// Dead-lettered without an attempt: no destination.
	if err := s.MarkResult(ctx, id, time.Time{}, events.StatusDeadLetter, 0, 0, t0, time.Time{}, events.AttemptFacts{}); err != nil {
		t.Fatalf("mark dead-letter: %v", err)
	}
	if got := deliveredTo(t, pool, id); got != "" {
		t.Errorf("a delivery that was never attempted records destination %q — the record would "+
			"claim data was sent somewhere it never went", got)
	}

	// Now an attempt happens, and a later unattempted outcome must not erase it.
	const real = "https://real.invalid/hook"
	if err := s.MarkResult(ctx, id, time.Time{}, events.StatusFailed, 1, 500, t0, t0.Add(time.Minute), events.AttemptFacts{Destination: real, SignedTS: 1700000001}); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	if err := s.MarkResult(ctx, id, time.Time{}, events.StatusDeadLetter, 2, 0, t0, time.Time{}, events.AttemptFacts{}); err != nil {
		t.Fatalf("mark dead-letter after attempt: %v", err)
	}
	if got := deliveredTo(t, pool, id); got != real {
		t.Errorf("an unattempted outcome erased the recorded destination: got %q, want %q", got, real)
	}
}

// deliveredTo reads the recorded destination straight from the row, so the
// assertion is about what is stored rather than what any query composes.
func deliveredTo(t *testing.T, pool *pgxpool.Pool, id string) string {
	t.Helper()
	var v *string
	if err := pool.QueryRow(context.Background(),
		`SELECT delivered_to FROM webhook_delivery WHERE id=$1`, id).Scan(&v); err != nil {
		t.Fatalf("read delivered_to: %v", err)
	}
	if v == nil {
		return ""
	}
	return *v
}
