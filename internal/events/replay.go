package events

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// ReplayConfig parameterises the replay worker.
type ReplayConfig struct {
	Interval   time.Duration // between job scans (default 3s)
	BatchLimit int           // events read per chain per page (default 500)
}

func (c *ReplayConfig) withDefaults() {
	if c.Interval <= 0 {
		c.Interval = 3 * time.Second
	}
	if c.BatchLimit <= 0 {
		c.BatchLimit = 500
	}
}

// ReplayWorker executes replay jobs. It NEVER regenerates events: time-window
// jobs read committed events from the audit log (EventSource) and re-enqueue new
// deliveries; delivery-id jobs re-send existing delivery records. Delivery to the
// endpoint is performed by the existing dispatcher — this worker only enqueues.
type ReplayWorker struct {
	jobs     ReplayJobStore
	source   EventSource
	webhooks WebhookStore
	deliv    DeliveryStore
	ops      DeliveryOps
	metrics  ConsumeMetrics
	log      *slog.Logger
	now      func() time.Time
	cfg      ReplayConfig
}

// NewReplayWorker builds the worker. All stores are required.
func NewReplayWorker(jobs ReplayJobStore, source EventSource, webhooks WebhookStore, deliv DeliveryStore, ops DeliveryOps, metrics ConsumeMetrics, log *slog.Logger, cfg ReplayConfig) *ReplayWorker {
	if metrics == nil {
		metrics = NopConsumeMetrics{}
	}
	if log == nil {
		log = slog.Default()
	}
	cfg.withDefaults()
	return &ReplayWorker{
		jobs: jobs, source: source, webhooks: webhooks, deliv: deliv, ops: ops,
		metrics: metrics, log: log, now: func() time.Time { return time.Now().UTC() }, cfg: cfg,
	}
}

// Run scans for pending jobs on Interval until ctx is cancelled.
func (w *ReplayWorker) Run(ctx context.Context) error {
	w.log.Info("replay worker started", "interval", w.cfg.Interval.String())
	w.RunOnce(ctx)
	t := time.NewTicker(w.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			w.log.Info("replay worker stopped", "reason", ctx.Err())
			return ctx.Err()
		case <-t.C:
			w.RunOnce(ctx)
		}
	}
}

// RunOnce claims and executes all pending replay jobs once.
func (w *ReplayWorker) RunOnce(ctx context.Context) {
	pending, err := w.jobs.ClaimPendingJobs(ctx, 16)
	if err != nil {
		w.log.Error("replay: claim jobs", "err", err)
		return
	}
	for i := range pending {
		if ctx.Err() != nil {
			return
		}
		w.execute(ctx, pending[i])
	}
}

func (w *ReplayWorker) execute(ctx context.Context, job ReplayJob) {
	start := w.now()
	// The claim already moved the job to running and stamped its token
	// (ADR-CONCURRENCY-007); a separate "mark running" write here is what left
	// the window in which a second worker could select the same pending job.
	// job.ClaimedAt is carried into the outcome write below, which is fenced on
	// it, so a worker that has lost the job cannot overwrite the owner's verdict.

	n, err := w.run(ctx, job)
	job.EventsReplayed = n
	job.UpdatedAt = w.now()
	if err != nil {
		job.Status = JobFailed
		job.Error = err.Error()
		w.log.Error("replay: job failed", "job_id", job.ID, "err", err)
	} else {
		job.Status = JobCompleted
	}
	// The outcome is written on a context detached from the worker's, so a
	// demotion or shutdown mid-replay cannot lose it (ADR-CONCURRENCY-008). The
	// replay has already enqueued deliveries — an effect in the outside world —
	// and this queue has no lease recovery (ADR-CONCURRENCY-007), so a lost
	// outcome leaves the job stuck in `running` permanently.
	octx, cancelOutcome := outcomeContext(ctx)
	defer cancelOutcome()
	if err := w.jobs.UpdateJob(octx, job); err != nil {
		if errors.Is(err, ErrStaleClaim) {
			// This worker no longer owns the job; the current owner records the
			// authoritative outcome (ADR-CONCURRENCY-005/007).
			w.log.Info("replay: job result fenced — claimed by another worker", "job_id", job.ID)
			return
		}
		// The replay happened but could not be recorded. Say so, and do not let
		// the success metrics below claim an outcome the database does not hold.
		w.log.Error("replay: job ran but outcome not recorded", "job_id", job.ID, "err", err)
		return
	}

	w.metrics.IncReplayJob()
	w.metrics.AddReplayEvents(n)
	w.metrics.ObserveReplayDuration(w.now().Sub(start))
}

func (w *ReplayWorker) run(ctx context.Context, job ReplayJob) (int, error) {
	if len(job.DeliveryIDs) > 0 {
		// Re-send existing deliveries by id (preserves everything).
		return w.ops.Requeue(ctx, job.WebhookID, job.DeliveryIDs)
	}
	return w.replayWindow(ctx, job)
}

// replayWindow reads committed audit events in [From,To] and re-enqueues new
// deliveries for the webhook. The event content is taken verbatim from the audit
// log — nothing is reconstructed.
func (w *ReplayWorker) replayWindow(ctx context.Context, job ReplayJob) (int, error) {
	wh, err := w.webhooks.Get(ctx, job.WebhookID)
	if err != nil {
		return 0, err
	}
	chains, err := w.source.ListChainIDs(ctx)
	if err != nil {
		return 0, err
	}
	now := w.now()
	total := 0
	for _, chainID := range chains {
		if ctx.Err() != nil {
			return total, ctx.Err()
		}
		var cursor int64
		for {
			evs, err := w.source.ListEvents(ctx, chainID, cursor, false, w.cfg.BatchLimit, "")
			if err != nil {
				return total, err
			}
			if len(evs) == 0 {
				break
			}
			var batch []Delivery
			for i := range evs {
				cursor = evs[i].Seq
				if !inWindow(evs[i].OccurredAt, job.From, job.To) {
					continue
				}
				ev := eventFrom(evs[i])
				// Replay re-enqueues committed deliveries, so it must apply the
				// same ownership rule as the original fan-out or it becomes a
				// second route to the same cross-tenant delivery.
				if !domain.SameOwner(wh, ev) || !wh.Matches(ev) {
					continue
				}
				batch = append(batch, Delivery{
					// Deterministic id: replaying the same window is idempotent,
					// re-enqueuing collides rather than duplicating (ADR-CONCURRENCY-003).
					ID: DeliveryID(wh.ID, ev.ChainID, ev.Seq), WebhookID: wh.ID, Event: ev,
					Status: StatusPending, NextAttemptAt: now, CreatedAt: now,
				})
			}
			if len(batch) > 0 {
				if err := w.deliv.Enqueue(ctx, batch); err != nil {
					return total, err
				}
				total += len(batch)
			}
			if len(evs) < w.cfg.BatchLimit {
				break
			}
		}
	}
	return total, nil
}

func inWindow(t, from, to time.Time) bool {
	if !from.IsZero() && t.Before(from) {
		return false
	}
	if !to.IsZero() && t.After(to) {
		return false
	}
	return true
}

// NewReplayJobID generates a replay job id.
func NewReplayJobID() string { return "rpl_" + randDeliveryHex() }

func randDeliveryHex() string {
	// Reuse the delivery id generator's entropy, dropping its prefix.
	id := newDeliveryID()
	return id[len("dlv_"):]
}
