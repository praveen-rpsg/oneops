package security

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// iocMatcherSystemActor identifies this file as the actor on every Incident
// timeline row it writes — the IOC-match analog of securityDetectorSystemActor
// (this package's own doc comment on that constant explains why a distinct
// per-pipeline identity matters: a reader of the timeline can tell a
// threshold/window detection from a watchlist match).
const iocMatcherSystemActor = "system:ioc-matcher"

// iocIndicatorTypeOrder fixes a deterministic iteration order over
// domain.IOCIndicatorType's closed set for matchObservation below — see that
// function's own doc comment for why determinism matters here.
var iocIndicatorTypeOrder = []domain.IOCIndicatorType{
	domain.IOCIndicatorTypeIP,
	domain.IOCIndicatorTypeDomain,
	domain.IOCIndicatorTypeURL,
	domain.IOCIndicatorTypeFileHash,
	domain.IOCIndicatorTypeEmail,
}

// IOCMatcherConfig parameterises the IOCMatcher — the identical
// Config/Store/Run/RunOnce shape Detector uses (this package's own doc
// comment), applied to E8.2b's watchlist match instead of E8.1b-2's
// threshold/window detection. A SEPARATE type from Config: the two workers
// share no field whose default would make sense shared (a rule's own
// WindowSeconds lives on the row; an ioc carries no window at all).
type IOCMatcherConfig struct {
	// Interval between match passes AND the trailing window each pass
	// evaluates (default 30s, matching Config.Interval's own default): a
	// security_observation is evaluated against the watchlist by AT MOST one
	// pass, because pass N's window is (now-Interval, now] and pass N+1's is
	// (now, now+Interval] — consecutive windows TILE rather than overlap
	// (docs/PLATFORM-BUILD-PLAN.md E8.2b, ADR-SOC-005's "process once"
	// discipline). There is deliberately no separate window knob: widening
	// Interval widens the window identically, and decoupling them would
	// reintroduce the "same observation re-noted every pass" hazard this
	// design specifically avoids.
	Interval time.Duration
	// IOCPageSize bounds one EnabledIOCsForTenant page (default 200).
	IOCPageSize int
	// ObservationPageSize bounds one RecentForTenant page (default 500,
	// matching domain.DefaultSecurityObservationQueryLimit).
	ObservationPageSize int
	// MaxIOCsPerTenant caps how many of one tenant's enabled iocs one tick
	// loads, across every page (default 5000) — the "no unbounded pull"
	// discipline Config.MaxRulesPerTick applies, narrowed to one tenant.
	MaxIOCsPerTenant int
	// MaxObservationsPerTenant caps how many of one tenant's observations one
	// tick reads, across every page (default
	// domain.MaxSecurityObservationQueryLimit) — the same numeric bound
	// QueryRangeForTenant already applies to a single asset/type, applied
	// here to a tenant's whole window instead.
	MaxObservationsPerTenant int
	// MaxTenantsPerTick caps how many tenants-with-enabled-iocs one tick
	// processes (default 5000) — mirrors Config.MaxRulesPerTick's role, one
	// level up (tenants instead of rules).
	MaxTenantsPerTick int
	// Concurrency bounds how many tenants are matched at once (default 8).
	Concurrency int
}

func (c *IOCMatcherConfig) withDefaults() {
	if c.Interval <= 0 {
		c.Interval = 30 * time.Second
	}
	if c.IOCPageSize <= 0 {
		c.IOCPageSize = 200
	}
	if c.ObservationPageSize <= 0 {
		c.ObservationPageSize = domain.DefaultSecurityObservationQueryLimit
	}
	if c.MaxIOCsPerTenant <= 0 {
		c.MaxIOCsPerTenant = 5000
	}
	if c.MaxObservationsPerTenant <= 0 {
		c.MaxObservationsPerTenant = domain.MaxSecurityObservationQueryLimit
	}
	if c.MaxTenantsPerTick <= 0 {
		c.MaxTenantsPerTick = 5000
	}
	if c.Concurrency <= 0 {
		c.Concurrency = 8
	}
}

