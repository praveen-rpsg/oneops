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
			// Stopped mid-batch: the rest were claimed but never run, and the claim
			// already charged each an attempt. Give those back on a detached context
			// (ADR-CONCURRENCY-006).
			rctx, cancel := outcomeContext(ctx)
			for _, rest := range due[i:] {
				if err := e.execs.ReleaseClaim(rctx, rest.ID, rest.ClaimedAt); err != nil {
					e.log.Warn("policy executor: release unused claim", "execution_id", rest.ID, "err", err)
				}
			}
			cancel()
			return
		}
		e.attempt(ctx, due[i])
	}
}

// outcomeContext returns the context used to record an outcome the action has
// already produced. Detached from the worker's cancellation for the same reason
// as the dispatcher's: a demotion or shutdown mid-run must not lose the record
// of an action that already ran, because an unrecorded attempt is reclaimed and
// re-run (ADR-CONCURRENCY-006). Values are kept, cancellation is dropped, and it
// carries its own deadline.
func outcomeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), outcomeWriteTimeout)
}

// outcomeWriteTimeout bounds how long recording an already-produced outcome may
// hold up a demotion or shutdown.
const outcomeWriteTimeout = 5 * time.Second

// Attempt runs one execution synchronously (used by the admin test endpoint).
func (e *Executor) Attempt(ctx context.Context, ex Execution) ExecutionStatus {
	return e.attempt(ctx, ex)
}

func (e *Executor) attempt(ctx context.Context, ex Execution) ExecutionStatus {
	e.metrics.IncExecution()
	start := e.now()

	// Outcomes are recorded on a context detached from the worker's, so a
	// demotion or shutdown mid-run cannot lose them (ADR-CONCURRENCY-006).
	octx, cancelOutcome := outcomeContext(ctx)
	defer cancelOutcome()

	p, err := e.policies.Get(ctx, ex.PolicyID)
	if err != nil {
		_ = e.execs.MarkResult(octx, ex.ID, ex.ClaimedAt, ExecDeadLetter, ex.RetryCount, "policy not found: "+err.Error(), start, e.now(), time.Time{})
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
		_ = e.execs.MarkResult(octx, ex.ID, ex.ClaimedAt, ExecDeadLetter, ex.RetryCount, "ownership refused: "+err.Error(), start, e.now(), time.Time{})
		e.metrics.IncFailure()
		return ExecDeadLetter
	}

	// A composed run's step runs the Action named at its position in the
	// Sequence, not the policy's single Action field — that field is what a
	// single-action policy (empty Steps, RunID always "") still uses,
	// unchanged (ADR-POLICY-001).
	action := p.Action
	if ex.RunID != "" {
		if ex.StepIndex < 0 || ex.StepIndex >= len(p.Steps) {
			// The policy's Steps changed (or shrank) after this run started —
			// never guess which action to run for a step that no longer
			// exists. Dead-letter rather than retry a lookup that will never
			// succeed.
			_ = e.execs.MarkResult(octx, ex.ID, ex.ClaimedAt, ExecDeadLetter, ex.RetryCount,
				fmt.Sprintf("run %s: step %d out of range for policy's current %d-step sequence", ex.RunID, ex.StepIndex, len(p.Steps)),
				start, e.now(), time.Time{})
			e.metrics.IncFailure()
			return ExecDeadLetter
		}
		action = p.Steps[ex.StepIndex].Action
	}

	runErr := e.runIsolated(ctx, action, ex.Event)
	e.metrics.ObserveDuration(e.now().Sub(start))

	if runErr == nil {
		if err := e.execs.MarkResult(octx, ex.ID, ex.ClaimedAt, ExecSucceeded, ex.RetryCount, "", start, e.now(), time.Time{}); err != nil {
			if errors.Is(err, ErrStaleClaim) {
				// Lease expired and the row was reclaimed mid-run; the reclaiming worker
				// owns the outcome. The action already ran (at-least-once); we do not
				// record it, so we never overwrite the reclaimer's state (ADR-CONCURRENCY-005).
				e.log.Info("policy executor: result fenced — row reclaimed by another worker", "execution_id", ex.ID)
				return ExecSucceeded
			}
			// The action ran but its outcome could not be recorded; the row will be
			// reclaimed and re-run (ADR-CONCURRENCY-006).
			e.log.Error("policy executor: action ran but outcome not recorded", "execution_id", ex.ID, "err", err)
			return ExecSucceeded
		}
		// The outcome is durably recorded and this worker still owns it (no
		// fencing error): only now may the run's sequence advance. A step
		// outside a composed run (RunID empty) has nothing to advance.
		if ex.RunID != "" {
			e.advanceRun(octx, p, ex)
		}
		return ExecSucceeded
	}

	// ex.RetryCount is the attempt number the claim assigned to this attempt
	// (ADR-CONCURRENCY-006); the budget is compared against it directly.
	e.metrics.IncFailure()
	retry := ex.RetryCount
	if retry >= p.MaxRetries {
		_ = e.execs.MarkResult(octx, ex.ID, ex.ClaimedAt, ExecDeadLetter, retry, runErr.Error(), start, e.now(), time.Time{})
		e.log.Warn("policy executor: dead-letter", "execution_id", ex.ID, "policy_id", p.ID, "attempts", retry, "err", runErr)
		return ExecDeadLetter
	}
	e.metrics.IncRetry()
	next := e.now().Add(e.backoff(retry))
	if err := e.execs.MarkResult(octx, ex.ID, ex.ClaimedAt, ExecFailed, retry, runErr.Error(), start, e.now(), next); errors.Is(err, ErrStaleClaim) {
		e.log.Info("policy executor: failed result fenced — row reclaimed by another worker", "execution_id", ex.ID)
	}
	return ExecFailed
}

