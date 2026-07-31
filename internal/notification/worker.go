package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/policy"
)

// HTTPDoer is the minimal HTTP client the webhook channel needs (satisfied by
// *http.Client). The worker never constructs its own client: the caller must
// inject one built by safehttp.Client, so an outbound request to a
// tenant-supplied recipient URL is subject to the SSRF guard
// (ADR-SECURITY-001) exactly as webhook and policy HTTP actions are — this
// package does not, and must not, reimplement that check.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// emptyConfig is passed to policy.EmailSender.Send: the sender's config
// argument carries per-action template/routing configuration, which is
// out of scope here (deferred — see the package doc). A notification's own
// Subject/Body already carry everything the sender needs, via Event.Metadata.
var emptyConfig = json.RawMessage(`{}`)

// WorkerConfig parameterises delivery. Mirrors events.DispatcherConfig.
type WorkerConfig struct {
	// Interval between delivery passes (default 2s).
	Interval time.Duration
	// BatchLimit bounds notifications claimed per pass (default 100).
	BatchLimit int
	// RequestTimeout bounds a single webhook-channel POST (default 10s).
	RequestTimeout time.Duration
	// ClaimLease is how long a claim is owned before it is considered abandoned
	// and reclaimable (default 2m).
	ClaimLease time.Duration
	// BaseBackoff is the first retry delay, doubled per retry (default 5s).
	BaseBackoff time.Duration
	// MaxBackoff caps the backoff (default 1h).
	MaxBackoff time.Duration
	// MaxAttempts bounds how many times a notification is attempted before its
	// retry budget is exhausted (default 5).
	MaxAttempts int
}

func (c *WorkerConfig) withDefaults() {
	if c.Interval <= 0 {
		c.Interval = 2 * time.Second
	}
	if c.BatchLimit <= 0 {
		c.BatchLimit = 100
	}
	if c.RequestTimeout <= 0 {
		c.RequestTimeout = 10 * time.Second
	}
	if c.ClaimLease <= 0 {
		c.ClaimLease = 2 * time.Minute
	}
	if c.BaseBackoff <= 0 {
		c.BaseBackoff = 5 * time.Second
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = time.Hour
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 5
	}
}

// Worker delivers due notifications at least once, with capped exponential
// backoff on failure, mirroring events.Dispatcher's claim/attempt/fence shape.
//
//   - inapp is persisted only: there is nothing to send, so a claim is marked
//     sent immediately.
//   - email routes through the injected policy.EmailSender (interface only; a
//     nil sender fails every attempt, exactly like policy.EmailAction).
//   - webhook POSTs through the injected HTTPDoer, expected to be an
//     SSRF-safe client (see HTTPDoer).
type Worker struct {
	store   Store
	email   policy.EmailSender
	http    HTTPDoer
	metrics Metrics
	log     *slog.Logger
	now     func() time.Time
	cfg     WorkerConfig
}

// NewWorker builds a worker. store is required; email and http may be nil.
func NewWorker(store Store, email policy.EmailSender, doer HTTPDoer, metrics Metrics, log *slog.Logger, cfg WorkerConfig) *Worker {
	if metrics == nil {
		metrics = NopMetrics{}
	}
	if log == nil {
		log = slog.Default()
	}
	cfg.withDefaults()
	return &Worker{
		store: store, email: email, http: doer, metrics: metrics, log: log,
		now: func() time.Time { return time.Now().UTC() }, cfg: cfg,
	}
}

// Run delivers on Interval until ctx is cancelled, then returns ctx.Err().
func (w *Worker) Run(ctx context.Context) error {
	w.log.Info("notification worker started", "interval", w.cfg.Interval.String())
	w.RunOnce(ctx)
	t := time.NewTicker(w.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			w.log.Info("notification worker stopped", "reason", ctx.Err())
			return ctx.Err()
		case <-t.C:
			w.RunOnce(ctx)
		}
	}
}

