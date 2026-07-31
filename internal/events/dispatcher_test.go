package events

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// capturingLogger is quiet() but keeps the text so a test can assert a
// particular log line was (or was not) emitted, instead of only that the
// code path did not panic.
func capturingLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

// ---- RunOnce: claim-pass failure ---------------------------------------------

// A claim-pass failure (e.g. the database is unreachable) must halt the pass
// without attempting anything — there is nothing claimed to attempt, and the
// dispatcher must not panic on the store error.
func TestDispatcher_RunOnce_ClaimErrorHaltsWithoutAttempting(t *testing.T) {
	del := newFakeDeliveries()
	del.claimErr = errors.New("db unavailable")
	called := false
	doer := doerFunc(func(*http.Request) (*http.Response, error) { called = true; return resp(200), nil })
	d := NewDispatcher(del, newFakeWebhooks(), newFakeOwners(), doer, nil, quiet(), DispatcherConfig{})

	d.RunOnce(context.Background()) // must not panic

	if called {
		t.Fatal("a claim-pass error must not be followed by any delivery attempt")
	}
}

// ---- RunOnce: cancellation mid-batch gives back unattempted claims -----------

// A worker demoted or shut down mid-batch must give back every claim it has
// not yet attempted — those rows were charged an attempt by ClaimDue
// (ADR-CONCURRENCY-006) but never sent, so a few restarts would otherwise
// dead-letter deliveries that were never sent (at-least-once, ADR-CONCURRENCY-002).
func TestDispatcher_RunOnce_CtxCancelMidBatchReleasesUnattemptedClaims(t *testing.T) {
	wh := newFakeWebhooks(Webhook{ID: "w1", TenantID: testTenant, URL: "http://x", Secret: "s", Enabled: true, MaxRetries: 3})
	del := newFakeDeliveries()
	now := time.Now()
	_ = del.Enqueue(context.Background(), []Delivery{
		newDelivery("d1", "w1", now), newDelivery("d2", "w1", now), newDelivery("d3", "w1", now),
	})

	ctx, cancel := context.WithCancel(context.Background())
	var attempts int
	var mu sync.Mutex
	doer := doerFunc(func(*http.Request) (*http.Response, error) {
		mu.Lock()
		attempts++
		mu.Unlock()
		// Simulate the demotion/shutdown landing right after the first delivery
		// is sent but before the batch finishes.
		cancel()
		return resp(200), nil
	})
	d := NewDispatcher(del, wh, newFakeOwners(), doer, nil, quiet(), DispatcherConfig{})
	d.RunOnce(ctx)

	mu.Lock()
	got := attempts
	mu.Unlock()
	if got != 1 {
		t.Fatalf("attempts = %d, want exactly 1 (rest must be released, not attempted, after cancellation)", got)
	}

	delivered, released := 0, 0
	for _, d := range del.all() {
		switch d.Status {
		case StatusDelivered:
			delivered++
		case StatusPending:
			released++
			if d.RetryCount != 0 {
				t.Errorf("%s: RetryCount = %d, want 0 — the claim's charged attempt must be given back on release", d.ID, d.RetryCount)
			}
		default:
			t.Errorf("%s: status = %q, want delivered or released-to-pending", d.ID, d.Status)
		}
	}
	if delivered != 1 || released != 2 {
		t.Fatalf("delivered=%d released=%d, want 1 and 2 (unattempted claims must be released, not left inflight)", delivered, released)
	}
}

// A context already cancelled before the pass begins must release the entire
// claimed batch: nothing is attempted, and every claim is given back.
func TestDispatcher_RunOnce_PreCancelledCtxReleasesWholeBatch(t *testing.T) {
	wh := newFakeWebhooks(Webhook{ID: "w1", TenantID: testTenant, URL: "http://x", Secret: "s", Enabled: true, MaxRetries: 3})
	del := newFakeDeliveries()
	now := time.Now()
	_ = del.Enqueue(context.Background(), []Delivery{newDelivery("d1", "w1", now), newDelivery("d2", "w1", now)})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	doer := doerFunc(func(*http.Request) (*http.Response, error) { called = true; return resp(200), nil })
	NewDispatcher(del, wh, newFakeOwners(), doer, nil, quiet(), DispatcherConfig{}).RunOnce(ctx)

	if called {
		t.Fatal("a pre-cancelled context must not be followed by any delivery attempt")
	}
	for _, d := range del.all() {
		if d.Status != StatusPending || d.RetryCount != 0 {
			t.Errorf("%s: status=%q retry=%d, want released to pending with attempt given back", d.ID, d.Status, d.RetryCount)
		}
	}
}

