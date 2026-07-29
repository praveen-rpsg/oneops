package events

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// DispatcherConfig parameterises delivery.
type DispatcherConfig struct {
	// Interval between delivery passes (default 2s).
	Interval time.Duration
	// BatchLimit bounds deliveries claimed per pass (default 100).
	BatchLimit int
	// RequestTimeout bounds a single POST (default 10s).
	RequestTimeout time.Duration
	// ClaimLease is how long a claimed (inflight) delivery is owned before it is
	// considered abandoned and reclaimable — the bound on re-delivery after a
	// worker crash (default 2m, comfortably above RequestTimeout).
	ClaimLease time.Duration
	// BaseBackoff is the first retry delay, doubled per retry (default 5s).
	BaseBackoff time.Duration
	// MaxBackoff caps the backoff (default 1h).
	MaxBackoff time.Duration
}

func (c *DispatcherConfig) withDefaults() {
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
}

// Dispatcher delivers due webhook deliveries: it signs the payload (HMAC-SHA256),
// POSTs it, and records the result. On a non-2xx or transport error it schedules
// an exponential-backoff retry, moving to dead-letter once a webhook's MaxRetries
// is exhausted. It reads secrets live, so secret rotation applies to pending
// retries.
type Dispatcher struct {
	deliv    DeliveryStore
	webhooks WebhookStore
	owners   EventOwnerResolver
	http     HTTPDoer
	metrics  Metrics
	log      *slog.Logger
	now      func() time.Time
	cfg      DispatcherConfig
}

// NewDispatcher builds a dispatcher. deliv, webhooks, owners and doer are
// required. owners is the authoritative source of event ownership; a dispatcher
// built without it refuses every delivery rather than trusting the queue.
func NewDispatcher(deliv DeliveryStore, webhooks WebhookStore, owners EventOwnerResolver, doer HTTPDoer, metrics Metrics, log *slog.Logger, cfg DispatcherConfig) *Dispatcher {
	if metrics == nil {
		metrics = NopMetrics{}
	}
	if log == nil {
		log = slog.Default()
	}
	cfg.withDefaults()
	return &Dispatcher{
		deliv: deliv, webhooks: webhooks, owners: owners, http: doer, metrics: metrics, log: log,
		now: func() time.Time { return time.Now().UTC() }, cfg: cfg,
	}
}

// Run delivers on Interval until ctx is cancelled, then returns ctx.Err().
func (d *Dispatcher) Run(ctx context.Context) error {
	d.log.Info("event dispatcher started", "interval", d.cfg.Interval.String())
	d.RunOnce(ctx)
	t := time.NewTicker(d.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			d.log.Info("event dispatcher stopped", "reason", ctx.Err())
			return ctx.Err()
		case <-t.C:
			d.RunOnce(ctx)
		}
	}
}

// RunOnce claims and attempts all due deliveries once.
func (d *Dispatcher) RunOnce(ctx context.Context) {
	due, err := d.deliv.ClaimDue(ctx, d.now(), d.cfg.ClaimLease, d.cfg.BatchLimit)
	if err != nil {
		d.log.Error("event dispatcher: claim due", "err", err)
		return
	}
	for i := range due {
		if ctx.Err() != nil {
			// Stopped mid-batch (demotion or shutdown). The rest of the batch was
			// claimed but never attempted, and the claim already charged each row an
			// attempt. Give those attempts back, or a few restarts would dead-letter
			// deliveries that were never sent (ADR-CONCURRENCY-006). The release runs
			// on a detached context — the worker's is already cancelled.
			rctx, cancel := outcomeContext(ctx)
			for _, rest := range due[i:] {
				if err := d.deliv.ReleaseClaim(rctx, rest.ID, rest.ClaimedAt); err != nil {
					d.log.Warn("event dispatcher: release unused claim", "delivery_id", rest.ID, "err", err)
				}
			}
			cancel()
			return
		}
		_, _ = d.attempt(ctx, due[i])
	}
}

