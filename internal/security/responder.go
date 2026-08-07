package security

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/policy"
)

// responderSystemActor names this package's Responder as the source of the
// policy.Event it hands to the action Registry (ev.Actor below) — the
// RESPONSE analog of securityDetectorSystemActor/iocMatcherSystemActor: a
// downstream webhook payload or notification can tell which pipeline
// produced it.
const responderSystemActor = "system:security-responder"

// ResponderConfig parameterises the Responder — the identical Config/Store/
// Run/RunOnce shape Detector/IOCMatcher use (this package's own doc comment),
// applied to E8.5's response-rule matching instead of detection. A SEPARATE
// type from Config/IOCMatcherConfig: this worker's window is over
// incident.created_at, not security_observation, and it has its own
// independent page/cap knobs.
type ResponderConfig struct {
	// Interval between response passes AND the trailing window each pass
	// evaluates (default 30s) — the SAME window==interval TILING discipline
	// IOCMatcherConfig.Interval's own doc comment states, applied here to
	// incident.created_at instead of security_observation.observed_at: an
	// incident is considered by AT MOST one pass, because pass N's window is
	// (now-Interval, now] and pass N+1's is (now, now+Interval].
	Interval time.Duration
	// RulePageSize bounds one EnabledSecurityResponseRulesForTenant page
	// (default 200).
	RulePageSize int
	// IncidentPageSize bounds one RecentSecurityIncidentsForTenant page
	// (default 500).
	IncidentPageSize int
	// MaxRulesPerTenant caps how many of one tenant's enabled rules one tick
	// loads, across every page (default 5000) — mirrors
	// IOCMatcherConfig.MaxIOCsPerTenant.
	MaxRulesPerTenant int
	// MaxIncidentsPerTenant caps how many of one tenant's security incidents
	// one tick reads, across every page (default 5000) — mirrors
	// IOCMatcherConfig.MaxObservationsPerTenant.
	MaxIncidentsPerTenant int
	// MaxTenantsPerTick caps how many tenants-with-enabled-rules one tick
	// processes (default 5000).
	MaxTenantsPerTick int
	// Concurrency bounds how many tenants are processed at once (default 8).
	Concurrency int
}

func (c *ResponderConfig) withDefaults() {
	if c.Interval <= 0 {
		c.Interval = 30 * time.Second
	}
	if c.RulePageSize <= 0 {
		c.RulePageSize = 200
	}
	if c.IncidentPageSize <= 0 {
		c.IncidentPageSize = 500
	}
	if c.MaxRulesPerTenant <= 0 {
		c.MaxRulesPerTenant = 5000
	}
	if c.MaxIncidentsPerTenant <= 0 {
		c.MaxIncidentsPerTenant = 5000
	}
	if c.MaxTenantsPerTick <= 0 {
		c.MaxTenantsPerTick = 5000
	}
	if c.Concurrency <= 0 {
		c.Concurrency = 8
	}
}

// Responder is the leader-gated security-response-automation worker
// completing E8 (E8.5, ADR-SOC-010): each pass, per tenant holding at least
// one enabled security_response_rule, it loads that tenant's enabled rules
// and its trailing-window, SECURITY-sourced incidents and, for every
// (incident, rule) match not already dispatched, runs the rule's SAFE
// action via the EXISTING policy.Registry — exactly once per (incident,
// rule), via security_response_dispatch's claim-first ledger (see
// DispatchClaimer's own doc comment).
//
// This engine is triggered directly off the SECURITY-INCIDENT lifecycle
// (domain.Incident rows whose Source is domain.IncidentSourceSecurity) — it
// is NEVER wired through policy.Consumer/audit_event: that consumer tails
// the constitutional GOVERNANCE hash-chain (12 closed
// domain.ConfigurationOperation values), and a security incident has no
// place on it. This package adds no domain.ConfigurationOperation value and
// this file imports nothing from the audit/governance path.
//
// SAFE ONLY: ActionType is restricted, at Create time
// (domain.SecurityResponseRule.Validate), to domain.SafeSecurityResponseActionTypes
// ("http", "notification") — the OUTBOUND-safe palette. There is no
// "command"/arbitrary-execution path reachable from this worker, and no
// destructive/response action (isolate, block, disable, quarantine) exists
// anywhere in this codebase for it to run even if one were later added to
// policy.Registry: this worker only ever calls ActionRunner.Run with an
// ActionType a rule was allowed to store, and that allowlist is enforced
// independently at the database (ck_security_response_rule_action_type) and
// in domain.SecurityResponseRule.Validate.
//
// Deliberately a SIBLING worker to Detector/IOCMatcher, not a hook inside
// either: it evaluates a different config table (security_response_rule)
// over a different read (incident, not security_observation) and produces a
// different consequence (an outbound SAFE action, not an Incident
// create/link) — there is no shared per-tenant iteration shape worth
// merging beyond the leader-gated Run/RunOnce scaffold itself.
type Responder struct {
	rules      ResponseRuleLister
	incidents  IncidentWindowReader
	dispatches DispatchClaimer
	actions    ActionRunner
	metrics    ResponderMetrics
	log        *slog.Logger
	now        func() time.Time
	cfg        ResponderConfig
}

