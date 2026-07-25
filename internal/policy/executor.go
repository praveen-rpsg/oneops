package policy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// ExecutorConfig parameterises the executor.
type ExecutorConfig struct {
	Interval    time.Duration // between passes (default 2s)
	BatchLimit  int           // executions claimed per pass (default 100)
	BaseBackoff time.Duration // first retry delay, doubled per retry (default 5s)
	MaxBackoff  time.Duration // backoff cap (default 1h)
	ClaimLease  time.Duration // how long a running execution is owned before reclaim (default 2m)
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
	if c.ClaimLease <= 0 {
		c.ClaimLease = 2 * time.Minute
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
	owners   domain.EventOwnerResolver
	registry *Registry
	metrics  Metrics
	log      *slog.Logger
	now      func() time.Time
	cfg      ExecutorConfig
}

// NewExecutor builds the executor. execs, policies, owners and registry are
// required. owners is the authoritative source of event ownership; an executor
// built without it refuses every execution rather than trusting the queued
// event (ADR-TENANCY-003).
func NewExecutor(execs ExecutionStore, policies Store, owners domain.EventOwnerResolver, registry *Registry, metrics Metrics, log *slog.Logger, cfg ExecutorConfig) *Executor {
	if metrics == nil {
		metrics = NopMetrics{}
	}
	if log == nil {
		log = slog.Default()
	}
	cfg.withDefaults()
	return &Executor{
		execs: execs, policies: policies, owners: owners, registry: registry, metrics: metrics, log: log,
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
	due, err := e.execs.ClaimDue(ctx, e.now(), e.cfg.ClaimLease, e.cfg.BatchLimit)
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
		_ = e.execs.MarkResult(ctx, ex.ID, ex.ClaimedAt, ExecDeadLetter, ex.RetryCount, "policy not found: "+err.Error(), start, e.now(), time.Time{})
		return ExecDeadLetter
	}

	// The policy is authoritative — fetched by id from the policy table, not from
	// the queue. The event is not: ex.Event is a snapshot stored in the queued
	// execution row, and a policy action POSTs that event's contents outbound.
	// A synthetic row pairing this policy with another tenant's event was
	// executed against the running service, exfiltrating a victim's governance
	// event to the attacker's endpoint. Labelling the row self-consistently did
	// not help detection, because the row's label is not evidence.
	//
	// So the event's owner is re-derived from the audit log and compared against
	// the policy through the shared framework, before the action runs. The
	// queued event's own fields are used only as coordinates into the
	// authoritative record, never as the ownership claim itself.
	if err := domain.ResolveAndAuthorize(ctx, e.owners, p, ex.Event.CfgID, ex.Event.Seq); err != nil {
		// Fail closed and dead-letter, never retry: a cross-tenant or phantom
		// execution is never made transiently valid by trying again.
		e.log.Error("policy executor: refused execution",
			"execution_id", ex.ID, "policy_id", p.ID, "policy_tenant", p.OwnerTenantID(),
			"event_chain", ex.Event.CfgID, "event_seq", ex.Event.Seq, "err", err.Error())
		_ = e.execs.MarkResult(ctx, ex.ID, ex.ClaimedAt, ExecDeadLetter, ex.RetryCount, "ownership refused: "+err.Error(), start, e.now(), time.Time{})
		e.metrics.IncFailure()
		return ExecDeadLetter
	}

	runErr := e.runIsolated(ctx, p, ex.Event)
	e.metrics.ObserveDuration(e.now().Sub(start))

	if runErr == nil {
		if err := e.execs.MarkResult(ctx, ex.ID, ex.ClaimedAt, ExecSucceeded, ex.RetryCount, "", start, e.now(), time.Time{}); errors.Is(err, ErrStaleClaim) {
			// Lease expired and the row was reclaimed mid-run; the reclaiming worker
			// owns the outcome. The action already ran (at-least-once); we do not
			// record it, so we never overwrite the reclaimer's state (ADR-CONCURRENCY-005).
			e.log.Info("policy executor: result fenced — row reclaimed by another worker", "execution_id", ex.ID)
		}
		return ExecSucceeded
	}

	e.metrics.IncFailure()
	retry := ex.RetryCount + 1
	if retry >= p.MaxRetries {
		_ = e.execs.MarkResult(ctx, ex.ID, ex.ClaimedAt, ExecDeadLetter, retry, runErr.Error(), start, e.now(), time.Time{})
		e.log.Warn("policy executor: dead-letter", "execution_id", ex.ID, "policy_id", p.ID, "err", runErr)
		return ExecDeadLetter
	}
	e.metrics.IncRetry()
	next := e.now().Add(e.backoff(retry))
	if err := e.execs.MarkResult(ctx, ex.ID, ex.ClaimedAt, ExecFailed, retry, runErr.Error(), start, e.now(), next); errors.Is(err, ErrStaleClaim) {
		e.log.Info("policy executor: failed result fenced — row reclaimed by another worker", "execution_id", ex.ID)
	}
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