// IOCMatcher is the leader-gated IOC-watchlist detector (E8.2b, ADR-SOC-005):
// each pass, per tenant holding at least one enabled ioc, it loads that
// tenant's enabled watchlist and its trailing-window security_observations
// and raises or links a security-sourced Incident on a value match — reusing
// Detector's exact IncidentCorrelator (E8.1b-2) rather than a parallel
// correlation path, and the SAME FindOrCreateOpenSecurityIncident
// no-duplicate guarantee (ux_incident_open_security_per_asset). See
// matchObservation's doc comment for the match rule itself (ADR-SOC-004's
// value-based, per-candidate-type-normalized comparison).
//
// Deliberately a SIBLING worker to Detector, not a second pass folded into
// RunOnce: the two evaluate different config tables (security_rule vs ioc)
// over structurally different SQL shapes (a bounded per-asset/type COUNT vs a
// bounded cross-asset/type row read) and have no transition state to share
// (a security_rule persists last_state/current_incident_id; an ioc carries no
// match state of its own — domain.IOC's own doc comment). One method trying
// to do both would tangle two unrelated per-tenant iteration shapes together
// for no shared benefit beyond the (already reused) IncidentCorrelator.
type IOCMatcher struct {
	iocs         IOCLister
	observations ObservationWindowReader
	correlator   IncidentCorrelator
	metrics      IOCMatcherMetrics
	log          *slog.Logger
	now          func() time.Time
	cfg          IOCMatcherConfig
}

// NewIOCMatcher builds the matcher over the privileged pool's IOCLister and
// ObservationWindowReader. correlator MUST NOT be nil — the identical
// non-decision NewDetector's own doc comment states: correlation into an
// Incident is this package's ONLY consequence of a match, so a nil correlator
// would make every match a silent no-op.
func NewIOCMatcher(iocs IOCLister, observations ObservationWindowReader, correlator IncidentCorrelator, metrics IOCMatcherMetrics, log *slog.Logger, cfg IOCMatcherConfig) *IOCMatcher {
	if log == nil {
		log = slog.Default()
	}
	if metrics == nil {
		metrics = NopIOCMatcherMetrics{}
	}
	cfg.withDefaults()
	return &IOCMatcher{
		iocs: iocs, observations: observations, correlator: correlator,
		metrics: metrics, log: log,
		now: func() time.Time { return time.Now().UTC() }, cfg: cfg,
	}
}

// Run matches on Interval until ctx is cancelled — mirrors Detector.Run.
func (m *IOCMatcher) Run(ctx context.Context) error {
	m.log.Info("ioc matcher started", "interval", m.cfg.Interval.String())
	m.RunOnce(ctx)
	t := time.NewTicker(m.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			m.log.Info("ioc matcher stopped", "reason", ctx.Err())
			return ctx.Err()
		case <-t.C:
			m.RunOnce(ctx)
		}
	}
}

// RunOnce matches every tenant that currently holds at least one enabled ioc,
// up to MaxTenantsPerTick, with bounded concurrency, over the trailing
// (now-Interval, now] window — see IOCMatcherConfig.Interval's doc comment
// for why the window is fixed to the pass interval. Each tenant is
// independent: its own watchlist and its own observations, never a
// cross-tenant map (the same tenant-isolation-by-construction
// grouping.Reconciler.RunOnce's own doc comment states for its tenant loop).
func (m *IOCMatcher) RunOnce(ctx context.Context) {
	now := m.now()
	from := now.Add(-m.cfg.Interval)

	tenants, err := m.iocs.TenantsWithEnabledIOCs(ctx)
	if err != nil {
		m.log.Error("ioc matcher: list tenants with enabled iocs", "err", err)
		m.metrics.IncErrors()
		return
	}

	sem := make(chan struct{}, m.cfg.Concurrency)
	var wg sync.WaitGroup
	processed := 0
	for _, tenantID := range tenants {
		if ctx.Err() != nil {
			break
		}
		processed++
		if processed > m.cfg.MaxTenantsPerTick {
			break
		}
		tenantID := tenantID
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			m.matchTenant(ctx, tenantID, from, now)
		}()
	}
	wg.Wait()
}