// NewResponder builds the responder over the privileged pool's
// ResponseRuleLister/IncidentWindowReader/DispatchClaimer and the
// policy action Registry (ActionRunner). actions MUST NOT be nil — running
// the rule's SAFE action is this worker's ONLY consequence of a match, the
// identical non-decision NewDetector/NewIOCMatcher's own doc comments state
// for their own correlator argument.
func NewResponder(
	rules ResponseRuleLister, incidents IncidentWindowReader, dispatches DispatchClaimer,
	actions ActionRunner, metrics ResponderMetrics, log *slog.Logger, cfg ResponderConfig,
) *Responder {
	if log == nil {
		log = slog.Default()
	}
	if metrics == nil {
		metrics = NopResponderMetrics{}
	}
	cfg.withDefaults()
	return &Responder{
		rules: rules, incidents: incidents, dispatches: dispatches, actions: actions,
		metrics: metrics, log: log,
		now: func() time.Time { return time.Now().UTC() }, cfg: cfg,
	}
}

// Run responds on Interval until ctx is cancelled — mirrors
// Detector.Run/IOCMatcher.Run exactly.
func (r *Responder) Run(ctx context.Context) error {
	r.log.Info("security responder started", "interval", r.cfg.Interval.String())
	r.RunOnce(ctx)
	t := time.NewTicker(r.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			r.log.Info("security responder stopped", "reason", ctx.Err())
			return ctx.Err()
		case <-t.C:
			r.RunOnce(ctx)
		}
	}
}

// RunOnce responds for every tenant that currently holds at least one
// enabled security_response_rule, up to MaxTenantsPerTick, with bounded
// concurrency, over the trailing (now-Interval, now] window — mirrors
// IOCMatcher.RunOnce exactly, including the tenant-isolation-by-construction
// per-tenant loop (each tenant's own rules and own incidents, never a
// cross-tenant map).
func (r *Responder) RunOnce(ctx context.Context) {
	now := r.now()
	from := now.Add(-r.cfg.Interval)

	tenants, err := r.rules.TenantsWithEnabledSecurityResponseRules(ctx)
	if err != nil {
		r.log.Error("security responder: list tenants with enabled rules", "err", err)
		r.metrics.IncErrors()
		return
	}

	sem := make(chan struct{}, r.cfg.Concurrency)
	var wg sync.WaitGroup
	processed := 0
	for _, tenantID := range tenants {
		if ctx.Err() != nil {
			break
		}
		processed++
		if processed > r.cfg.MaxTenantsPerTick {
			break
		}
		tenantID := tenantID
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			r.respondTenant(ctx, tenantID, from, now)
		}()
	}
	wg.Wait()
}

// respondTenant loads tenantID's enabled rules and trailing-window security
// incidents and dispatches every match. Both reads carry tenantID as an
// EXPLICIT argument (ADR-TENANCY-012): this pass runs on the privileged
// pool, which has row-level security switched off, so nothing else confines
// either read to tenantID.
func (r *Responder) respondTenant(ctx context.Context, tenantID string, from, to time.Time) {
	rules, err := r.loadEnabledRules(ctx, tenantID)
	if err != nil {
		r.log.Error("security responder: load enabled rules", "tenant_id", tenantID, "err", err)
		r.metrics.IncErrors()
		return
	}
	if len(rules) == 0 {
		// A race with the rule being disabled/deleted between
		// TenantsWithEnabledSecurityResponseRules and here; nothing to match
		// against this tick.
		r.metrics.IncTenantsProcessed()
		return
	}

	var after *domain.Incident
	total := 0
	for {
		if ctx.Err() != nil {
			return
		}
		remaining := r.cfg.MaxIncidentsPerTenant - total
		if remaining <= 0 {
			break
		}
		pageSize := r.cfg.IncidentPageSize
		if pageSize > remaining {
			pageSize = remaining
		}
		page, err := r.incidents.RecentSecurityIncidentsForTenant(ctx, tenantID, from, to, pageSize, after)
		if err != nil {
			r.log.Error("security responder: read recent security incidents", "tenant_id", tenantID, "err", err)
			r.metrics.IncErrors()
			return
		}
		if len(page) == 0 {
			break
		}
		for i := range page {
			inc := page[i]
			r.metrics.IncIncidentsChecked()
			for _, rule := range rules {
				if !ruleMatchesIncident(rule, &inc) {
					continue
				}
				r.metrics.IncMatched()
				r.dispatch(ctx, tenantID, &inc, rule)
			}
		}
		after = &page[len(page)-1]
		total += len(page)
		if len(page) < pageSize {
			break
		}
	}
	r.metrics.IncTenantsProcessed()
}