// A failed release of an unused claim (the detached write itself fails) must
// not crash the worker — the row stays claimed and will be reclaimed once its
// lease expires (ADR-CONCURRENCY-002), and the failure is only logged.
func TestDispatcher_RunOnce_ReleaseClaimErrorIsLoggedNotFatal(t *testing.T) {
	wh := newFakeWebhooks(Webhook{ID: "w1", TenantID: testTenant, URL: "http://x", Secret: "s", Enabled: true, MaxRetries: 3})
	del := newFakeDeliveries()
	_ = del.Enqueue(context.Background(), []Delivery{newDelivery("d1", "w1", time.Now())})
	del.releaseErr = errors.New("release write failed")

	log, buf := capturingLogger()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled: the single claimed row goes straight to release
	d := NewDispatcher(del, wh, newFakeOwners(), doerFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("must not attempt delivery once cancelled")
		return resp(200), nil
	}), nil, log, DispatcherConfig{})

	d.RunOnce(ctx) // must not panic despite ReleaseClaim failing

	if !strings.Contains(buf.String(), "release unused claim") {
		t.Errorf("release failure was not logged: %s", buf.String())
	}
}

// ---- attempt: MarkResult fencing on success ----------------------------------

// A successful delivery whose outcome write is fenced (the lease expired and
// another worker already reclaimed the row) must not be recorded over the
// reclaimer's state, and must not double-count the delivered metric — the POST
// happened, but this worker no longer owns the row (ADR-CONCURRENCY-005).
func TestDispatcher_Attempt_SuccessFencedByReclaimDoesNotDoubleCount(t *testing.T) {
	wh := newFakeWebhooks(Webhook{ID: "w1", TenantID: testTenant, URL: "http://x", Secret: "s", Enabled: true, MaxRetries: 3})
	del := newFakeDeliveries()
	del.staleIDs = map[string]bool{"d1": true}
	doer := doerFunc(func(*http.Request) (*http.Response, error) { return resp(200), nil })
	m := &countMetrics{}
	d := NewDispatcher(del, wh, newFakeOwners(), doer, m, quiet(), DispatcherConfig{})

	status, err := d.Deliver(context.Background(), newDelivery("d1", "w1", time.Now()))

	if err != nil {
		t.Fatalf("a fenced outcome is not this worker's failure: err = %v", err)
	}
	if status != StatusDelivered {
		t.Fatalf("status = %q, want delivered (the send happened)", status)
	}
	if m.delivered != 0 {
		t.Errorf("delivered metric = %d, want 0 — a fenced write must not double-count the reclaimer's success", m.delivered)
	}
}

// A successful delivery whose outcome write fails for a reason OTHER than
// fencing must surface the error: the POST happened but was not recorded, so
// the row must remain claimed for reclaim and re-send rather than being
// silently forgotten (ADR-CONCURRENCY-006).
func TestDispatcher_Attempt_SuccessMarkResultErrorSurfacesNotSwallowed(t *testing.T) {
	wh := newFakeWebhooks(Webhook{ID: "w1", TenantID: testTenant, URL: "http://x", Secret: "s", Enabled: true, MaxRetries: 3})
	del := newFakeDeliveries()
	writeErr := errors.New("write timeout")
	del.markErr = map[string]error{"d1": writeErr}
	doer := doerFunc(func(*http.Request) (*http.Response, error) { return resp(200), nil })
	m := &countMetrics{}
	d := NewDispatcher(del, wh, newFakeOwners(), doer, m, quiet(), DispatcherConfig{})

	status, err := d.Deliver(context.Background(), newDelivery("d1", "w1", time.Now()))

	if !errors.Is(err, writeErr) {
		t.Fatalf("err = %v, want the underlying write error surfaced", err)
	}
	if status != StatusDelivered {
		t.Fatalf("status = %q, want delivered (the send happened even though recording it failed)", status)
	}
	if m.delivered != 0 {
		t.Errorf("delivered metric = %d, want 0 — an unrecorded outcome must not claim success", m.delivered)
	}
}