// outcomeContext returns the context used to record an outcome the platform has
// already produced. It is deliberately detached from the worker's context.
//
// The worker's context is cancelled the instant this instance is demoted
// (ADR-CONCURRENCY-003) or the process shuts down. Writing the outcome through
// it meant that a delivery in flight across a demotion POSTed to the subscriber
// and then failed to record anything — the error was discarded, the success
// metric was incremented anyway, and the row was left claimed for reclaim and
// re-send. Proven live: the subscriber received the event, the row stayed
// `inflight` at retry_count 0. An outcome that has already happened in the
// outside world is not the worker's to forget.
//
// It keeps the worker context's values (tracing, request identity) and drops
// only its cancellation, and it carries its own deadline so a stuck database
// cannot hold a demotion or shutdown open indefinitely.
func outcomeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), outcomeWriteTimeout)
}

// outcomeWriteTimeout bounds how long recording an already-produced outcome may
// hold up a demotion or shutdown.
const outcomeWriteTimeout = 5 * time.Second

// Deliver performs a single delivery attempt for one record. It is exported so
// the admin "test" endpoint can drive one delivery synchronously. It returns the
// resulting status.
func (d *Dispatcher) Deliver(ctx context.Context, del Delivery) (DeliveryStatus, error) {
	return d.attempt(ctx, del)
}