// loadEnabledRules pages through tenantID's enabled rules, up to
// MaxRulesPerTenant, into a slice ordered by rule_id — deterministic, the
// same reason IOCMatcher.loadEnabledIOCs's own doc comment gives for
// indexing rather than trusting map order, applied here to a slice because
// (unlike ioc's per-(type,value) index) there is no natural lookup key
// narrower than "every enabled rule this tenant has".
func (r *Responder) loadEnabledRules(ctx context.Context, tenantID string) ([]*domain.SecurityResponseRule, error) {
	var out []*domain.SecurityResponseRule
	after := ""
	total := 0
	for {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		remaining := r.cfg.MaxRulesPerTenant - total
		if remaining <= 0 {
			break
		}
		pageSize := r.cfg.RulePageSize
		if pageSize > remaining {
			pageSize = remaining
		}
		page, err := r.rules.EnabledSecurityResponseRulesForTenant(ctx, tenantID, pageSize, after)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		for _, rule := range page {
			if !rule.Enabled {
				// Defensive belt-and-suspenders — the same
				// "trust the port's contract, verify it anyway" discipline
				// IOCMatcher.loadEnabledIOCs applies to its own enabled=true
				// filter.
				continue
			}
			out = append(out, rule)
		}
		after = page[len(page)-1].RuleID
		total += len(page)
		if len(page) < pageSize {
			break
		}
	}
	return out, nil
}

// ruleMatchesIncident is E8.5's match rule (ADR-SOC-010): inc.Severity is at
// least rule.MinSeverity, and — when rule.AssetID is set — inc.AssetID
// equals it exactly. inc is always security-sourced already (the caller
// only ever passes rows RecentSecurityIncidentsForTenant returned), so
// Source is not re-checked here.
func ruleMatchesIncident(rule *domain.SecurityResponseRule, inc *domain.Incident) bool {
	if !inc.Severity.AtLeast(rule.MinSeverity) {
		return false
	}
	if rule.AssetID == nil {
		return true
	}
	return inc.AssetID != nil && *inc.AssetID == *rule.AssetID
}

// dispatch runs one matched (incident, rule) pair's SAFE action — see
// DispatchClaimer's own doc comment for the record-first, at-most-once
// ordering: the ledger row is claimed BEFORE the action runs, so a crash
// between the two silently drops that one firing rather than risking a
// duplicate outbound call on the next pass or a process restart. The action
// input is synthesised from the incident (id, source, severity, asset,
// title) plus the matched rule's own identity — never anything read from
// audit_event, because none of this package's reads touch that chain.
func (r *Responder) dispatch(ctx context.Context, tenantID string, inc *domain.Incident, rule *domain.SecurityResponseRule) {
	now := r.now()
	claimed, err := r.dispatches.ClaimDispatch(ctx, tenantID, inc.IncidentID, rule.RuleID, rule.ActionType, now)
	if err != nil {
		r.log.Error("security responder: claim dispatch", "tenant_id", tenantID,
			"incident_id", inc.IncidentID, "rule_id", rule.RuleID, "err", err)
		r.metrics.IncErrors()
		return
	}
	if !claimed {
		// Already dispatched by an earlier pass (or a concurrent goroutine
		// within this one) — the ordinary exactly-once outcome, not a
		// failure.
		r.metrics.IncSkippedAlreadyDispatched()
		return
	}

	ev := responseEvent(tenantID, inc, rule, now)
	if err := r.actions.Run(ctx, rule.ActionType, ev, rule.ActionConfig); err != nil {
		r.log.Error("security responder: run action", "tenant_id", tenantID,
			"incident_id", inc.IncidentID, "rule_id", rule.RuleID, "action_type", rule.ActionType, "err", err)
		r.metrics.IncErrors()
		return
	}
	r.metrics.IncDispatched()
}

// responseEvent synthesises the policy.Event ActionRunner.Run receives from
// the matched incident and rule — the SAFE action's own input, never a row
// read from or written to audit_event. EventID is the (incident, rule) pair
// itself (stable and unique per dispatch, so a downstream consumer that
// happens to dedup on EventID sees the same behaviour a governance policy
// execution would); CfgID names the incident, mirroring how a governance
// event's CfgID names its own configuration object.
func responseEvent(tenantID string, inc *domain.Incident, rule *domain.SecurityResponseRule, at time.Time) policy.Event {
	meta := map[string]string{
		"incident_id": inc.IncidentID,
		"source":      string(inc.Source),
		"severity":    string(inc.Severity),
		"title":       inc.Title,
		"rule_id":     rule.RuleID,
		"rule_name":   rule.Name,
	}
	if inc.AssetID != nil {
		meta["asset_id"] = *inc.AssetID
	}
	return policy.Event{
		TenantID:    tenantID,
		EventID:     "secresp_" + inc.IncidentID + "_" + rule.RuleID,
		OperationID: inc.IncidentID,
		Operation:   "security.incident.response",
		Actor:       responderSystemActor,
		CfgID:       inc.IncidentID,
		OccurredAt:  at,
		Metadata:    meta,
	}
}