// runIsolated runs the action under a recover so a panicking action never
// escapes the policy subsystem.
func (e *Executor) runIsolated(ctx context.Context, action ActionSpec, ev Event) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("policy action panicked: %v", r)
		}
	}()
	return e.registry.Run(ctx, action.Type, ev, action.Config)
}

// advanceRun moves a composed Sequence run forward after one of its steps
// just succeeded and had that outcome durably recorded (ADR-POLICY-001 §3).
// It reuses the leader-gated executor's own claim/fence/retry machinery for
// the step it enqueues — this is not a second runtime, only the same
// ClaimDue/MarkResult cycle applied to the next Execution row.
//
// It is a thin wrapper over advanceRunSteps: fetch the run's current
// Executions, then hand off to the one path that decides and performs the
// move. That path is also the Reconciler's (reconciler.go) — there is
// exactly one place a composed run's next step is ever created, reached
// reactively from here and periodically from the sweep.
func (e *Executor) advanceRun(ctx context.Context, p Policy, ex Execution) {
	steps, err := e.execs.ListByRun(ctx, ex.RunID)
	if err != nil {
		e.log.Error("policy executor: advance run: list executions", "run_id", ex.RunID, "err", err)
		return
	}
	advanceRunSteps(ctx, e.execs, e.log, e.now, p, ex.RunID, steps)
}

