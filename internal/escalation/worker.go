package escalation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// maxTiersPerPolicy bounds a single ListTiers fetch. An escalation ladder is,
// by construction, a short human-authored list (E5.2b-1's own review: tiers
// are added one at a time through an admin API); a policy with more tiers
// than this is outside what this engine considers — an honest, documented
// bound, not a silent truncation nobody would notice.
const maxTiersPerPolicy = 500

// WorkerConfig parameterises the Worker. Mirrors notification.WorkerConfig.
type WorkerConfig struct {
	// Interval between claim passes (default 5s — an escalation is a paging
	// decision an operator is waiting on, tighter than the Seeder's own 15s).
	Interval time.Duration
	// BatchLimit bounds escalation_state rows claimed per pass (default 100).
	BatchLimit int
	// ClaimLease is how long a claim is owned before it is considered
	// abandoned and reclaimable (default 2m).
	ClaimLease time.Duration
}

func (c *WorkerConfig) withDefaults() {
	if c.Interval <= 0 {
		c.Interval = 5 * time.Second
	}
	if c.BatchLimit <= 0 {
		c.BatchLimit = 100
	}
	if c.ClaimLease <= 0 {
		c.ClaimLease = 2 * time.Minute
	}
}

// Worker is the leader-gated escalation-processing pass (E5.2b-2,
// ADR-ONCALL-003): claims due escalation_state rows and pages/escalates/
// terminates them, mirroring notification.Worker's claim-act-fence shape
// exactly.
type Worker struct {
	store      Store
	incidents  IncidentReader
	policies   PolicyReader
	oncall     OnCallResolver
	membership MembershipChecker
	users      UserReader
	notifier   Notifier
	metrics    Metrics
	log        *slog.Logger
	now        func() time.Time
	cfg        WorkerConfig
}

// NewWorker builds a Worker. Every dependency is required except metrics/log,
// which default to NopMetrics/slog.Default — the same convention
// notification.NewWorker/grouping.NewReconciler use.
func NewWorker(
	store Store, incidents IncidentReader, policies PolicyReader, oncall OnCallResolver,
	membership MembershipChecker, users UserReader, notifier Notifier,
	metrics Metrics, log *slog.Logger, cfg WorkerConfig,
) *Worker {
	if metrics == nil {
		metrics = NopMetrics{}
	}
	if log == nil {
		log = slog.Default()
	}
	cfg.withDefaults()
	return &Worker{
		store: store, incidents: incidents, policies: policies, oncall: oncall,
		membership: membership, users: users, notifier: notifier, metrics: metrics, log: log,
		now: func() time.Time { return time.Now().UTC() }, cfg: cfg,
	}
}

// Run processes due escalation_state rows on Interval until ctx is
// cancelled, then returns ctx.Err() — the same shape notification.Worker.Run
// uses.
func (w *Worker) Run(ctx context.Context) error {
	w.log.Info("escalation worker started", "interval", w.cfg.Interval.String())
	w.RunOnce(ctx)
	t := time.NewTicker(w.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			w.log.Info("escalation worker stopped", "reason", ctx.Err())
			return ctx.Err()
		case <-t.C:
			w.RunOnce(ctx)
		}
	}
}

// RunOnce claims and processes all due escalation_state rows once.
func (w *Worker) RunOnce(ctx context.Context) {
	due, err := w.store.ClaimDue(ctx, w.now(), w.cfg.ClaimLease, w.cfg.BatchLimit)
	if err != nil {
		w.log.Error("escalation worker: claim due", "err", err)
		w.metrics.IncErrors()
		return
	}
	for i := range due {
		if ctx.Err() != nil {
			// Stopped mid-batch (demotion or shutdown). The rest of the batch
			// was claimed but never processed, and the claim already charged
			// each row an attempt — give those back, or a few restarts would
			// exhaust nothing (there is no retry budget here, but a stuck
			// claimed_at would block reclaim for a full lease for no reason).
			// Mirrors notification.Worker.RunOnce exactly.
			rctx, cancel := outcomeContext(ctx)
			for _, rest := range due[i:] {
				if err := w.store.ReleaseClaim(rctx, rest.StateID, rest.ClaimedAt); err != nil {
					w.log.Warn("escalation worker: release unused claim", "state_id", rest.StateID, "err", err)
				}
			}
			cancel()
			return
		}
		w.process(ctx, due[i])
	}
}

