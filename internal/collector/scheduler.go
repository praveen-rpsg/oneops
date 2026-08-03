package collector

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// Config parameterises the scheduler. Mirrors notification.WorkerConfig's
// shape.
type Config struct {
	// Interval between due-scan passes (default 15s).
	Interval time.Duration
	// BatchLimit bounds due checks claimed per pass (default 200).
	BatchLimit int
	// Concurrency bounds how many checks execute at once within one pass
	// (default 20) — the "bounded concurrency" requirement: a pass with more
	// due checks than this runs them in waves, never all at once.
	Concurrency int
	// CheckTimeout bounds a single check's HTTP round trip (default 10s) —
	// the "a slow/hanging target must not stall the scheduler" requirement:
	// one check can cost at most this long, regardless of how unresponsive
	// its target is.
	CheckTimeout time.Duration
}

func (c *Config) withDefaults() {
	if c.Interval <= 0 {
		c.Interval = 15 * time.Second
	}
	if c.BatchLimit <= 0 {
		c.BatchLimit = 200
	}
	if c.Concurrency <= 0 {
		c.Concurrency = 20
	}
	if c.CheckTimeout <= 0 {
		c.CheckTimeout = 10 * time.Second
	}
}

// Scheduler is the leader-gated poller: on each tick it due-scans, executes
// due checks with bounded concurrency, writes results as telemetry samples,
// and records each check's last run — mirrors notification.Worker/
// events.Dispatcher's shape, with a due-scan (Store.DueChecks) standing in
// for their atomic claim (see Store's doc comment for why that is sufficient
// here).
type Scheduler struct {
	store     Store
	telemetry domain.TelemetryRepository
	http      HTTPDoer
	metrics   Metrics
	log       *slog.Logger
	now       func() time.Time
	cfg       Config
}

// NewScheduler builds a Scheduler. store and telemetry are required; http
// may be nil (every "http" check then reports down — see runHTTP).
func NewScheduler(store Store, telemetry domain.TelemetryRepository, doer HTTPDoer, metrics Metrics, log *slog.Logger, cfg Config) *Scheduler {
	if metrics == nil {
		metrics = NopMetrics{}
	}
	if log == nil {
		log = slog.Default()
	}
	cfg.withDefaults()
	return &Scheduler{
		store: store, telemetry: telemetry, http: doer, metrics: metrics, log: log,
		now: func() time.Time { return time.Now().UTC() }, cfg: cfg,
	}
}

// Run due-scans on Interval until ctx is cancelled, then returns ctx.Err() —
// the same tick shape as TelemetryRollupWorker.Run and notification.Worker.Run.
func (s *Scheduler) Run(ctx context.Context) error {
	s.log.Info("collector scheduler started", "interval", s.cfg.Interval.String(), "concurrency", s.cfg.Concurrency)
	s.RunOnce(ctx)
	t := time.NewTicker(s.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			s.log.Info("collector scheduler stopped", "reason", ctx.Err())
			return ctx.Err()
		case <-t.C:
			s.RunOnce(ctx)
		}
	}
}

// RunOnce due-scans and executes every due check once. It is synchronous —
// Run does not tick again until this returns — so no pass can overlap the
// next one; combined with bounded per-check concurrency and CheckTimeout, one
// pass costs at most roughly (len(due)/Concurrency)*CheckTimeout, never
// unbounded.
func (s *Scheduler) RunOnce(ctx context.Context) {
	due, err := s.store.DueChecks(ctx, s.now(), s.cfg.BatchLimit)
	if err != nil {
		s.log.Error("collector scheduler: due-scan", "err", err)
		return
	}
	if len(due) == 0 {
		return
	}

	sem := make(chan struct{}, s.cfg.Concurrency)
	var wg sync.WaitGroup
	for _, c := range due {
		if ctx.Err() != nil {
			// Stopped mid-batch (demotion or shutdown). The checks not yet
			// launched simply remain due — no claim was taken on them (see
			// Store's doc comment), so the next leader's first pass picks
			// them up; nothing to release.
			break
		}
		c := c
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			s.runOne(ctx, c)
		}()
	}
	wg.Wait()
}