// matchTenant loads tenantID's enabled watchlist and trailing-window
// observations and correlates every match. Both reads carry tenantID as an
// EXPLICIT argument (ADR-TENANCY-012): this pass runs on the privileged pool,
// which has row-level security switched off, so nothing else confines
// either read to tenantID.
func (m *IOCMatcher) matchTenant(ctx context.Context, tenantID string, from, to time.Time) {
	idx, err := m.loadEnabledIOCs(ctx, tenantID)
	if err != nil {
		m.log.Error("ioc matcher: load enabled iocs", "tenant_id", tenantID, "err", err)
		m.metrics.IncErrors()
		return
	}
	if len(idx) == 0 {
		// A race with the ioc being disabled/deleted between
		// TenantsWithEnabledIOCs and here; nothing to match against this
		// tick.
		m.metrics.IncTenantsProcessed()
		return
	}

	var after *domain.SecurityObservation
	total := 0
	for {
		if ctx.Err() != nil {
			return
		}
		remaining := m.cfg.MaxObservationsPerTenant - total
		if remaining <= 0 {
			break
		}
		pageSize := m.cfg.ObservationPageSize
		if pageSize > remaining {
			pageSize = remaining
		}
		page, err := m.observations.RecentForTenant(ctx, tenantID, from, to, pageSize, after)
		if err != nil {
			m.log.Error("ioc matcher: read recent observations", "tenant_id", tenantID, "err", err)
			m.metrics.IncErrors()
			return
		}
		if len(page) == 0 {
			break
		}
		for i := range page {
			obs := page[i]
			m.metrics.IncObservationsChecked()
			if ioc := matchObservation(idx, &obs); ioc != nil {
				m.correlate(ctx, tenantID, &obs, ioc)
			}
		}
		after = &page[len(page)-1]
		total += len(page)
		if len(page) < pageSize {
			break
		}
	}
	m.metrics.IncTenantsProcessed()
}

// loadEnabledIOCs pages through tenantID's enabled watchlist, up to
// MaxIOCsPerTenant, and indexes it by (IndicatorType, IndicatorValue) for
// matchObservation's lookups. A defensive `!i.Enabled` skip is kept here even
// though EnabledIOCsForTenant's own SQL already filters on enabled=true — the
// same "trust the port's contract, verify it anyway" belt-and-suspenders
// FindOrCreateOpenSecurityIncident's own doc comment applies to Validate.
func (m *IOCMatcher) loadEnabledIOCs(ctx context.Context, tenantID string) (map[domain.IOCIndicatorType]map[string]*domain.IOC, error) {
	idx := map[domain.IOCIndicatorType]map[string]*domain.IOC{}
	after := ""
	total := 0
	for {
		if ctx.Err() != nil {
			return idx, ctx.Err()
		}
		remaining := m.cfg.MaxIOCsPerTenant - total
		if remaining <= 0 {
			break
		}
		pageSize := m.cfg.IOCPageSize
		if pageSize > remaining {
			pageSize = remaining
		}
		page, err := m.iocs.EnabledIOCsForTenant(ctx, tenantID, pageSize, after)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		for _, i := range page {
			if !i.Enabled {
				continue
			}
			byValue, ok := idx[i.IndicatorType]
			if !ok {
				byValue = map[string]*domain.IOC{}
				idx[i.IndicatorType] = byValue
			}
			byValue[i.IndicatorValue] = i
		}
		after = page[len(page)-1].IOCID
		total += len(page)
		if len(page) < pageSize {
			break
		}
	}
	return idx, nil
}