// outcomeContext detaches an outcome write from the Worker's own context, the
// same reasoning notification.outcomeContext/events.outcomeContext give: a
// demotion or shutdown mid-attempt must not lose an outcome that has already
// happened in the outside world (a page already sent, an ack already
// observed).
func outcomeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), outcomeWriteTimeout)
}

const outcomeWriteTimeout = 5 * time.Second

// process handles one claimed escalation_state row: re-check the incident,
// stop if it is acknowledged or otherwise no longer open, else page the
// current tier's on-call user (subject to the page-time active-membership
// re-check) and advance.
//
// Every tenant-scoped read/write below is bound to st.TenantID via
// domain.WithTenant, re-deriving the tenant rather than trusting an ambient
// one (ADR-TENANCY-003) — st.TenantID itself came from nowhere but the
// escalated incident's own row, written once by Store.Seed, so this is not a
// second, weaker source of truth, it is the same one carried forward.
func (w *Worker) process(ctx context.Context, st *domain.EscalationState) {
	scopedCtx := domain.WithTenant(ctx, &domain.Tenant{TenantID: st.TenantID})

	inc, err := w.incidents.Get(scopedCtx, st.IncidentID)
	if err != nil {
		w.log.Error("escalation worker: load incident", "state_id", st.StateID, "incident_id", st.IncidentID, "err", err)
		w.metrics.IncErrors()
		w.release(ctx, st)
		return
	}

	// Ack/resolve cancels escalation, checked here every cycle — the
	// decoupled mechanism by which acknowledging or resolving an incident
	// stops its paging (ADR-ONCALL-003 §3). "resolved" is the catch-all
	// label for every non-open, non-acknowledged status (investigating,
	// resolved, closed, reopened): this engine does not distinguish among
	// them, and in particular does NOT re-arm escalation for a reopened
	// incident that already has a terminal row — an honest, documented bound
	// (docs/PLATFORM-BUILD-PLAN.md E5.2b-2; re-escalating a reopened incident
	// is deferred).
	if inc.Status != domain.IncidentOpen {
		terminal := domain.EscalationStateResolved
		if inc.Status == domain.IncidentAcknowledged {
			terminal = domain.EscalationStateAcked
		}
		if err := w.markTerminal(ctx, st, terminal); err != nil {
			return
		}
		if terminal == domain.EscalationStateAcked {
			w.metrics.IncAcked()
		} else {
			w.metrics.IncResolved()
		}
		return
	}

	tiers, err := w.policies.ListTiers(scopedCtx, st.PolicyID, maxTiersPerPolicy, "")
	if err != nil {
		w.log.Error("escalation worker: load policy tiers", "state_id", st.StateID, "policy_id", st.PolicyID, "err", err)
		w.metrics.IncErrors()
		w.release(ctx, st)
		return
	}

	if st.CurrentTierIndex >= len(tiers) {
		if err := w.markTerminal(ctx, st, domain.EscalationStateExhausted); err != nil {
			return
		}
		w.metrics.IncExhausted()
		return
	}

	tier := tiers[st.CurrentTierIndex]
	if w.page(scopedCtx, st, inc, tier) {
		w.metrics.IncPaged()
	} else {
		w.metrics.IncSkippedRevoked()
	}

	nextAttempt := w.now().Add(time.Duration(tier.WaitSeconds) * time.Second)
	octx, cancel := outcomeContext(ctx)
	defer cancel()
	if err := w.store.Advance(octx, st.StateID, st.ClaimedAt, st.CurrentTierIndex+1, nextAttempt); err != nil {
		if errors.Is(err, ErrStaleClaim) {
			w.log.Info("escalation worker: advance fenced — row reclaimed by another worker", "state_id", st.StateID)
			return
		}
		w.log.Error("escalation worker: advance", "state_id", st.StateID, "err", err)
		w.metrics.IncErrors()
		return
	}
	w.metrics.IncEscalated()
}

