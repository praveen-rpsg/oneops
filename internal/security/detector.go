package security

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

// securityDetectorSystemActor identifies this package as the actor on every
// Incident timeline row it writes (create/link/recovery notes) — so an
// operator reading an incident's timeline can tell a system-derived entry
// from their own action. Distinct from alerting.alertingSystemActor: a
// reader of the timeline can tell which detection pipeline produced a note.
const securityDetectorSystemActor = "system:security"

// Config parameterises the SecurityDetector — mirrors alerting.Config
// exactly.
type Config struct {
	// Interval between evaluation passes (default 30s, matching
	// alerting.Config.Interval's own default and reasoning).
	Interval time.Duration
	// PageSize bounds one EnabledRules page (default 200).
	PageSize int
	// MaxRulesPerTick caps how many rules one RunOnce evaluates in total,
	// across every page (default 5000) — mirrors alerting.Config.MaxRulesPerTick.
	MaxRulesPerTick int
	// Concurrency bounds how many rules are evaluated at once (default 8):
	// each evaluation is one security_observation count plus, on a
	// transition, one incident correlation.
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

// SecurityDetector is the leader-gated SIEM detector — see the package doc
// comment.
type Detector struct {
	store      Store
	counter    ObservationCounter
	correlator IncidentCorrelator
	metrics    Metrics
	log        *slog.Logger
	now        func() time.Time
	cfg        Config
}

// NewDetector builds the detector over the privileged pool's Store and
// ObservationCounter. Unlike alerting.NewEvaluator's optional correlator,
// correlator here MUST NOT be nil: correlation into an Incident is this
// package's ONLY consequence of a firing — there is no separate
// notify-regardless-of-correlation action the way alerting has — so a nil
// correlator would make every firing a silent no-op rather than a narrower,
// still-useful behaviour.
func NewDetector(store Store, counter ObservationCounter, correlator IncidentCorrelator, metrics Metrics, log *slog.Logger, cfg Config) *Detector {
	if log == nil {
		log = slog.Default()
	}
	if metrics == nil {
		metrics = NopMetrics{}
	}
	cfg.withDefaults()
	return &Detector{
		store: store, counter: counter, correlator: correlator,
		metrics: metrics, log: log,
		now: func() time.Time { return time.Now().UTC() }, cfg: cfg,
	}
}

// Run evaluates on Interval until ctx is cancelled — mirrors
// alerting.Evaluator.Run exactly.
func (d *Detector) Run(ctx context.Context) error {
	d.log.Info("security rule detector started", "interval", d.cfg.Interval.String())
	d.RunOnce(ctx)
	t := time.NewTicker(d.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			d.log.Info("security rule detector stopped", "reason", ctx.Err())
			return ctx.Err()
		case <-t.C:
			d.RunOnce(ctx)
		}
	}
}

// RunOnce pages through every enabled rule, up to MaxRulesPerTick, and
// evaluates each with bounded concurrency — mirrors
// alerting.Evaluator.RunOnce exactly.
func (d *Detector) RunOnce(ctx context.Context) {
	now := d.now()
	after := ""
	processed := 0

	sem := make(chan struct{}, d.cfg.Concurrency)
	for {
		if ctx.Err() != nil {
			return
		}
		rules, err := d.store.EnabledRules(ctx, d.cfg.PageSize, after)
		if err != nil {
			d.log.Error("security rule detector: list enabled rules", "err", err)
			d.metrics.IncErrors()
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
			if processed > d.cfg.MaxRulesPerTick {
				wg.Wait()
				return
			}
			sem <- struct{}{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				d.evaluateRule(ctx, rule, now)
			}()
		}
		wg.Wait()

		if len(rules) < d.cfg.PageSize {
			return
		}
	}
}

