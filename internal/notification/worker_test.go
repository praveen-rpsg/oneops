package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/policy"
)

func capturingLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

// ---- inapp: no external send ------------------------------------------------

func TestWorker_InApp_MarkedSentWithNoExternalCall(t *testing.T) {
	store := newFakeStore(newPending("n1", domain.NotificationInApp, "alice", time.Now()))
	w := NewWorker(store, nil, doerFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("inapp must never make an outbound call")
		return resp(200), nil
	}), nil, quiet(), WorkerConfig{})

	w.RunOnce(context.Background())

	got := store.get("n1")
	if got.Status != domain.NotificationSent {
		t.Fatalf("status = %q, want sent", got.Status)
	}
	if got.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", got.Attempts)
	}
}

// ---- webhook channel: success and retry -------------------------------------

func TestWorker_Webhook_SuccessMarksSent(t *testing.T) {
	store := newFakeStore(newPending("n1", domain.NotificationWebhook, "http://example.test/hook", time.Now()))
	var gotBody []byte
	doer := doerFunc(func(r *http.Request) (*http.Response, error) {
		gotBody, _ = readAll(r.Body)
		return resp(200), nil
	})
	m := &countMetrics{}
	w := NewWorker(store, nil, doer, m, quiet(), WorkerConfig{})

	w.RunOnce(context.Background())

	got := store.get("n1")
	if got.Status != domain.NotificationSent {
		t.Fatalf("status = %q, want sent", got.Status)
	}
	if m.delivered != 1 {
		t.Errorf("delivered metric = %d, want 1", m.delivered)
	}
	if !strings.Contains(string(gotBody), "n1") {
		t.Errorf("payload = %s, want it to carry the notification id", gotBody)
	}
}

func TestWorker_Webhook_FailureSchedulesRetryUntilExhausted(t *testing.T) {
	store := newFakeStore(newPending("n1", domain.NotificationWebhook, "http://example.test/hook", time.Now()))
	doer := doerFunc(func(*http.Request) (*http.Response, error) { return resp(500), nil })
	m := &countMetrics{}
	w := NewWorker(store, nil, doer, m, quiet(), WorkerConfig{BaseBackoff: time.Millisecond, MaxAttempts: 2})

	// First attempt: failed, budget not exhausted (Attempts=1 < MaxAttempts=2) —
	// a retry is scheduled, not a terminal failure. This is the test that bites:
	// before the retry-vs-exhaust comparison used the claimed Attempts directly,
	// an off-by-one here either retried forever or dead-lettered on the first
	// failure.
	w.RunOnce(context.Background())
	got := store.get("n1")
	if got.Status != domain.NotificationFailed {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	if got.NextAttemptAt.IsZero() {
		t.Fatal("a notification below its retry budget must have a scheduled next attempt, not be terminal")
	}
	if got.Terminal() {
		t.Fatal("one failed attempt with budget remaining must not be terminal")
	}

	// Second attempt (budget now exhausted at Attempts=2): terminal, no further
	// attempt is ever scheduled again.
	time.Sleep(20 * time.Millisecond)
	w.RunOnce(context.Background())
	got = store.get("n1")
	if got.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", got.Attempts)
	}
	if !got.Terminal() {
		t.Fatal("a notification at its retry budget must be terminal (NextAttemptAt zero)")
	}
	if m.failed != 2 {
		t.Errorf("failed metric = %d, want 2", m.failed)
	}

	// A third pass must not claim it again — it is exhausted, not merely due.
	w.RunOnce(context.Background())
	if got := store.get("n1"); got.Attempts != 2 {
		t.Fatalf("an exhausted notification must never be claimed again; attempts = %d", got.Attempts)
	}
}

// ---- email channel -----------------------------------------------------------

type fakeEmailSender struct {
	err error
}

func (f fakeEmailSender) Send(context.Context, policy.Event, json.RawMessage) error { return f.err }