// page attempts to page tier's on-call user, applying the page-time
// active-membership re-check (E5.2a review nit 1, ADR-ONCALL-003 §4): a
// revoked or absent on-call user is never paged, and this is recorded
// (returns false) rather than silently dropped, so the caller advances the
// ladder instead of stalling on a tier nobody can ever satisfy.
func (w *Worker) page(ctx context.Context, st *domain.EscalationState, inc *domain.Incident, tier *domain.EscalationTier) bool {
	res, err := w.oncall.OnCall(ctx, tier.OnCallScheduleID, w.now())
	if err != nil {
		w.log.Error("escalation worker: resolve on-call", "state_id", st.StateID,
			"schedule_id", tier.OnCallScheduleID, "err", err)
		w.metrics.IncErrors()
		return false
	}
	if res.UserID == nil {
		w.log.Warn("escalation worker: no on-call user for tier — advancing without paging",
			"state_id", st.StateID, "schedule_id", tier.OnCallScheduleID, "tier", st.CurrentTierIndex)
		return false
	}

	active, err := w.membership.ActiveMember(ctx, *res.UserID)
	if err != nil {
		w.log.Error("escalation worker: check active membership", "state_id", st.StateID,
			"user_id", *res.UserID, "err", err)
		w.metrics.IncErrors()
		return false
	}
	if !active {
		w.log.Warn("escalation worker: on-call user is not an active tenant member — advancing without paging",
			"state_id", st.StateID, "user_id", *res.UserID, "tier", st.CurrentTierIndex)
		return false
	}

	user, err := w.users.Get(ctx, *res.UserID)
	if err != nil {
		w.log.Error("escalation worker: resolve on-call user email", "state_id", st.StateID,
			"user_id", *res.UserID, "err", err)
		w.metrics.IncErrors()
		return false
	}

	n, err := domain.NewNotification(st.TenantID, domain.NotificationEmail, user.Email,
		fmt.Sprintf("[Escalation] Incident %s — tier %d page", inc.IncidentID, st.CurrentTierIndex+1),
		fmt.Sprintf("Incident %q (%s, severity %s) is still unacknowledged. "+
			"You are on call for tier %d of escalation policy %s.",
			inc.Title, inc.IncidentID, inc.Severity, st.CurrentTierIndex+1, st.PolicyID))
	if err != nil {
		w.log.Error("escalation worker: build page notification", "state_id", st.StateID, "err", err)
		w.metrics.IncErrors()
		return false
	}
	if _, err := w.notifier.Enqueue(ctx, n); err != nil {
		w.log.Error("escalation worker: enqueue page", "state_id", st.StateID, "err", err)
		w.metrics.IncErrors()
		return false
	}
	return true
}

// markTerminal records a terminal outcome, fenced on the claim token, logging
// and counting a stale fence exactly like the notification Worker's own
// finishSent/markFailed do.
func (w *Worker) markTerminal(ctx context.Context, st *domain.EscalationState, status domain.EscalationStateStatus) error {
	octx, cancel := outcomeContext(ctx)
	defer cancel()
	if err := w.store.MarkTerminal(octx, st.StateID, st.ClaimedAt, status); err != nil {
		if errors.Is(err, ErrStaleClaim) {
			w.log.Info("escalation worker: terminal outcome fenced — row reclaimed by another worker",
				"state_id", st.StateID, "status", status)
			return err
		}
		w.log.Error("escalation worker: mark terminal", "state_id", st.StateID, "status", status, "err", err)
		w.metrics.IncErrors()
		return err
	}
	return nil
}

// release returns an unused claim after a read failure that leaves the row
// unprocessed this cycle, so it does not sit claimed for a full lease for no
// reason. Best-effort: a failure here just means the lease alone reclaims it
// later.
func (w *Worker) release(ctx context.Context, st *domain.EscalationState) {
	octx, cancel := outcomeContext(ctx)
	defer cancel()
	if err := w.store.ReleaseClaim(octx, st.StateID, st.ClaimedAt); err != nil {
		w.log.Warn("escalation worker: release claim after read failure", "state_id", st.StateID, "err", err)
	}
}
