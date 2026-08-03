package postgres

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
)

// TelemetryRetentionConfig parameterises the retention worker (E2.1b).
type TelemetryRetentionConfig struct {
	// Interval between cleanup passes (default 1h, the same cadence
	// events.RetentionWorker uses).
	Interval time.Duration
}

func (c *TelemetryRetentionConfig) withDefaults() {
	if c.Interval <= 0 {
		c.Interval = time.Hour
	}
}

// TelemetryRetentionWorker bounds telemetry_sample and telemetry_rollup_5m's
// growth by dropping whole chunks older than a horizon (E2.1b).
//
// This is NOT TimescaleDB's add_retention_policy: that call, and the
// scheduled background job it would install, require the Timescale License.
// This platform's pinned image (timescale/timescaledb:*-oss,
// ADR-TELEMETRY-001) carries only the Apache license — verified live, see
// ADR-TELEMETRY-002. `drop_chunks` itself is Apache-licensed core hypertable
// chunk management, so this worker calls it directly, on a schedule it
// drives itself, the same shape events.RetentionWorker already uses for
// webhook deliveries.
//
// Runs on the PRIVILEGED pool: dropping a chunk is a whole-hypertable
// operation spanning every tenant's rows in that time range at once, so
// there is no tenant-scoped connection this could run on even in principle
// (see domain.TelemetryRawRetentionDaysKey's doc comment).
type TelemetryRetentionWorker struct {
	pool *pgxpool.Pool
	log  *slog.Logger
	now  func() time.Time
	cfg  TelemetryRetentionConfig
}

// NewTelemetryRetentionWorker builds the worker over the privileged pool.
func NewTelemetryRetentionWorker(pool *pgxpool.Pool, log *slog.Logger, cfg TelemetryRetentionConfig) *TelemetryRetentionWorker {
	if log == nil {
		log = slog.Default()
	}
	cfg.withDefaults()
	return &TelemetryRetentionWorker{pool: pool, log: log, now: func() time.Time { return time.Now().UTC() }, cfg: cfg}
}

// Run drops old chunks on Interval until ctx is cancelled.
func (w *TelemetryRetentionWorker) Run(ctx context.Context) error {
	w.log.Info("telemetry retention worker started", "interval", w.cfg.Interval.String())
	w.RunOnce(ctx)
	t := time.NewTicker(w.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			w.log.Info("telemetry retention worker stopped", "reason", ctx.Err())
			return ctx.Err()
		case <-t.C:
			w.RunOnce(ctx)
		}
	}
}

// RunOnce drops chunks older than the currently effective horizons: the raw
// table's is operator-tunable (domain.TelemetryRawRetentionDaysKey, read
// fresh on every pass so a Setting change takes effect without a restart);
// the rollup's is the fixed domain.DefaultTelemetryRollupRetentionDays — see
// that constant's doc comment for why only the raw horizon is tunable.
func (w *TelemetryRetentionWorker) RunOnce(ctx context.Context) {
	rawDays, err := w.effectiveRawRetentionDays(ctx)
	if err != nil {
		w.log.Error("telemetry retention: resolve raw retention", "err", err)
		rawDays = domain.DefaultTelemetryRawRetentionDays
	}
	w.dropOldChunks(ctx, "telemetry_sample", rawDays)
	w.dropOldChunks(ctx, "telemetry_rollup_5m", domain.DefaultTelemetryRollupRetentionDays)
}

// effectiveRawRetentionDays reads the SystemTenantID override of
// domain.TelemetryRawRetentionDaysKey, falling back to
// domain.DefaultTelemetryRawRetentionDays when unset. It queries directly
// rather than through SettingStore/SettingRepository because that interface
// is tenant-scoped-by-connection (ADR-TENANCY-002) and this worker
// deliberately reads exactly one named tenant's row from the privileged
// pool, not "the bound connection's tenant".
func (w *TelemetryRetentionWorker) effectiveRawRetentionDays(ctx context.Context) (int, error) {
	def := domain.SettingDefinitions[domain.TelemetryRawRetentionDaysKey]
	var raw string
	err := w.pool.QueryRow(ctx,
		`SELECT value FROM setting WHERE tenant_id = $1 AND key = $2`,
		domain.SystemTenantID, domain.TelemetryRawRetentionDaysKey).Scan(&raw)
	switch {
	case err == pgx.ErrNoRows:
		raw = def.Default
	case err != nil:
		return 0, fmt.Errorf("read %s: %w", domain.TelemetryRawRetentionDaysKey, err)
	}
	n, ok := def.DecodeValue(raw).(int64)
	if !ok || n < def.Min {
		// A stored value SettingStore.Upsert would already have refused to
		// write; reaching this means the row predates a bound tightening, not
		// a live bypass. Fail closed toward the shorter, safer default rather
		// than toward "retain forever".
		return domain.DefaultTelemetryRawRetentionDays, nil
	}
	return int(n), nil
}

// dropOldChunks drops every chunk of table whose entire range is older than
// days, logging what it removed. drop_chunks is schema-qualified as
// public.drop_chunks for the same reason ADR-TELEMETRY-001 qualifies
// create_hypertable: this repository's integration suite runs several
// packages' migrations — and, for this worker, their queries — concurrently
// against private schemas whose search_path does not include public, where
// the timescaledb extension's functions actually live.
//
// older_than is built with make_interval(days => $2), not `($2 || '
// days')::interval` — the string-concatenation form left pgx's parameter
// planner unable to tell $2 was an integer (Postgres's own `describe` step
// resolves the `||` operator's right-hand unknown-typed placeholder to
// `text`, so pgx tried to encode a Go int into a text parameter and every
// call failed with "cannot find encode plan", silently, because the caller
// only logs the error). make_interval takes an actual integer parameter, so
// there is no operator to resolve ambiguously. Caught by
// TestTelemetryRetentionWorker_DropsOldRollupChunks, which failed exactly
// this way before this fix.
func (w *TelemetryRetentionWorker) dropOldChunks(ctx context.Context, table string, days int) {
	rows, err := w.pool.Query(ctx,
		`SELECT public.drop_chunks($1::regclass, older_than => make_interval(days => $2))`, table, days)
	if err != nil {
		w.log.Error("telemetry retention: drop_chunks", "table", table, "err", err)
		return
	}
	defer rows.Close()
	dropped := 0
	for rows.Next() {
		var chunk string
		if err := rows.Scan(&chunk); err != nil {
			w.log.Error("telemetry retention: scan dropped chunk", "table", table, "err", err)
			return
		}
		dropped++
	}
	if err := rows.Err(); err != nil {
		w.log.Error("telemetry retention: iterate dropped chunks", "table", table, "err", err)
		return
	}
	if dropped > 0 {
		w.log.Info("telemetry retention: dropped chunks", "table", table, "count", dropped, "older_than_days", days)
	}
}