// A failed delivery whose retry/dead-letter write is fenced by a reclaim must
// not crash and must not attempt to reschedule against the reclaimer's row.
//
// RetryCount is set to 1, the minimum value ClaimDue ever hands attempt() (it
// always increments before returning, ADR-CONCURRENCY-006) — this test is
// about fencing, not about the RetryCount==0 case. That case (backoff's shift
// no longer going negative) is its own dedicated regression, see
// TestDispatcher_Backoff_ZeroRetryDoesNotPanic and
// TestDispatcher_Deliver_FreshRecordAgainstFailingTargetDoesNotPanic below.
func TestDispatcher_Attempt_FailureFencedByReclaimIsNotFatal(t *testing.T) {
	wh := newFakeWebhooks(Webhook{ID: "w1", TenantID: testTenant, URL: "http://x", Secret: "s", Enabled: true, MaxRetries: 3})
	del := newFakeDeliveries()
	del.staleIDs = map[string]bool{"d1": true}
	doer := doerFunc(func(*http.Request) (*http.Response, error) { return resp(500), nil })
	log, buf := capturingLogger()
	d := NewDispatcher(del, wh, newFakeOwners(), doer, nil, log, DispatcherConfig{BaseBackoff: time.Millisecond})

	del2 := newDelivery("d1", "w1", time.Now())
	del2.RetryCount = 1 // below MaxRetries, so this is a retry not a dead-letter
	status, err := d.Deliver(context.Background(), del2)

	// A plain non-2xx response (no transport error) is a failure with no error
	// value: post() only returns an error for a transport failure, so a 500
	// surfaces solely as StatusFailed. Callers must consult status, not err,
	// to detect an HTTP-level failure.
	if status != StatusFailed {
		t.Fatalf("status = %q, want failed", status)
	}
	if err != nil {
		t.Errorf("err = %v, want nil (a non-2xx response is not a Go error)", err)
	}
	if !strings.Contains(buf.String(), "failed result fenced") {
		t.Errorf("fenced retry-write was not logged: %s", buf.String())
	}
}

// ---- attempt: transport failure ----------------------------------------------

// A network-level transport error (no response at all) must be treated the
// same as a bad status code: it counts as a failed attempt eligible for retry,
// never left unrecorded.
func TestDispatcher_Attempt_TransportErrorRetries(t *testing.T) {
	wh := newFakeWebhooks(Webhook{ID: "w1", TenantID: testTenant, URL: "http://x", Secret: "s", Enabled: true, MaxRetries: 3})
	del := newFakeDeliveries()
	transportErr := errors.New("connection refused")
	doer := doerFunc(func(*http.Request) (*http.Response, error) { return nil, transportErr })
	m := &countMetrics{}
	d := NewDispatcher(del, wh, newFakeOwners(), doer, m, quiet(), DispatcherConfig{BaseBackoff: time.Millisecond})

	del1 := newDelivery("d1", "w1", time.Now())
	del1.RetryCount = 1 // the minimum ClaimDue ever produces (ADR-CONCURRENCY-006)
	status, err := d.Deliver(context.Background(), del1)

	if !errors.Is(err, transportErr) || status != StatusFailed {
		t.Fatalf("status=%q err=%v, want failed wrapping the transport error", status, err)
	}
	if m.retry != 1 {
		t.Errorf("retry metric = %d, want 1 — a transport error is a retryable failure, not silently dropped", m.retry)
	}
}

// A malformed destination URL fails building the HTTP request itself (never
// reaches the network). It must be accounted exactly like any other failed
// attempt — retried or dead-lettered — not silently dropped.
func TestDispatcher_Attempt_BadRequestURLFailsAttempt(t *testing.T) {
	wh := newFakeWebhooks(Webhook{ID: "w1", TenantID: testTenant, URL: "http://x\x7f", Secret: "s", Enabled: true, MaxRetries: 3})
	del := newFakeDeliveries()
	called := false
	doer := doerFunc(func(*http.Request) (*http.Response, error) { called = true; return resp(200), nil })
	d := NewDispatcher(del, wh, newFakeOwners(), doer, nil, quiet(), DispatcherConfig{BaseBackoff: time.Millisecond})

	del1 := newDelivery("d1", "w1", time.Now())
	del1.RetryCount = 1
	status, err := d.Deliver(context.Background(), del1)

	if called {
		t.Fatal("a request that could not even be built must never reach the transport")
	}
	if status != StatusFailed || err == nil {
		t.Fatalf("status=%q err=%v, want failed with the build error surfaced", status, err)
	}
}

// ---- NewDispatcher defaults ---------------------------------------------------

// A nil logger must not be carried forward as a nil — every log call would
// panic the worker on its very first line.
func TestNewDispatcher_NilLoggerDefaults(_ *testing.T) {
	d := NewDispatcher(newFakeDeliveries(), newFakeWebhooks(), newFakeOwners(),
		doerFunc(func(*http.Request) (*http.Response, error) { return resp(200), nil }), nil, nil, DispatcherConfig{})
	d.RunOnce(context.Background()) // must not panic on a nil-defaulted logger
}

// ---- Run: clean shutdown -------------------------------------------------------

