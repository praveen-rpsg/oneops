package alerting

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// evaluationCoverageSlack tolerates a small gap between a rule's window start
// (now - ForDuration) and the oldest sample actually returned, so a producer
// reporting every few seconds is not falsely denied a fire because its
// earliest sample landed a moment after the exact window boundary. It is
// deliberately small relative to MinAlertRuleForDurationSeconds (1s) through
// MaxAlertRuleForDurationSeconds (24h): widening it would let a breach that
// only covers a fraction of ForDuration pass as "sustained".
const evaluationCoverageSlack = 30 * time.Second

// alertingSystemActor identifies this package as the actor on every
// Incident timeline row it writes (E4.1's correlation create/link/recovery
// notes) — so an operator reading an incident's timeline can tell a
// system-derived entry from their own action.
const alertingSystemActor = "system:alerting"

// Config parameterises the Evaluator.
type Config struct {
	// Interval between evaluation passes (default 30s — short enough that a
	// sustained breach is noticed promptly, long enough not to hammer
	// telemetry_sample every second across every tenant's rules).
	Interval time.Duration
	// PageSize bounds one EnabledRules page (default 200).
	PageSize int
	// MaxRulesPerTick caps how many rules one RunOnce evaluates in total,
	// across every page — the same "no unbounded background pass" rule
	// TelemetryRollupWorker's trailing window and CollectorCheckStore.
	// DueChecks's limit already apply (default 5000).
	MaxRulesPerTick int
	// Concurrency bounds how many rules are evaluated at once (default 8):
	// each evaluation is one telemetry query plus, on a transition, one
	// notification enqueue, and unbounded fan-out across thousands of rules
	// would open thousands of connections from one process at once.
	Concurrency int
}

func (c *Config) withDefaults() {
	if c.Interval <= 0 {
		c.Interval = 30 * time.Second
	}
	if c.PageSize <= 0 {
		c.PageSize = 200
	}
	if c.MaxRulesPerTick <= 0 {
		c.MaxRulesPerTick = 5000
	}
	if c.Concurrency <= 0 {
		c.Concurrency = 8
	}
}

// Evaluator is the leader-gated alert-rule evaluator — see the package doc
// comment.
type Evaluator struct {
	store      Store
	telemetry  TelemetryReader
	notifier   Notifier
	correlator IncidentCorrelator
	metrics    Metrics
	log        *slog.Logger
	now        func() time.Time
	cfg        Config
}

// NewEvaluator builds the evaluator over the privileged pool's Store and
// TelemetryReader. correlator may be nil: E4.1's alert->incident wiring is
// then skipped entirely (a firing still notifies; it never creates or links
// an Incident) — the same "optional dependency, narrower behaviour" shape a
// nil Metrics does not get (Metrics always defaults to NopMetrics) but a
// deployment that has not yet wired incident management needs, since unlike
// metrics there is no safe no-op IncidentCorrelator to default to (any
// stand-in either lies about creating an incident or silently drops the
// correlation, and this package does not decide which is less wrong).
func NewEvaluator(
	store Store, telemetry TelemetryReader, notifier Notifier, correlator IncidentCorrelator,
	metrics Metrics, log *slog.Logger, cfg Config,
) *Evaluator {
	if log == nil {
		log = slog.Default()
	}
	if metrics == nil {
		metrics = NopMetrics{}
	}
	cfg.withDefaults()
	return &Evaluator{
		store: store, telemetry: telemetry, notifier: notifier, correlator: correlator, metrics: metrics, log: log,
		now: func() time.Time { return time.Now().UTC() }, cfg: cfg,
	}
}

// Run evaluates on Interval until ctx is cancelled.
func (e *Evaluator) Run(ctx context.Context) error {
	e.log.Info("alert rule evaluator started", "interval", e.cfg.Interval.String())
	e.RunOnce(ctx)
	t := time.NewTicker(e.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			e.log.Info("alert rule evaluator stopped", "reason", ctx.Err())
			return ctx.Err()
		case <-t.C:
			e.RunOnce(ctx)
		}
	}
}