// advanceRunSteps is the single path that decides and performs a composed
// run's next move, computed fresh from the run's own Executions (steps) and
// its Policy's current Sequence — never cached, never assumed. It is called
// both reactively, immediately after a step's success is recorded
// (advanceRun above), and periodically by Reconciler.RunOnce recovering a
// run whose next step was never enqueued because the worker that recorded
// the predecessor's success crashed before enqueuing it. Both callers
// converge on the one Enqueue call below, so the permanent-stall gap this
// closes cannot reopen a second bookkeeping path for it.
//
// The cursor (RunProgress.NextStepIndex) is recomputed from steps every call
// rather than assumed to be any particular step's index+1, so a duplicated
// or racing call — the same step's success advanced twice, or the sweep and
// a live worker both reaching the same run — recomputes the same answer
// instead of drifting, and the deterministic id it enqueues under
// (ExecutionID(runID, stepTag, stepIndex)) makes a repeated Enqueue a no-op
// (`ON CONFLICT (id) DO NOTHING`, the same idempotency shape ExecutionID
// already gives a single-action policy).
func advanceRunSteps(ctx context.Context, execs ExecutionStore, log *slog.Logger, now func() time.Time, p Policy, runID string, steps []Execution) {
	next := (RunProgress{RunID: runID, Seq: p.Steps, Steps: steps}).NextStepIndex()
	if next >= len(p.Steps) {
		// No next step: the run is complete (or has no steps to check). There
		// is no run-complete entity to mark — RunProgress.Complete() over
		// this run's Executions already reports it.
		return
	}
	if cur, ok := executionAtStep(steps, next); ok {
		if cur.Status != ExecSucceeded {
			// The cursor's own row already exists and has not succeeded yet
			// (pending, running, or failed-awaiting-retry): the claim/fence/
			// retry machinery already owns it. Touching it here — enqueuing
			// a duplicate or nudging its state — is exactly what would
			// reintroduce a second work-queue path for the same step.
			return
		}
		// NextStepIndex CAUTION (composition.go): the step that just
		// succeeded names a Gate that has not resolved to GatePassed. Pause
		// here — set the gate pending and enqueue nothing. A future gate
		// evaluator (W3) is what advances the run past this point.
		//
		// Self-defending (W2 review nit): NextStepIndex only returns a
		// succeeded step's own index when that step names a Gate. Refuse to
		// mark it pending if the Sequence disagrees — an ungated step must
		// never be wrongly paused because Steps returned a stale set.
		if p.Steps[cur.StepIndex].Gate == nil {
			log.Error("policy executor: advance run: succeeded ungated step reported as blocking — refusing to set a gate",
				"execution_id", cur.ID, "run_id", runID, "step_index", cur.StepIndex)
			return
		}
		if err := execs.SetGate(ctx, cur.ID, GatePending); err != nil {
			log.Error("policy executor: advance run: set gate pending", "execution_id", cur.ID, "run_id", runID, "err", err)
		}
		return
	}
	// No row exists yet for the cursor: the genuinely missing next step —
	// either because this is the reactive path enqueuing it for the first
	// time, or because the Reconciler found a run wedged exactly here. Its
	// predecessor (next-1) is what NextStepIndex just proved succeeded with
	// its gate resolved, so its Event snapshot is the one to propagate:
	// every step of one run carries the same triggering Event.
	pred, ok := executionAtStep(steps, next-1)
	if !ok {
		// Defensive only: NextStepIndex cannot return a positive next
		// without a resolved predecessor at next-1 present in steps, and the
		// Reconciler's candidate query never selects a run with no
		// Executions at all.
		log.Error("policy executor: advance run: no predecessor execution for cursor", "run_id", runID, "step_index", next)
		return
	}
	nextEx := Execution{
		ID: ExecutionID(runID, stepTag, int64(next)), PolicyID: p.ID, Event: pred.Event, Status: ExecPending,
		NextAttemptAt: now(), CreatedAt: now(),
		RunID: runID, StepIndex: next,
	}
	if err := execs.Enqueue(ctx, []Execution{nextEx}); err != nil {
		log.Error("policy executor: advance run: enqueue next step", "run_id", runID, "next_step", next, "err", err)
	}
}

// backoff computes the delay before the next attempt. retry is normally >= 1
// (ClaimDue always increments it before an attempt is made), but Attempt is
// also called directly, unclaimed, by the admin "test this policy" endpoint
// with a fresh record whose RetryCount is 0 — shift must not go negative for
// that input, or this panics. Clamping the shift at 0 makes retry<=1 both
// yield BaseBackoff, and leaves the queue-driven path (retry>=1) unchanged.
func (e *Executor) backoff(retry int) time.Duration {
	shift := retry - 1
	if shift < 0 {
		shift = 0
	}
	b := e.cfg.BaseBackoff << shift
	if b <= 0 || b > e.cfg.MaxBackoff {
		return e.cfg.MaxBackoff
	}
	return b
}
