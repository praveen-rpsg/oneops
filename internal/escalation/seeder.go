package escalation

import (
	"context"
	"log/slog"
	"time"
)

// SeederConfig parameterises the Seeder.
type SeederConfig struct {
	// Interval between seeding passes (default 15s — this is what stands
	// between an alert-sourced incident opening and its first page, so it
	// runs noticeably tighter than grouping's own 60s noise-reduction sweep;
	// this is a "did anyone start paging this yet" check, not the initial
	// alert itself).
	Interval time.Duration
}

func (c *SeederConfig) withDefaults() {
	if c.Interval <= 0 {
		c.Interval = 15 * time.Second
	}
}

// Seeder is the leader-gated reconciliation pass that enrols newly-open,
// unacknowledged, alert-sourced incidents into escalation_state (E5.2b-2).
// DECOUPLED from internal/alerting's evaluator: it never reads or writes
// anything the hot path touches, the same "separate periodic sweep, not a
// hook inside the hot path" choice internal/grouping's own package doc
// comment makes for E4.2 grouping.
type Seeder struct {
	store   Store
	metrics Metrics
	log     *slog.Logger
	now     func() time.Time
	cfg     SeederConfig
}

// NewSeeder builds a Seeder over store.
func NewSeeder(store Store, metrics Metrics, log *slog.Logger, cfg SeederConfig) *Seeder {
	if metrics == nil {
		metrics = NopMetrics{}
	}
	if log == nil {
		log = slog.Default()
	}
	cfg.withDefaults()
	return &Seeder{store: store, metrics: metrics, log: log,
		now: func() time.Time { return time.Now().UTC() }, cfg: cfg}
}

// Run seeds on Interval until ctx is cancelled, then returns ctx.Err() — the
// same shape notification.Worker.Run/grouping.Reconciler.Run use.
func (s *Seeder) Run(ctx context.Context) error {
	s.log.Info("escalation seeder started", "interval", s.cfg.Interval.String())
	s.RunOnce(ctx)
	t := time.NewTicker(s.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			s.log.Info("escalation seeder stopped", "reason", ctx.Err())
			return ctx.Err()
		case <-t.C:
			s.RunOnce(ctx)
		}
	}
}

// RunOnce seeds every eligible incident once.
func (s *Seeder) RunOnce(ctx context.Context) {
	seeded, skippedNoPolicy, err := s.store.Seed(ctx, s.now())
	if err != nil {
		s.log.Error("escalation seeder: seed", "err", err)
		s.metrics.IncErrors()
		return
	}
	s.metrics.IncSeeded(seeded)
	if skippedNoPolicy > 0 {
		s.metrics.IncSkippedNoPolicy(skippedNoPolicy)
		s.log.Debug("escalation seeder: incidents left unseeded — tenant has no active escalation policy",
			"count", skippedNoPolicy)
	}
	if seeded > 0 {
		s.log.Info("escalation seeder: seeded incidents", "count", seeded)
	}
}
