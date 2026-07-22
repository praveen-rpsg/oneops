package events

import (
	"context"
	"log/slog"
	"time"
)

// RetentionConfig parameterises the retention worker.
type RetentionConfig struct {
	// Interval between cleanup passes (default 1h).
	Interval time.Duration
	// MaxAge is how long a terminal delivery is retained (default 720h/30d). Zero
	// or negative disables deletion (cleanup becomes a no-op count refresh).
	MaxAge time.Duration
}

func (c *RetentionConfig) withDefaults() {
	if c.Interval <= 0 {
		c.Interval = time.Hour
	}
}

// RetentionWorker periodically deletes terminal deliveries (delivered and
// dead_letter) older than MaxAge and refreshes the dead-letter gauge. It NEVER
// deletes pending or failed (in-flight) deliveries.
type RetentionWorker struct {
	ops     DeliveryOps
	metrics ConsumeMetrics
	log     *slog.Logger
	now     func() time.Time
	cfg     RetentionConfig
}

// NewRetentionWorker builds the worker.
func NewRetentionWorker(ops DeliveryOps, metrics ConsumeMetrics, log *slog.Logger, cfg RetentionConfig) *RetentionWorker {
	if metrics == nil {
		metrics = NopConsumeMetrics{}
	}
	if log == nil {
		log = slog.Default()
	}
	cfg.withDefaults()
	return &RetentionWorker{ops: ops, metrics: metrics, log: log, now: func() time.Time { return time.Now().UTC() }, cfg: cfg}
}

// Run cleans up on Interval until ctx is cancelled.
func (w *RetentionWorker) Run(ctx context.Context) error {
	w.log.Info("retention worker started", "interval", w.cfg.Interval.String(), "max_age", w.cfg.MaxAge.String())
	w.RunOnce(ctx)
	t := time.NewTicker(w.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			w.log.Info("retention worker stopped", "reason", ctx.Err())
			return ctx.Err()
		case <-t.C:
			w.RunOnce(ctx)
		}
	}
}

// RunOnce performs one cleanup pass and refreshes the dead-letter gauge. Only
// terminal deliveries older than MaxAge are removed; pending and failed are never
// touched.
func (w *RetentionWorker) RunOnce(ctx context.Context) {
	if w.cfg.MaxAge > 0 {
		cutoff := w.now().Add(-w.cfg.MaxAge)
		n, err := w.ops.DeleteOlderThan(ctx, cutoff, []DeliveryStatus{StatusDelivered, StatusDeadLetter})
		if err != nil {
			w.log.Error("retention: delete", "err", err)
		} else if n > 0 {
			w.log.Info("retention: pruned deliveries", "count", n, "cutoff", cutoff)
		}
	}
	if n, err := w.ops.CountByStatus(ctx, StatusDeadLetter); err == nil {
		w.metrics.SetDeadLetters(n)
	}
}