// RunOnce pages through every enabled rule, up to MaxRulesPerTick, and
// evaluates each with bounded concurrency.
func (e *Evaluator) RunOnce(ctx context.Context) {
	now := e.now()
	after := ""
	processed := 0

	sem := make(chan struct{}, e.cfg.Concurrency)
	for {
		if ctx.Err() != nil {
			return
		}
		rules, err := e.store.EnabledRules(ctx, e.cfg.PageSize, after)
		if err != nil {
			e.log.Error("alert rule evaluator: list enabled rules", "err", err)
			e.metrics.IncErrors()
			return
		}
		if len(rules) == 0 {
			return
		}

		var wg sync.WaitGroup
		for _, rule := range rules {
			rule := rule
			after = rule.RuleID
			processed++
			if processed > e.cfg.MaxRulesPerTick {
				wg.Wait()
				return
			}
			sem <- struct{}{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				e.evaluateRule(ctx, rule, now)
			}()
		}
		wg.Wait()

		if len(rules) < e.cfg.PageSize {
			return
		}
	}
}

// evaluateRule queries the rule's own tenant's telemetry — see
// TelemetryReader's doc comment for why tenantID is a required, explicit
// argument rather than assumed from the connection — decides whether the
// breach has been sustained across ForDuration, and on a state TRANSITION
// only, enqueues one Notification and persists the new state.
//
// Order matters: the notification is enqueued BEFORE the transition is
// persisted. If the process crashes between the two, the next tick still
// sees the old LastState, re-detects the same real-world transition, and
// enqueues a second notification — a duplicate. Persisting first and
// crashing before the notification would instead silently lose the alert
// forever. Between "duplicate" and "missed", this package always chooses
// duplicate (the same priority ADR-CONCURRENCY-*'s at-least-once delivery
// choices make elsewhere in this platform; recorded as its own decision in
// ADR-TENANCY-012 §2).
//
// correlate (E4.1) runs after notify and, like it, BEFORE the transition is
// persisted, for the identical reason: if the process crashes between
// correlate and RecordTransition, the next tick re-detects the same
// transition and correlate runs again. That second run is harmless beyond
// one duplicate timeline note — FindOrCreateOpenAlertIncident's uniqueness
// constraint (postgres.IncidentStore's doc comment) means it links rather
// than duplicates an incident it already created — the same "duplicate over
// silent loss" trade as notify.
func (e *Evaluator) evaluateRule(ctx context.Context, rule *domain.AlertRule, now time.Time) {
	e.metrics.IncEvaluated()

	from := now.Add(-time.Duration(rule.ForDuration) * time.Second)
	samples, err := e.telemetry.QueryRangeForTenant(ctx, rule.TenantID, rule.AssetID, rule.Metric, from, now)
	if err != nil {
		e.log.Error("alert rule evaluator: query telemetry", "rule_id", rule.RuleID, "err", err)
		e.metrics.IncErrors()
		return
	}

	next := domain.AlertRuleStateOK
	if sustainedBreach(samples, rule.Comparator, rule.Threshold, from) {
		next = domain.AlertRuleStateFiring
	}
	if next == rule.LastState {
		return // no transition: no write, no notification (transition-only)
	}

	if err := e.notify(ctx, rule, next); err != nil {
		e.log.Error("alert rule evaluator: enqueue notification", "rule_id", rule.RuleID, "err", err)
		e.metrics.IncErrors()
		return // do not persist the transition; retry both next tick
	}

	nextIncidentID := rule.CurrentIncidentID
	if e.correlator != nil {
		id, err := e.correlate(ctx, rule, next)
		if err != nil {
			e.log.Error("alert rule evaluator: correlate incident", "rule_id", rule.RuleID, "err", err)
			e.metrics.IncErrors()
			return // do not persist the transition; retry both next tick (see doc comment above)
		}
		nextIncidentID = id
	}

	if _, err := e.store.RecordTransition(ctx, rule.RuleID, rule.RowVersion, next, now, nextIncidentID); err != nil {
		if errors.Is(err, domain.ErrVersionMismatch) || errors.Is(err, domain.ErrNotFound) {
			// The rule was edited, disabled or deleted between this tick's read
			// and this write. Its own next read will reflect that; nothing to
			// retry with the version this tick held.
			e.log.Info("alert rule evaluator: transition not recorded (rule changed concurrently)",
				"rule_id", rule.RuleID, "err", err)
			return
		}
		e.log.Error("alert rule evaluator: record transition", "rule_id", rule.RuleID, "err", err)
		e.metrics.IncErrors()
		return
	}

	if next == domain.AlertRuleStateFiring {
		e.metrics.IncFired()
	} else {
		e.metrics.IncRecovered()
	}
}