// matchObservation returns the enabled IOC obs matches, or nil — E8.2b's
// value-based match (ADR-SOC-004, ADR-SOC-005): obs matches an ioc when ANY
// value in obs.Attributes, normalized the SAME way
// domain.NormalizeIOCIndicatorValue normalizes indicator_value but FOR THAT
// CANDIDATE IOC'S OWN indicator_type, equals it. Type-aware field-key mapping
// (only comparing e.g. a "src_ip"-keyed attribute against ip-typed iocs) is
// DEFERRED — an HONEST BOUND, not an oversight: an attribute value that
// happens to equal an enabled ip-typed indicator matches even if the
// attribute's own key is named "domain", "actor", or anything else.
//
// Deterministic: iterates indicator types in a fixed order
// (iocIndicatorTypeOrder) and attribute keys in sorted order, so which of two
// simultaneously-matching iocs (a genuine edge case: two enabled iocs of
// different types whose normalized forms both equal some attribute value)
// "wins" the correlation's title/severity is reproducible, never
// map-iteration-order-dependent.
func matchObservation(idx map[domain.IOCIndicatorType]map[string]*domain.IOC, obs *domain.SecurityObservation) *domain.IOC {
	if len(idx) == 0 || len(obs.Attributes) == 0 {
		return nil
	}
	keys := make([]string, 0, len(obs.Attributes))
	for k := range obs.Attributes {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, iType := range iocIndicatorTypeOrder {
		values, ok := idx[iType]
		if !ok {
			continue
		}
		for _, k := range keys {
			normalized := domain.NormalizeIOCIndicatorValue(iType, obs.Attributes[k])
			if ioc, found := values[normalized]; found {
				return ioc
			}
		}
	}
	return nil
}

// correlate wires one match into Incident creation/linking — the SECURITY
// analog of Detector.correlate's firing half, with no recovery counterpart:
// an IOC match has no "un-match" transition to recover from (ADR-SOC-005) —
// domain.IOC carries no match state, unlike domain.SecurityRule's
// last_state/current_incident_id.
//
// Severity is set by whichever match reaches FindOrCreateOpenSecurityIncident
// FIRST for a given (tenant, asset): a later match on an already-open
// incident only appends a note (noteOnLink), it never escalates the
// incident's stored Severity — the identical first-writer-wins bound
// FindOrCreateOpenSecurityIncident's own doc comment already accepts for
// title/description, stated here explicitly as an HONEST BOUND for severity
// (ADR-SOC-005).
func (m *IOCMatcher) correlate(ctx context.Context, tenantID string, obs *domain.SecurityObservation, ioc *domain.IOC) {
	title := fmt.Sprintf("[%s] IOC match: %s %s on asset %s", ioc.Severity, ioc.IndicatorType, ioc.IndicatorValue, obs.AssetID)
	note := fmt.Sprintf("ioc match: ioc_id=%s indicator_type=%s indicator_value=%s observation_type=%s source=%s observed_at=%s",
		ioc.IOCID, ioc.IndicatorType, ioc.IndicatorValue, obs.ObservationType, obs.Source, obs.ObservedAt.Format(time.RFC3339))
	want, err := domain.NewSecurityIncident(tenantID, title, note, ioc.Severity, obs.AssetID)
	if err != nil {
		m.log.Error("ioc matcher: build security incident", "tenant_id", tenantID, "ioc_id", ioc.IOCID, "err", err)
		m.metrics.IncErrors()
		return
	}
	if _, err := m.correlator.FindOrCreateOpenSecurityIncident(ctx, want, iocMatcherSystemActor, note); err != nil {
		m.log.Error("ioc matcher: find-or-create security incident", "tenant_id", tenantID, "ioc_id", ioc.IOCID, "err", err)
		m.metrics.IncErrors()
		return
	}
	m.metrics.IncMatched()
}
