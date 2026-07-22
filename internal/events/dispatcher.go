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
)

// DispatcherConfig parameterises delivery.
type DispatcherConfig struct {
	// Interval between delivery passes (default 2s).
	Interval time.Duration
	// BatchLimit bounds deliveries claimed per pass (default 100).
	BatchLimit int
	// RequestTimeout bounds a single POST (default 10s).
	RequestTimeout time.Duration
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
	http     HTTPDoer
	metrics  Metrics
	log      *slog.Logger
	now      func() time.Time
	cfg      DispatcherConfig
}

// NewDispatcher builds a dispatcher. deliv, webhooks, and doer are required.
func NewDispatcher(deliv DeliveryStore, webhooks WebhookStore, doer HTTPDoer, metrics Metrics, log *slog.Logger, cfg DispatcherConfig) *Dispatcher {
	if metrics == nil {
		metrics = NopMetrics{}
	}
	if log == nil {
		log = slog.Default()
	}
	cfg.withDefaults()
	return &Dispatcher{
		deliv: deliv, webhooks: webhooks, http: doer, metrics: metrics, log: log,
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
	due, err := d.deliv.ClaimDue(ctx, d.now(), d.cfg.BatchLimit)
	if err != nil {
		d.log.Error("event dispatcher: claim due", "err", err)
		return
	}
	for i := range due {
		if ctx.Err() != nil {
			return
		}
		_, _ = d.attempt(ctx, due[i])
	}
}

// Deliver performs a single delivery attempt for one record. It is exported so
// the admin "test" endpoint can drive one delivery synchronously. It returns the
// resulting status.
func (d *Dispatcher) Deliver(ctx context.Context, del Delivery) (DeliveryStatus, error) {
	return d.attempt(ctx, del)
}

func (d *Dispatcher) attempt(ctx context.Context, del Delivery) (DeliveryStatus, error) {
	wh, err := d.webhooks.Get(ctx, del.WebhookID)
	if err != nil {
		// The subscriber is gone; the delivery can never succeed.
		_ = d.deliv.MarkResult(ctx, del.ID, StatusDeadLetter, del.RetryCount, 0, d.now(), time.Time{})
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
		_ = d.deliv.MarkResult(ctx, del.ID, StatusDelivered, del.RetryCount, code, now, time.Time{})
		d.metrics.IncDelivered()
		return StatusDelivered, nil
	}

	// Failure: schedule a retry or dead-letter.
	d.metrics.IncFailure()
	retry := del.RetryCount + 1
	if retry >= wh.MaxRetries {
		_ = d.deliv.MarkResult(ctx, del.ID, StatusDeadLetter, retry, code, now, time.Time{})
		d.log.Warn("event dispatcher: dead-letter", "delivery_id", del.ID, "webhook_id", wh.ID, "retries", retry)
		return StatusDeadLetter, derr
	}
	d.metrics.IncRetry()
	next := now.Add(d.backoff(retry))
	_ = d.deliv.MarkResult(ctx, del.ID, StatusFailed, retry, code, now, next)
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

func newDeliveryID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(errNoID) // crypto/rand failure is unrecoverable
	}
	return "dlv_" + hex.EncodeToString(b[:])
}