// sustainedBreach reports whether every sample in [from, now] breaches
// threshold under comparator AND the samples returned actually cover the
// window's start (within evaluationCoverageSlack) — a metric that only
// recently started reporting, or only recently started breaching, has not
// sustained a breach for the full ForDuration even though every sample it
// does have breaches. samples must be ordered by Timestamp ascending (the
// TelemetryReader contract).
//
// A window whose true sample count exceeds domain.MaxTelemetryQueryLimit is
// a special case: TelemetryReader.QueryRangeForTenant then hands back only
// the MOST RECENT MaxTelemetryQueryLimit samples (its own doc comment
// explains why), so samples[0] is not actually the window's start at all —
// it is simply the oldest sample that fit under the cap, and comparing it
// against `from` would wrongly refuse a genuinely sustained, currently-firing
// breach just because the window is long and the metric is high-frequency
// (e.g. a 24h ForDuration on a 10s-interval metric ≈ 8640 samples > 5000).
// In that case every fetched sample breaching IS the answer: "is it
// breaching now, for as much of the recent window as this cap can see" —
// the operationally correct question when the whole window cannot be
// examined at all. The window-start coverage check therefore applies only
// when the fetch was NOT capped.
func sustainedBreach(samples []domain.Sample, cmp domain.AlertComparator, threshold float64, from time.Time) bool {
	if len(samples) == 0 {
		return false
	}
	for _, s := range samples {
		if !cmp.Breached(s.Value, threshold) {
			return false
		}
	}
	capped := len(samples) >= domain.MaxTelemetryQueryLimit
	if capped {
		return true
	}
	return !samples[0].Timestamp.After(from.Add(evaluationCoverageSlack))
}

// notify builds and enqueues the transition's Notification. Recipient is the
// watched asset — there is no on-call/assignment concept yet (E5.2 defers
// routing) — and the channel is in-app: it requires no external
// configuration, so a rule always produces a tracked record even before any
// email/webhook channel is wired, the same default policy.PolicyNotifier
// chooses when a policy names no channel.
func (e *Evaluator) notify(ctx context.Context, rule *domain.AlertRule, next domain.AlertRuleState) error {
	var subject, body string
	switch next {
	case domain.AlertRuleStateFiring:
		subject = fmt.Sprintf("[%s] %s %s %v on asset %s", rule.Severity, rule.Metric, rule.Comparator, rule.Threshold, rule.AssetID)
		body = fmt.Sprintf("rule_id=%s asset_id=%s metric=%s comparator=%s threshold=%v severity=%s state=firing",
			rule.RuleID, rule.AssetID, rule.Metric, rule.Comparator, rule.Threshold, rule.Severity)
	default:
		subject = fmt.Sprintf("[%s] recovered: %s on asset %s", rule.Severity, rule.Metric, rule.AssetID)
		body = fmt.Sprintf("rule_id=%s asset_id=%s metric=%s state=ok", rule.RuleID, rule.AssetID, rule.Metric)
	}

	n, err := domain.NewNotification(rule.TenantID, domain.NotificationInApp, rule.AssetID, subject, body)
	if err != nil {
		return fmt.Errorf("build notification: %w", err)
	}
	_, err = e.notifier.Enqueue(ctx, n)
	return err
}

