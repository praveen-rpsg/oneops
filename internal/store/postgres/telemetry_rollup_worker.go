package postgres

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// telemetryRollupBucketInterval is the SQL interval literal for
// domain.TelemetryRollupBucket. Kept as a separate constant, rather than
// formatted from the Duration at call time, so the SQL text is grep-able and
// obviously matches the migration's `telemetry_rollup_5m` name.
const telemetryRollupBucketInterval = "5 minutes"

// TelemetryRollupConfig parameterises the rollup worker (E2.1b).
type TelemetryRollupConfig struct {
	// Interval between materialization passes (default 5m — the bucket width
	// itself, so no bucket waits more than one pass to appear in the rollup).
	Interval time.Duration
	// Window is how far back each pass re-materializes, trailing "now"
	// (default 24h).
	Window time.Duration
	// Lag excludes the most recent samples from materialization so a bucket
	// still receiving late writes is not rolled up prematurely (default 2m).
	Lag time.Duration
}

func (c *TelemetryRollupConfig) withDefaults() {
	if c.Interval <= 0 {
		c.Interval = 5 * time.Minute
	}
	if c.Window <= 0 {
		c.Window = 24 * time.Hour
	}
	if c.Lag <= 0 {
		c.Lag = 2 * time.Minute
	}
}

// TelemetryRollupWorker populates telemetry_rollup_5m from telemetry_sample
// (E2.1b downsampling).
//
// This is an application-level substitute for TimescaleDB's continuous
// aggregate, not that feature itself: `CREATE MATERIALIZED VIEW ... WITH
// (timescaledb.continuous)` and `add_continuous_aggregate_policy` both
// require the Timescale License, which this platform's pinned image
// (timescale/timescaledb:*-oss) does not carry — verified live, see
// ADR-TELEMETRY-002. telemetry_rollup_5m is instead a table this platform
// owns outright, carrying the SAME tenant_id/RLS discipline every other
// tenant-owned table does, and this worker keeps it populated the way
// TimescaleDB's own background job would for a real continuous aggregate.
//
// Each pass re-materializes a TRAILING WINDOW (RunOnce) rather than tracking
// a durable watermark: the upsert is idempotent (ON CONFLICT ... DO UPDATE),
// so recomputing a bucket that has not changed is a wasted write, not a
// correctness risk, and this makes the worker self-healing across restarts
// and late-arriving data without a separate cursor table to keep consistent.
//
// Runs on the PRIVILEGED pool: it aggregates every tenant's rows into every
// tenant's rollup rows from one process in one pass, the same category of
// cross-tenant background worker the webhook dispatcher and policy consumer
// already are (arch.TestServerWiringUsesTenantScopedPool's "background
// workers are unconstrained" note). It takes no action keyed by what it
// reads — it aggregates rows and writes rows carrying the same tenant_id
// they already carried in telemetry_sample — so ADR-TENANCY-009's
// re-derive-ownership rule, written for a consumer that ACTS on another
// tenant's behalf (an outbound delivery, a policy execution), has nothing to
// re-derive here: tenant_id flows straight through, never chosen by the
// worker.
type TelemetryRollupWorker struct {
	pool *pgxpool.Pool
	log  *slog.Logger
	now  func() time.Time
	cfg  TelemetryRollupConfig
}

// NewTelemetryRollupWorker builds the worker over the privileged pool.
func NewTelemetryRollupWorker(pool *pgxpool.Pool, log *slog.Logger, cfg TelemetryRollupConfig) *TelemetryRollupWorker {
	if log == nil {
		log = slog.Default()
	}
	cfg.withDefaults()
	return &TelemetryRollupWorker{pool: pool, log: log, now: func() time.Time { return time.Now().UTC() }, cfg: cfg}
}

// Run materializes on Interval until ctx is cancelled.
func (w *TelemetryRollupWorker) Run(ctx context.Context) error {
	w.log.Info("telemetry rollup worker started", "interval", w.cfg.Interval.String(), "window", w.cfg.Window.String())
	w.RunOnce(ctx)
	t := time.NewTicker(w.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			w.log.Info("telemetry rollup worker stopped", "reason", ctx.Err())
			return ctx.Err()
		case <-t.C:
			w.RunOnce(ctx)
		}
	}
}

// RunOnce re-materializes the trailing [now-Lag-Window, now-Lag) window.
func (w *TelemetryRollupWorker) RunOnce(ctx context.Context) {
	upper := w.now().Add(-w.cfg.Lag)
	lower := upper.Add(-w.cfg.Window)
	n, err := w.MaterializeRange(ctx, lower, upper)
	if err != nil {
		w.log.Error("telemetry rollup: materialize", "err", err)
		return
	}
	if n > 0 {
		w.log.Info("telemetry rollup: materialized buckets", "count", n, "from", lower, "to", upper)
	}
}

// MaterializeRange recomputes every 5-minute bucket with at least one raw
// sample timestamp in [from, to) and upserts it into telemetry_rollup_5m,
// returning the number of bucket-rows written or refreshed. Idempotent:
// re-running over an overlapping range recomputes the same buckets to the
// same values.
//
// public.time_bucket is schema-qualified for the same reason
// ADR-TELEMETRY-001 qualifies every other timescaledb call: this
// repository's integration suite runs each package's queries against a
// private schema whose search_path does not include public, where the
// timescaledb extension's functions actually live.
func (w *TelemetryRollupWorker) MaterializeRange(ctx context.Context, from, to time.Time) (int64, error) {
	tag, err := w.pool.Exec(ctx, `
		INSERT INTO telemetry_rollup_5m (tenant_id, asset_id, metric, bucket, avg_value, min_value, max_value, sample_count)
		SELECT tenant_id, asset_id, metric, public.time_bucket($3::interval, ts) AS bucket,
		       avg(value), min(value), max(value), count(*)
		  FROM telemetry_sample
		 WHERE ts >= $1 AND ts < $2
		 GROUP BY tenant_id, asset_id, metric, bucket
		ON CONFLICT (tenant_id, asset_id, metric, bucket) DO UPDATE
		   SET avg_value = EXCLUDED.avg_value, min_value = EXCLUDED.min_value,
		       max_value = EXCLUDED.max_value, sample_count = EXCLUDED.sample_count`,
		from, to, telemetryRollupBucketInterval)
	if err != nil {
		return 0, fmt.Errorf("materialize telemetry rollup: %w", err)
	}
	return tag.RowsAffected(), nil
}