// outcomeContext detaches a result write from ctx's own cancellation, the
// same reasoning notification.outcomeContext/events.outcomeContext give: a
// demotion or shutdown mid-check must not lose a result that already
// happened in the outside world (the HTTP request was already sent).
func outcomeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), outcomeWriteTimeout)
}

const outcomeWriteTimeout = 5 * time.Second

// runOne executes one due check by Type dispatch and records the outcome.
func (s *Scheduler) runOne(ctx context.Context, c *domain.CollectorCheck) {
	switch c.Type {
	case domain.CollectorCheckTypeHTTP:
		s.runHTTP(ctx, c)
	default:
		// No executor for this type in this build (SNMP/cloud land in
		// E2.2b/c). Still record a run so the due-scan does not re-select it
		// every pass; no telemetry sample to write.
		octx, cancel := outcomeContext(ctx)
		defer cancel()
		if err := s.store.RecordRun(octx, c.CheckID, s.now(), domain.CollectorCheckStatusUnsupported); err != nil {
			s.log.Error("collector scheduler: record run", "check_id", c.CheckID, "err", err)
		}
		s.metrics.IncChecked(domain.CollectorCheckStatusUnsupported)
	}
}

// runHTTP executes one "http" check: a bounded GET against c.Target,
// SSRF-guarded via the injected HTTPDoer (see HTTPDoer's doc comment), and
// writes up/latency_ms/status_code samples tied to c.AssetID.
func (s *Scheduler) runHTTP(ctx context.Context, c *domain.CollectorCheck) {
	if s.http == nil {
		s.finishHTTP(ctx, c, httpCheckResult{Up: false})
		return
	}
	cctx, cancel := context.WithTimeout(ctx, s.cfg.CheckTimeout)
	defer cancel()
	result := runHTTPCheck(cctx, s.http, c.Target)
	s.finishHTTP(ctx, c, result)
}

func (s *Scheduler) finishHTTP(ctx context.Context, c *domain.CollectorCheck, result httpCheckResult) {
	octx, cancel := outcomeContext(ctx)
	defer cancel()

	ts := s.now()
	if samples := s.buildSamples(c, result, ts); len(samples) > 0 {
		if _, err := s.telemetry.WriteSamples(octx, samples); err != nil {
			s.log.Error("collector scheduler: write samples", "check_id", c.CheckID, "err", err)
		}
	}

	status := domain.CollectorCheckStatusDown
	if result.Up {
		status = domain.CollectorCheckStatusOK
	}
	if err := s.store.RecordRun(octx, c.CheckID, ts, status); err != nil {
		s.log.Error("collector scheduler: record run", "check_id", c.CheckID, "err", err)
	}
	s.metrics.IncChecked(status)
}

// buildSamples turns one check result into telemetry samples named
// <MetricPrefix>_up / _latency_ms / _status_code. status_code is present only
// when the target actually answered (see httpCheckResult's doc comment). A
// sample that somehow fails domain.NewSample's validation (metric_prefix and
// target were already validated at check creation/update time, so this
// should not happen in practice) is logged and dropped rather than aborting
// the other samples for this same check.
func (s *Scheduler) buildSamples(c *domain.CollectorCheck, r httpCheckResult, ts time.Time) []domain.Sample {
	var out []domain.Sample
	add := func(suffix string, value float64) {
		sm, err := domain.NewSample(c.TenantID, c.AssetID, c.MetricPrefix+"_"+suffix, value, ts, nil)
		if err != nil {
			s.log.Warn("collector scheduler: sample dropped", "check_id", c.CheckID, "metric_suffix", suffix, "err", err)
			return
		}
		out = append(out, *sm)
	}

	up := 0.0
	if r.Up {
		up = 1
	}
	add("up", up)
	add("latency_ms", r.LatencyMs)
	if r.HasStatusCode {
		add("status_code", float64(r.StatusCode))
	}
	return out
}