// evaluateRule counts the rule's own tenant's matching security_observation
// rows in its trailing window — see ObservationCounter's doc comment for why
// tenantID is a required, explicit argument rather than assumed from the
// connection — decides whether the count meets ThresholdCount, and on a
// state TRANSITION only, correlates into an Incident and persists the new
// state.
//
// Order matters, the same reason alerting.Evaluator.evaluateRule's own doc
// comment gives: correlate runs BEFORE RecordTransition. If the process
// crashes between the two, the next tick re-detects the same real-world
// transition and correlate runs again — harmless beyond one duplicate
// timeline note, because FindOrCreateOpenSecurityIncident's uniqueness
// constraint means it links rather than duplicates an incident it already
// created (the same "duplicate over silent loss" trade ADR-TENANCY-012 §2
// states for alerting's notify/correlate ordering).
func (d *Detector) evaluateRule(ctx context.Context, rule *domain.SecurityRule, now time.Time) {
	d.metrics.IncEvaluated()

	from := now.Add(-time.Duration(rule.WindowSeconds) * time.Second)
	count, err := d.counter.CountForTenant(ctx, rule.TenantID, rule.AssetID, rule.ObservationType, rule.MinSeverity, from, now)
	if err != nil {
		d.log.Error("security rule detector: count observations", "rule_id", rule.RuleID, "err", err)
		d.metrics.IncErrors()
		return
	}

	next := domain.SecurityRuleStateOK
	if count >= rule.ThresholdCount {
		next = domain.SecurityRuleStateFiring
	}
	if next == rule.LastState {
		return // no transition: transition-only, exactly like alerting.Evaluator
	}

	nextIncidentID, err := d.correlate(ctx, rule, next)
	if err != nil {
		d.log.Error("security rule detector: correlate incident", "rule_id", rule.RuleID, "err", err)
		d.metrics.IncErrors()
		return // do not persist the transition; retry next tick
	}

	if _, err := d.store.RecordTransition(ctx, rule.RuleID, rule.RowVersion, next, now, nextIncidentID); err != nil {
		if errors.Is(err, domain.ErrVersionMismatch) || errors.Is(err, domain.ErrNotFound) {
			// The rule was edited, disabled or deleted between this tick's read
			// and this write. Its own next read will reflect that; nothing to
			// retry with the version this tick held.
			d.log.Info("security rule detector: transition not recorded (rule changed concurrently)",
				"rule_id", rule.RuleID, "err", err)
			return
		}
		d.log.Error("security rule detector: record transition", "rule_id", rule.RuleID, "err", err)
		d.metrics.IncErrors()
		return
	}

	if next == domain.SecurityRuleStateFiring {
		d.metrics.IncFired()
	} else {
		d.metrics.IncRecovered()
	}
}

// correlate performs this package's rule->incident wiring for one transition
// and returns the CurrentIncidentID value the caller's RecordTransition
// should persist — the exact SECURITY analog of alerting.Evaluator.correlate:
//
//   - ok->firing: finds the tenant's OPEN, security-sourced incident already
//     linked to rule.AssetID and appends a "fired" note to it, or creates one
//     (Source=security, Severity=rule.IncidentSeverity, Title naming the
//     rule/observation-type/asset) when none exists. Returns the
//     linked/created incident's id.
//   - firing->ok: appends a "recovered" note to rule.CurrentIncidentID's
//     incident and returns nil, clearing the link. The incident itself is
//     left exactly as it is — this package correlates, it does not
//     remediate; an operator decides when it is actually resolved, the same
//     scope alerting.Evaluator.correlate's own doc comment states for E4.1.
//     rule.CurrentIncidentID nil is a no-op.
//
// rule.AssetID == "" — domain.SecurityRule.Validate requires a non-empty
// AssetID today, so this is unreachable — falls back to a no-op (returns
// rule.CurrentIncidentID unchanged) rather than guessing an asset or failing
// the whole transition over a defensive branch that cannot currently trigger.
func (d *Detector) correlate(ctx context.Context, rule *domain.SecurityRule, next domain.SecurityRuleState) (*string, error) {
	if strings.TrimSpace(rule.AssetID) == "" {
		return rule.CurrentIncidentID, nil
	}

	if next == domain.SecurityRuleStateOK {
		if rule.CurrentIncidentID == nil {
			return nil, nil
		}
		note := fmt.Sprintf("security rule %s recovered (rule_id=%s, threshold_count=%d)",
			rule.ObservationType, rule.RuleID, rule.ThresholdCount)
		if err := d.correlator.AppendSecurityNote(ctx, rule.TenantID, *rule.CurrentIncidentID, note, securityDetectorSystemActor); err != nil {
			return nil, fmt.Errorf("append recovery note: %w", err)
		}
		return nil, nil
	}

	title := fmt.Sprintf("[%s] %s detected on asset %s", rule.IncidentSeverity, rule.ObservationType, rule.AssetID)
	body := fmt.Sprintf("rule_id=%s observation_type=%s min_severity=%s window_seconds=%d threshold_count=%d",
		rule.RuleID, rule.ObservationType, rule.MinSeverity, rule.WindowSeconds, rule.ThresholdCount)
	want, err := domain.NewSecurityIncident(rule.TenantID, title, body, rule.IncidentSeverity, rule.AssetID)
	if err != nil {
		return nil, fmt.Errorf("build security incident: %w", err)
	}
	note := fmt.Sprintf("security rule %s fired (rule_id=%s, threshold_count=%d)",
		rule.ObservationType, rule.RuleID, rule.ThresholdCount)
	id, err := d.correlator.FindOrCreateOpenSecurityIncident(ctx, want, securityDetectorSystemActor, note)
	if err != nil {
		return nil, fmt.Errorf("find-or-create security incident: %w", err)
	}
	return &id, nil
}
