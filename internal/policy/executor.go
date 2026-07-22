package policy

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// ExecutorConfig parameterises the executor.
type ExecutorConfig struct {
	Interval    time.Duration // between passes (default 2s)
	BatchLimit  int           // executions claimed per pass (default 100)
	BaseBackoff time.Duration // first retry delay, doubled per retry (default 5s)
	MaxBackoff  time.Duration // backoff cap (default 1h)
}

func (c *ExecutorConfig) withDefaults() {
	if c.Interval <= 0 {
		c.Interval = 2 * time.Second
	}
	if c.BatchLimit <= 0 {
		c.BatchLimit = 100
	}
	if c.BaseBackoff <= 0 {
		c.BaseBackoff = 5 * time.Second
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = time.Hour
	}
}

// Executor runs due policy executions asynchronously. Each execution is isolated:
// its action runs under a recover, and any error (or panic) is captured on the
// execution record only — it never propagates to Governance, Audit, Replay, or
// Event Delivery. Failures retry with exponential backoff up to the policy's
// MaxRetries, then dead-letter.
type Executor struct {
	execs    ExecutionStore
	policies Store
	registry *Registry
	metrics  Metrics
	log      *slog.Logger
	now      func() time.Time
	cfg      ExecutorConfig
}

// NewExecutor builds the executor. execs, policies, and registry are required.
func NewExecutor(execs ExecutionStore, policies Store, registry *Registry, metrics Metrics, log *slog.Logger, cfg ExecutorConfig) *Executor {
	if metrics == nil {
		metrics = NopMetrics{}
	}
	if log == nil {
		log = slog.Default()
	}
	cfg.withDefaults()
	return &Executor{
		execs: execs, policies: policies, registry: registry, metrics: metrics, log: log,
		now: func() time.Time { return time.Now().UTC() }, cfg: cfg,
	}
}

// Run executes due executions on Interval until ctx is cancelled.
func (e *Executor) Run(ctx context.Context) error {
	e.log.Info("policy executor started", "interval", e.cfg.Interval.String())
	e.RunOnce(ctx)
	t := time.NewTicker(e.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			e.log.Info("policy executor stopped", "reason", ctx.Err())
			return ctx.Err()
		case <-t.C:
			e.RunOnce(ctx)
		}
	}
}

// RunOnce claims and runs all due executions once.
func (e *Executor) RunOnce(ctx context.Context) {
	due, err := e.execs.ClaimDue(ctx, e.now(), e.cfg.BatchLimit)
	if err != nil {
		e.log.Error("policy executor: claim due", "err", err)
		return
	}
	for i := range due {
		if ctx.Err() != nil {
			return
		}
		e.attempt(ctx, due[i])
	}
}

// Attempt runs one execution synchronously (used by the admin test endpoint).
func (e *Executor) Attempt(ctx context.Context, ex Execution) ExecutionStatus {
	return e.attempt(ctx, ex)
}

func (e *Executor) attempt(ctx context.Context, ex Execution) ExecutionStatus {
	e.metrics.IncExecution()
	start := e.now()

	p, err := e.policies.Get(ctx, ex.PolicyID)
	if err != nil {
		_ = e.execs.MarkResult(ctx, ex.ID, ExecDeadLetter, ex.RetryCount, "policy not found: "+err.Error(), start, e.now(), time.Time{})
		return ExecDeadLetter
	}

	runErr := e.runIsolated(ctx, p, ex.Event)
	e.metrics.ObserveDuration(e.now().Sub(start))

	if runErr == nil {
		_ = e.execs.MarkResult(ctx, ex.ID, ExecSucceeded, ex.RetryCount, "", start, e.now(), time.Time{})
		return ExecSucceeded
	}

	e.metrics.IncFailure()
	retry := ex.RetryCount + 1
	if retry >= p.MaxRetries {
		_ = e.execs.MarkResult(ctx, ex.ID, ExecDeadLetter, retry, runErr.Error(), start, e.now(), time.Time{})
		e.log.Warn("policy executor: dead-letter", "execution_id", ex.ID, "policy_id", p.ID, "err", runErr)
		return ExecDeadLetter
	}
	e.metrics.IncRetry()
	next := e.now().Add(e.backoff(retry))
	_ = e.execs.MarkResult(ctx, ex.ID, ExecFailed, retry, runErr.Error(), start, e.now(), next)
	return ExecFailed
}

// runIsolated runs the action under a recover so a panicking action never
// escapes the policy subsystem.
func (e *Executor) runIsolated(ctx context.Context, p Policy, ev Event) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("policy action panicked: %v", r)
		}
	}()
	return e.registry.Run(ctx, p.Action.Type, ev, p.Action.Config)
}

func (e *Executor) backoff(retry int) time.Duration {
	b := e.cfg.BaseBackoff << (retry - 1)
	if b <= 0 || b > e.cfg.MaxBackoff {
		return e.cfg.MaxBackoff
	}
	return b
}