func (d *Dispatcher) attempt(ctx context.Context, del Delivery) (DeliveryStatus, error) {
	// Every outcome is recorded on a context detached from the worker's, so a
	// demotion or shutdown mid-attempt cannot lose it (ADR-CONCURRENCY-006).
	octx, cancelOutcome := outcomeContext(ctx)
	defer cancelOutcome()

	wh, err := d.webhooks.Get(ctx, del.WebhookID)
	if err != nil {
		// The subscriber is gone; the delivery can never succeed.
		_ = d.deliv.MarkResult(octx, del.ID, del.ClaimedAt, StatusDeadLetter, del.RetryCount, 0, d.now(), time.Time{}, AttemptFacts{})
		return StatusDeadLetter, err
	}

	// The queue is storage and storage is untrusted. The delivery row carries an
	// owner label, but a label is only a claim: anyone with database write
	// access can forge a row whose label agrees with itself while its content
	// belongs to another tenant, and comparing two fields of the same forged row
	// proves nothing. Verified against the running service — such a row was
	// dispatched and delivered after an earlier fix that trusted the label.
	//
	// So ownership is re-derived from the audit log, which is append-only and
	// whose tenant_id is written inside the governance transaction
	// (ADR-AUDIT-005). That is the one owner an attacker cannot rewrite after
	// the event is committed. The delivery's own label is never consulted here.
	// Ownership is re-derived from the audit log and compared against the
	// subscription through the shared framework. The delivery row's own owner
	// label is never read: it is forgeable, and a self-consistent forged row
	// (label matching the webhook, event content belonging to another tenant)
	// was delivered against the running service before this. The check refuses
	// unless the authoritative event owner equals the webhook's owner.
	if err := domain.ResolveAndAuthorize(ctx, d.owners, wh, del.Event.ChainID, del.Event.Seq); err != nil {
		// Fail closed and dead-letter, never retry: a cross-tenant or phantom
		// delivery is never made transiently valid by trying again.
		d.log.Error("event dispatcher: refused delivery",
			"delivery_id", del.ID, "webhook_id", wh.ID,
			"chain_id", del.Event.ChainID, "seq", del.Event.Seq,
			"webhook_tenant", wh.OwnerTenantID(), "claimed_delivery_tenant", del.OwnerTenantID(),
			"err", err.Error())
		_ = d.deliv.MarkResult(octx, del.ID, del.ClaimedAt, StatusDeadLetter, del.RetryCount, 0, d.now(), time.Time{}, AttemptFacts{})
		return StatusDeadLetter, err
	}

	now := d.now()
	payload, ts, err := BuildPayload(del, now)
	if err != nil {
		d.log.Error("event dispatcher: build payload", "delivery_id", del.ID, "err", err)
		return del.Status, err
	}
	sig := Sign(wh.Secret, ts, del.ID, payload)

	code, derr := d.post(ctx, wh.URL, del, ts, sig, payload)
	d.metrics.ObserveDeliveryLatency(time.Since(now))

	if derr == nil && code >= 200 && code < 300 {
		if err := d.deliv.MarkResult(octx, del.ID, del.ClaimedAt, StatusDelivered, del.RetryCount, code, now, time.Time{}, AttemptFacts{Destination: wh.URL, SignedTS: ts}); err != nil {
			if errors.Is(err, ErrStaleClaim) {
				// This worker's lease expired and the row was reclaimed mid-flight; the
				// reclaiming worker owns the outcome. The POST still happened (the
				// receiver dedups on the stable id, ADR-CONCURRENCY-003) — but we do not
				// record it, so we never overwrite the reclaimer's state.
				d.log.Info("event dispatcher: delivery result fenced — row reclaimed by another worker", "delivery_id", del.ID)
				return StatusDelivered, nil
			}
			// The outcome happened but could not be recorded. Say so — the row will
			// be reclaimed and re-sent, and the success metric must not claim
			// otherwise (ADR-CONCURRENCY-006).
			d.log.Error("event dispatcher: delivered but outcome not recorded", "delivery_id", del.ID, "err", err)
			return StatusDelivered, err
		}
		d.metrics.IncDelivered()
		return StatusDelivered, nil
	}

	// Failure: schedule a retry or dead-letter. del.RetryCount is the attempt
	// number the claim assigned to this attempt (ADR-CONCURRENCY-006), so the
	// budget comparison is against it directly — no local increment.
	d.metrics.IncFailure()
	retry := del.RetryCount
	if retry >= wh.MaxRetries {
		_ = d.deliv.MarkResult(octx, del.ID, del.ClaimedAt, StatusDeadLetter, retry, code, now, time.Time{}, AttemptFacts{Destination: wh.URL, SignedTS: ts})
		d.log.Warn("event dispatcher: dead-letter", "delivery_id", del.ID, "webhook_id", wh.ID, "attempts", retry)
		return StatusDeadLetter, derr
	}
	d.metrics.IncRetry()
	next := now.Add(d.backoff(retry))
	if err := d.deliv.MarkResult(octx, del.ID, del.ClaimedAt, StatusFailed, retry, code, now, next, AttemptFacts{Destination: wh.URL, SignedTS: ts}); errors.Is(err, ErrStaleClaim) {
		// Reclaimed mid-flight: do not reschedule against the reclaimer's owned row.
		d.log.Info("event dispatcher: failed result fenced — row reclaimed by another worker", "delivery_id", del.ID)
	}
	return StatusFailed, derr
}

func (d *Dispatcher) post(ctx context.Context, url string, del Delivery, ts int64, sig string, payload []byte) (int, error) {
	reqCtx, cancel := context.WithTimeout(ctx, d.cfg.RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderSignature, sig)
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(HeaderDelivery, del.ID)
	req.Header.Set(HeaderEvent, EventType(del.Event.Operation))

	resp, err := d.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

func (d *Dispatcher) backoff(retry int) time.Duration {
	b := d.cfg.BaseBackoff << (retry - 1)
	if b <= 0 || b > d.cfg.MaxBackoff {
		return d.cfg.MaxBackoff
	}
	return b
}

var errNoID = errors.New("events: id generation failed")

// ErrStaleClaim is returned by MarkResult when the row was reclaimed by another
// worker before this one recorded its result: the fencing token no longer
// matches, so nothing was written. It is not a failure — the current owner will
// record the authoritative outcome — it is the signal that this worker was
// evicted mid-flight and its result must be discarded (ADR-CONCURRENCY-005).
var ErrStaleClaim = errors.New("events: delivery result fenced — row reclaimed by another worker")

func newDeliveryID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(errNoID) // crypto/rand failure is unrecoverable
	}
	return "dlv_" + hex.EncodeToString(b[:])
}