// Run must perform an initial pass and then return ctx.Err() promptly once the
// context is cancelled — the only part of the ticker-driven loop that is a
// deterministic unit to test (the ticker itself is not).
func TestDispatcher_Run_ReturnsCtxErrOnCancel(t *testing.T) {
	del := newFakeDeliveries()
	d := NewDispatcher(del, newFakeWebhooks(), newFakeOwners(),
		doerFunc(func(*http.Request) (*http.Response, error) { return resp(200), nil }), nil, quiet(), DispatcherConfig{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

// ---- backoff ------------------------------------------------------------------

// The backoff schedule is capped: it must never exceed MaxBackoff even when
// doubling would otherwise overflow or run past it.
func TestDispatcher_Backoff_CapsAtMaxBackoff(t *testing.T) {
	d := NewDispatcher(newFakeDeliveries(), newFakeWebhooks(), newFakeOwners(), doerFunc(func(*http.Request) (*http.Response, error) { return resp(200), nil }), nil, quiet(),
		DispatcherConfig{BaseBackoff: time.Second, MaxBackoff: time.Minute})

	if got := d.backoff(1); got != time.Second {
		t.Errorf("backoff(1) = %v, want %v (first retry is the base delay)", got, time.Second)
	}
	if got := d.backoff(10); got != time.Minute {
		t.Errorf("backoff(10) = %v, want capped at MaxBackoff %v", got, time.Minute)
	}
	// A retry count large enough to overflow the shift must still cap rather
	// than wrap around to a negative or zero duration.
	if got := d.backoff(100); got != time.Minute {
		t.Errorf("backoff(100) = %v, want capped at MaxBackoff %v (overflow must not escape the cap)", got, time.Minute)
	}
}

// backoff(0) must be total: it must return a positive delay (BaseBackoff) and
// must not panic. RetryCount 0 is the value on every unclaimed record —
// exactly what Dispatcher.Deliver receives from the admin "test this webhook"
// endpoint (cmd/controlplane/main.go), which builds a fresh Delivery{} rather
// than going through ClaimDue.
//
// BEFORE THE FIX: `BaseBackoff << (retry - 1)` computed a shift of -1 for
// retry==0, which is a negative shift count and panics
// ("runtime error: negative shift amount") — this test panicked the test
// binary on the pre-fix backoff(). The clamp in backoff() (shift := retry-1;
// if shift<0 { shift=0 }) is what makes this pass.
func TestDispatcher_Backoff_ZeroRetryDoesNotPanic(t *testing.T) {
	d := NewDispatcher(newFakeDeliveries(), newFakeWebhooks(), newFakeOwners(), doerFunc(func(*http.Request) (*http.Response, error) { return resp(200), nil }), nil, quiet(),
		DispatcherConfig{BaseBackoff: time.Second, MaxBackoff: time.Minute})

	got := d.backoff(0) // must not panic
	if got != time.Second {
		t.Errorf("backoff(0) = %v, want %v (same as backoff(1): the first delay, never negative or zero)", got, time.Second)
	}
}

// ---- Deliver: the synchronous admin "test webhook" entry point, unclaimed ---

// The admin "test this webhook" endpoint drives Deliver directly with a fresh,
// never-claimed Delivery (RetryCount at its zero value) against whatever the
// operator is testing. If that target fails and the webhook has MaxRetries>0
// (the normal case), the dispatcher must report a retryable failure — not
// panic. This is the end-to-end shape of the bug: reproduces
// cmd/controlplane/main.go's SetWebhooks callback exactly.
func TestDispatcher_Deliver_FreshRecordAgainstFailingTargetDoesNotPanic(t *testing.T) {
	wh := newFakeWebhooks(Webhook{ID: "w1", TenantID: testTenant, URL: "http://x", Secret: "s", Enabled: true, MaxRetries: 3})
	del := newFakeDeliveries()
	doer := doerFunc(func(*http.Request) (*http.Response, error) { return resp(500), nil })
	d := NewDispatcher(del, wh, newFakeOwners(), doer, nil, quiet(), DispatcherConfig{})

	fresh := Delivery{ // exactly what the admin "test" callback builds: no claim, RetryCount 0
		ID: "test_w1", WebhookID: "w1", Status: StatusPending,
		Event: Event{TenantID: testTenant, Operation: "test", ChainID: "w1", CfgID: "w1", OccurredAt: time.Now().UTC()},
	}

	status, err := d.Deliver(context.Background(), fresh) // must not panic

	if status != StatusFailed {
		t.Fatalf("status = %q, want failed (a retryable failure, not a crash)", status)
	}
	_ = err // a plain non-2xx carries no transport error; status is the signal
}

// ---- newDeliveryID ------------------------------------------------------------

// Every delivery id is prefixed, fixed-length hex, and unique per call — the
// dispatcher's own claim/fencing tokens are keyed on it.
func TestNewDeliveryID_FormatAndUniqueness(t *testing.T) {
	a := newDeliveryID()
	b := newDeliveryID()
	if !strings.HasPrefix(a, "dlv_") {
		t.Fatalf("id %q missing dlv_ prefix", a)
	}
	if len(a) != len("dlv_")+32 { // 16 random bytes, hex-encoded
		t.Fatalf("id %q has length %d, want %d", a, len(a), len("dlv_")+32)
	}
	if a == b {
		t.Fatal("two generated ids must not collide")
	}
}
