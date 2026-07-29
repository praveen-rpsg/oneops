//go:build integration

package postgres

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/events"
)

// alwaysOwner resolves every event to the system tenant, so ownership
// authorization passes and the test isolates the outcome-recording path.
type alwaysOwner struct{}

func (alwaysOwner) ResolveEventOwner(context.Context, string, int64) (string, error) {
	return domain.SystemTenantID, nil
}

// An outcome the platform has already produced must be durable, even when the
// worker is being stopped.
//
// ADR-CONCURRENCY-003 made demotion routine: the moment a leader loses its
// advisory lock, the leadership context is cancelled and the workers stop. The
// workers write their outcome with that same cancellable context. So a delivery
// in flight across a demotion POSTs successfully, and then its MarkResult is
// issued on an already-cancelled context and fails. The error is discarded, the
// success metric is incremented anyway, and the row is left `inflight` with
// retry_count unchanged — to be reclaimed and re-sent, forever (see
// TestRetryLiveness_*). One demotion converts a delivered event into an
// unbounded redelivery loop, and the metrics report it as delivered.
//
// This test asserts the property: the receiver got the POST, so the database
// must record the outcome, regardless of the worker's context being cancelled.
func TestOutcomeDurability_ResultSurvivesWorkerCancellation(t *testing.T) {
	pool := testPool(t)
	bg := context.Background()
	s := NewWebhookStore(pool)

	// The subscriber accepts and processes the event, but takes long enough that
	// the demotion lands while the call is in flight — the ordering a real
	// leadership handoff produces.
	var received int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&received, 1)
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	suffix := time.Now().UnixNano()
	whID := fmt.Sprintf("wh_cancel_%d", suffix)
	id := fmt.Sprintf("dlv_cancel_%d", suffix)
	chain := fmt.Sprintf("cancel-%d", suffix)

	if _, err := pool.Exec(bg, `
		INSERT INTO webhook (id, tenant_id, url, secret, enabled, max_retries)
		VALUES ($1, $2, $3, 'shh', true, 3)`,
		whID, domain.SystemTenantID, srv.URL); err != nil {
		t.Fatalf("seed webhook: %v", err)
	}

	t0 := time.Now().UTC()
	if err := s.Enqueue(bg, []events.Delivery{{
		ID: id, WebhookID: whID, Status: events.StatusPending, NextAttemptAt: t0.Add(-time.Second),
		Event: events.Event{
			TenantID: domain.SystemTenantID, ChainID: chain, CfgID: chain,
			Operation: "ratification", Seq: 1, EventID: id,
		},
	}}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	d := events.NewDispatcher(s, s, alwaysOwner{}, srv.Client(), nil, slog.Default(), events.DispatcherConfig{})

	claimed, err := s.ClaimDue(bg, t0, time.Minute, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: n=%d err=%v", len(claimed), err)
	}

	// This instance is demoted while the delivery is in flight: the leadership
	// context is cancelled. The subscriber has already received and is processing
	// the event.
	wctx, cancel := context.WithCancel(bg)
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	status, derr := d.Deliver(wctx, claimed[0])

	var dbStatus string
	var retry int
	if err := pool.QueryRow(bg,
		`SELECT status, retry_count FROM webhook_delivery WHERE id=$1`, id).
		Scan(&dbStatus, &retry); err != nil {
		t.Fatalf("read row: %v", err)
	}

	t.Logf("dispatcher returned status=%q err=%v; receiver got %d POST(s); "+
		"db row status=%q retry_count=%d",
		status, derr, atomic.LoadInt32(&received), dbStatus, retry)

	if atomic.LoadInt32(&received) == 0 {
		t.Skip("receiver never got the POST; cancellation raced ahead of the send — not the case under test")
	}
	if dbStatus == "inflight" {
		t.Errorf("LOST OUTCOME: the subscriber received the delivery but the row is still %q "+
			"(retry_count=%d) — it will be reclaimed and re-sent with no budget depletion", dbStatus, retry)
	}
}