// correlate performs E4.1's alert->incident wiring for one transition and
// returns the CurrentIncidentID value the caller's RecordTransition should
// persist. Only called when e.correlator is non-nil — see NewEvaluator's doc
// comment for what happens when it is not.
//
//   - ok->firing: finds the tenant's OPEN, alert-sourced incident already
//     linked to rule.AssetID and appends a "fired" note to it, or creates one
//     (Source=alert, Severity mapped from the rule's own, Title naming the
//     rule/metric/asset) when none exists — see
//     IncidentCorrelator.FindOrCreateOpenAlertIncident's doc comment for how
//     "create xor link, never both" is enforced. Returns the linked/created
//     incident's id.
//   - firing->ok: appends a "recovered" note to rule.CurrentIncidentID's
//     incident and returns nil, clearing the link. The incident itself is
//     left exactly as it is — E4.1 correlates, it does not remediate; an
//     operator, not this package, decides when the underlying incident is
//     actually resolved (docs/PLATFORM-BUILD-PLAN.md E4's scope note).
//     rule.CurrentIncidentID nil (nothing was ever linked, e.g. this rule's
//     prior firing predates E4.1's own rollout) is a no-op.
//
// rule.AssetID == "" — domain.AlertRule.Validate requires a non-empty
// AssetID today, so this is unreachable — falls back to notify-only
// (returns rule.CurrentIncidentID unchanged) rather than guessing an asset
// or failing the whole transition over a defensive branch that cannot
// currently trigger.
func (e *Evaluator) correlate(ctx context.Context, rule *domain.AlertRule, next domain.AlertRuleState) (*string, error) {
	if strings.TrimSpace(rule.AssetID) == "" {
		return rule.CurrentIncidentID, nil
	}

	if next == domain.AlertRuleStateOK {
		if rule.CurrentIncidentID == nil {
			return nil, nil
		}
		note := fmt.Sprintf("alert %s recovered (rule_id=%s, comparator=%s, threshold=%v)",
			rule.Metric, rule.RuleID, rule.Comparator, rule.Threshold)
		if err := e.correlator.AppendAlertNote(ctx, rule.TenantID, *rule.CurrentIncidentID, note, alertingSystemActor); err != nil {
			return nil, fmt.Errorf("append recovery note: %w", err)
		}
		return nil, nil
	}

	title := fmt.Sprintf("[%s] %s %s %v on asset %s", rule.Severity, rule.Metric, rule.Comparator, rule.Threshold, rule.AssetID)
	body := fmt.Sprintf("rule_id=%s metric=%s comparator=%s threshold=%v severity=%s",
		rule.RuleID, rule.Metric, rule.Comparator, rule.Threshold, rule.Severity)
	want, err := domain.NewAlertIncident(rule.TenantID, title, body, alertSeverityToIncidentSeverity(rule.Severity), rule.AssetID)
	if err != nil {
		return nil, fmt.Errorf("build alert incident: %w", err)
	}
	note := fmt.Sprintf("alert %s fired (rule_id=%s, comparator=%s, threshold=%v)",
		rule.Metric, rule.RuleID, rule.Comparator, rule.Threshold)
	id, err := e.correlator.FindOrCreateOpenAlertIncident(ctx, want, alertingSystemActor, note)
	if err != nil {
		return nil, fmt.Errorf("find-or-create alert incident: %w", err)
	}
	return &id, nil
}

// alertSeverityToIncidentSeverity maps AlertSeverity's three tiers onto
// IncidentSeverity's four for a newly CREATED alert-sourced incident (E4.1;
// an incident this correlation only LINKS to keeps whatever severity it was
// filed or last set to). critical maps directly; warning — worth acting on
// but not the top tier — maps to high rather than medium, so it is not
// mistaken for a low-urgency, manually triaged incident; info maps to low.
// medium is reachable only by an operator's own choice, never by this
// mapping.
func alertSeverityToIncidentSeverity(s domain.AlertSeverity) domain.IncidentSeverity {
	switch s {
	case domain.AlertSeverityCritical:
		return domain.IncidentSeverityCritical
	case domain.AlertSeverityWarning:
		return domain.IncidentSeverityHigh
	default:
		return domain.IncidentSeverityLow
	}
}