// RunOnce claims and attempts all due notifications once.
func (w *Worker) RunOnce(ctx context.Context) {
	due, err := w.store.ClaimDue(ctx, w.now(), w.cfg.ClaimLease, w.cfg.BatchLimit)
	if err != nil {
		w.log.Error("notification worker: claim due", "err", err)
		return
	}
	for i := range due {
		if ctx.Err() != nil {
			// Stopped mid-batch (demotion or shutdown). The rest of the batch was
			// claimed but never attempted, and the claim already charged each row an
			// attempt (ADR-CONCURRENCY-006). Give those attempts back, or a few
			// restarts would exhaust the retry budget of notifications that were
			// never sent. Detached from the worker's context, which is already
			// cancelled (mirrors events.Dispatcher.RunOnce).
			rctx, cancel := outcomeContext(ctx)
			for _, rest := range due[i:] {
				if err := w.store.ReleaseClaim(rctx, rest.NotificationID, rest.ClaimedAt); err != nil {
					w.log.Warn("notification worker: release unused claim", "notification_id", rest.NotificationID, "err", err)
				}
			}
			cancel()
			return
		}
		_, _ = w.attempt(ctx, due[i])
	}
}

// outcomeContext returns the context used to record an outcome the platform
// has already produced, detached from the worker's context for the same
// reason events.outcomeContext is: a demotion or shutdown mid-attempt must not
// lose an outcome that has already happened in the outside world.
func outcomeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), outcomeWriteTimeout)
}

const outcomeWriteTimeout = 5 * time.Second

// attempt performs a single delivery attempt for one notification, routed by
// channel, and returns the resulting status.
func (w *Worker) attempt(ctx context.Context, n *domain.Notification) (domain.NotificationStatus, error) {
	octx, cancel := outcomeContext(ctx)
	defer cancel()

	switch n.Channel {
	case domain.NotificationInApp:
		return w.finishSent(octx, n)
	case domain.NotificationEmail:
		return w.deliverEmail(ctx, octx, n)
	case domain.NotificationWebhook:
		return w.deliverWebhook(ctx, octx, n)
	default:
		// Not reachable through Validate, which refuses an unknown channel before
		// a row is ever persisted — but a worker must never retry data it cannot
		// interpret, since retrying cannot fix it. Terminal, not a transient
		// failure.
		w.log.Error("notification worker: unknown channel, terminal", "notification_id", n.NotificationID, "channel", n.Channel)
		return w.markFailed(octx, n, "unknown channel", time.Time{})
	}
}

func (w *Worker) deliverEmail(ctx, octx context.Context, n *domain.Notification) (domain.NotificationStatus, error) {
	if w.email == nil {
		return w.retryOrExhaust(octx, n, "notification worker: no email sender configured")
	}
	// Reusing policy.EmailSender's shape as-is: its config argument is a
	// per-action template/routing configuration, out of scope here (deferred —
	// see the package doc), so the notification's own Subject/Body travel as
	// Metadata instead.
	ev := policy.Event{
		EventID: n.NotificationID, Operation: "notification", CfgID: n.NotificationID,
		Actor: n.Recipient, OccurredAt: n.CreatedAt,
		Metadata: map[string]string{"recipient": n.Recipient, "subject": n.Subject, "body": n.Body},
	}
	if err := w.email.Send(ctx, ev, emptyConfig); err != nil {
		return w.retryOrExhaust(octx, n, err.Error())
	}
	return w.finishSent(octx, n)
}

// webhookPayload is what a webhook-channel recipient receives. Unlike
// events.Delivery's subscriber webhooks it carries no HMAC signature: it is
// not a subscription to platform events, it is a direct notification to an
// operator-configured endpoint (the same trust boundary policy.HTTPAction's
// recipient sits behind).
type webhookPayload struct {
	NotificationID string    `json:"notification_id"`
	Recipient      string    `json:"recipient"`
	Subject        string    `json:"subject"`
	Body           string    `json:"body"`
	OccurredAt     time.Time `json:"occurred_at"`
}