func TestWorker_Email_NoSenderConfiguredRetries(t *testing.T) {
	store := newFakeStore(newPending("n1", domain.NotificationEmail, "user@example.com", time.Now()))
	w := NewWorker(store, nil, nil, nil, quiet(), WorkerConfig{BaseBackoff: time.Millisecond, MaxAttempts: 5})

	w.RunOnce(context.Background())

	got := store.get("n1")
	if got.Status != domain.NotificationFailed {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	if got.LastError == "" {
		t.Error("last_error must explain why: no sender configured")
	}
}

func TestWorker_Email_SenderSuccessMarksSent(t *testing.T) {
	store := newFakeStore(newPending("n1", domain.NotificationEmail, "user@example.com", time.Now()))
	w := NewWorker(store, fakeEmailSender{}, nil, nil, quiet(), WorkerConfig{})

	w.RunOnce(context.Background())

	if got := store.get("n1").Status; got != domain.NotificationSent {
		t.Fatalf("status = %q, want sent", got)
	}
}

// ---- fencing: at-least-once, never amplified --------------------------------

func TestWorker_Attempt_SentFencedByReclaimDoesNotDoubleCount(t *testing.T) {
	store := newFakeStore(newPending("n1", domain.NotificationInApp, "alice", time.Now()))
	store.staleIDs = map[string]bool{"n1": true}
	m := &countMetrics{}
	w := NewWorker(store, nil, nil, m, quiet(), WorkerConfig{})

	w.RunOnce(context.Background())

	if m.delivered != 0 {
		t.Errorf("delivered metric = %d, want 0 — a fenced write must not double-count", m.delivered)
	}
}

func TestWorker_Attempt_FailedFencedByReclaimIsNotFatal(t *testing.T) {
	store := newFakeStore(newPending("n1", domain.NotificationWebhook, "http://example.test/hook", time.Now()))
	store.staleIDs = map[string]bool{"n1": true}
	doer := doerFunc(func(*http.Request) (*http.Response, error) { return resp(500), nil })
	log, buf := capturingLogger()
	w := NewWorker(store, nil, doer, nil, log, WorkerConfig{BaseBackoff: time.Millisecond})

	w.RunOnce(context.Background()) // must not panic

	if !strings.Contains(buf.String(), "fenced") {
		t.Errorf("fenced write was not logged: %s", buf.String())
	}
}

// ---- RunOnce: cancellation mid-batch releases unattempted claims ------------

func TestWorker_RunOnce_CtxCancelMidBatchReleasesUnattemptedClaims(t *testing.T) {
	now := time.Now()
	store := newFakeStore(
		newPending("n1", domain.NotificationInApp, "a", now),
		newPending("n2", domain.NotificationInApp, "b", now),
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled: nothing must be attempted, everything released
	w := NewWorker(store, nil, nil, nil, quiet(), WorkerConfig{})

	w.RunOnce(ctx)

	for _, n := range store.all() {
		if n.Status != domain.NotificationPending || n.Attempts != 0 {
			t.Errorf("%s: status=%q attempts=%d, want released to pending with attempt given back", n.NotificationID, n.Status, n.Attempts)
		}
	}
}

func TestWorker_RunOnce_ClaimErrorHaltsWithoutAttempting(t *testing.T) {
	store := newFakeStore()
	store.claimErr = errors.New("db unavailable")
	called := false
	w := NewWorker(store, nil, doerFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return resp(200), nil
	}), nil, quiet(), WorkerConfig{})

	w.RunOnce(context.Background()) // must not panic

	if called {
		t.Fatal("a claim-pass error must not be followed by any delivery attempt")
	}
}

// ---- backoff -------------------------------------------------------------

func TestWorker_Backoff_ZeroAttemptsDoesNotPanic(t *testing.T) {
	w := NewWorker(newFakeStore(), nil, nil, nil, quiet(), WorkerConfig{BaseBackoff: time.Second, MaxBackoff: time.Minute})
	if got := w.backoff(0); got != time.Second {
		t.Errorf("backoff(0) = %v, want %v", got, time.Second)
	}
}

func TestWorker_Backoff_CapsAtMaxBackoff(t *testing.T) {
	w := NewWorker(newFakeStore(), nil, nil, nil, quiet(), WorkerConfig{BaseBackoff: time.Second, MaxBackoff: time.Minute})
	if got := w.backoff(100); got != time.Minute {
		t.Errorf("backoff(100) = %v, want capped at %v (overflow must not escape the cap)", got, time.Minute)
	}
}

// ---- Run: clean shutdown ---------------------------------------------------

func TestWorker_Run_ReturnsCtxErrOnCancel(t *testing.T) {
	w := NewWorker(newFakeStore(), nil, nil, nil, quiet(), WorkerConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

// ---- helpers ----------------------------------------------------------------

type countMetrics struct{ delivered, failed int }

func (m *countMetrics) IncDelivered() { m.delivered++ }
func (m *countMetrics) IncFailed()    { m.failed++ }

func readAll(r interface{ Read([]byte) (int, error) }) ([]byte, error) {
	var buf bytes.Buffer
	tmp := make([]byte, 4096)
	for {
		n, err := r.Read(tmp)
		buf.Write(tmp[:n])
		if err != nil {
			break
		}
	}
	return buf.Bytes(), nil
}