func (w *Worker) deliverWebhook(ctx, octx context.Context, n *domain.Notification) (domain.NotificationStatus, error) {
	if w.http == nil {
		return w.retryOrExhaust(octx, n, "notification worker: no HTTP client configured")
	}
	payload, err := json.Marshal(webhookPayload{
		NotificationID: n.NotificationID, Recipient: n.Recipient,
		Subject: n.Subject, Body: n.Body, OccurredAt: n.CreatedAt,
	})
	if err != nil {
		return w.retryOrExhaust(octx, n, err.Error())
	}
	reqCtx, cancel := context.WithTimeout(ctx, w.cfg.RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, n.Recipient, bytes.NewReader(payload))
	if err != nil {
		return w.retryOrExhaust(octx, n, err.Error())
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.http.Do(req)
	if err != nil {
		return w.retryOrExhaust(octx, n, err.Error())
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return w.retryOrExhaust(octx, n, fmt.Sprintf("status %d", resp.StatusCode))
	}
	return w.finishSent(octx, n)
}

func (w *Worker) finishSent(octx context.Context, n *domain.Notification) (domain.NotificationStatus, error) {
	if err := w.store.MarkSent(octx, n.NotificationID, n.ClaimedAt, w.now()); err != nil {
		if errors.Is(err, ErrStaleClaim) {
			// This worker's lease expired and the row was reclaimed mid-flight; the
			// reclaiming worker owns the outcome. The send still happened — but we
			// do not record it, so we never overwrite the reclaimer's state
			// (ADR-CONCURRENCY-005).
			w.log.Info("notification worker: sent result fenced — row reclaimed by another worker", "notification_id", n.NotificationID)
			return domain.NotificationSent, nil
		}
		w.log.Error("notification worker: sent but outcome not recorded", "notification_id", n.NotificationID, "err", err)
		return domain.NotificationSent, err
	}
	w.metrics.IncDelivered()
	return domain.NotificationSent, nil
}

// retryOrExhaust schedules a retry, or exhausts the retry budget once
// n.Attempts (already charged by ClaimDue, ADR-CONCURRENCY-006) reaches
// MaxAttempts — the budget comparison needs no local increment.
func (w *Worker) retryOrExhaust(octx context.Context, n *domain.Notification, reason string) (domain.NotificationStatus, error) {
	var next time.Time
	if n.Attempts < w.cfg.MaxAttempts {
		next = w.now().Add(w.backoff(n.Attempts))
	}
	return w.markFailed(octx, n, reason, next)
}

func (w *Worker) markFailed(octx context.Context, n *domain.Notification, reason string, next time.Time) (domain.NotificationStatus, error) {
	w.metrics.IncFailed()
	if err := w.store.MarkFailed(octx, n.NotificationID, n.ClaimedAt, reason, next); err != nil {
		if errors.Is(err, ErrStaleClaim) {
			w.log.Info("notification worker: failed result fenced — row reclaimed by another worker", "notification_id", n.NotificationID)
			return domain.NotificationFailed, nil
		}
		w.log.Error("notification worker: failed but outcome not recorded", "notification_id", n.NotificationID, "err", err)
		return domain.NotificationFailed, err
	}
	return domain.NotificationFailed, nil
}

// backoff computes the delay before the next attempt. attempts is normally
// >= 1 (ClaimDue always charges one before an attempt is made). shift must not
// go negative — the exact panic ADR-CONCURRENCY's dispatcher/executor fix
// closed for retry==0 — so it is clamped at 0 the same way.
func (w *Worker) backoff(attempts int) time.Duration {
	shift := attempts - 1
	if shift < 0 {
		shift = 0
	}
	b := w.cfg.BaseBackoff << shift
	if b <= 0 || b > w.cfg.MaxBackoff {
		return w.cfg.MaxBackoff
	}
	return b
}
