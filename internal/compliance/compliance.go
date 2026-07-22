// Package compliance is a READ-ONLY compliance & evidence engine. It composes
// deterministic compliance evidence for a governance object exclusively from
// EXISTING persisted data — governance state, audit verification, and the
// execution timeline (which already composes audit events, webhook deliveries,
// replay jobs, and policy executions). It participates in no execution,
// introduces no new execution state, and modifies no runtime subsystem: every
// method is a projection over data the other subsystems already committed.
package compliance

import (
	"context"
	"sort"
	"time"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/timeline"
)

// GovernanceReader reads committed governance state. *postgres.ConfigObjectRepo
// (via domain.ConfigObjectRepository) satisfies it.
type GovernanceReader interface {
	Get(ctx context.Context, cfgID string) (*domain.ConfigObject, error)
	List(ctx context.Context, params domain.ListParams) (*domain.Page, error)
}

// Verifier reports audit-chain integrity. audit.ChainVerifier satisfies it.
type Verifier interface {
	VerifyChain(ctx context.Context, chainID string) (domain.VerifyResult, error)
}

// TimelineReader composes the execution timeline. *timeline.Service satisfies it.
type TimelineReader interface {
	ByGovernance(ctx context.Context, cfgID string, f timeline.Filter) (timeline.Page, error)
}

// GovernanceSummary is the governance-state facet of the evidence bundle.
type GovernanceSummary struct {
	CfgID          string    `json:"cfg_id"`
	Lifecycle      string    `json:"lifecycle"`
	RetentionClass string    `json:"retention_class"`
	Authority      string    `json:"authority"`
	RowVersion     int64     `json:"row_version"`
	RatifiedBy     string    `json:"ratified_by,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// IntegritySummary is the audit-integrity facet of the evidence bundle.
type IntegritySummary struct {
	ChainID       string `json:"chain_id"`
	Verified      bool   `json:"verified"`
	Checked       int64  `json:"checked"`
	HeadSeq       int64  `json:"head_seq"`
	FirstBreakSeq *int64 `json:"first_break_seq,omitempty"`
	BreakReason   string `json:"break_reason,omitempty"`
}

// Check is one read-only compliance rule result.
type Check struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Passed      bool   `json:"passed"`
	Detail      string `json:"detail,omitempty"`
}

// Evidence is the immutable evidence bundle for a governance object. Every field
// is derived from persisted data; only GeneratedAt varies between builds.
type Evidence struct {
	GovernanceID   string            `json:"governance_id"`
	GeneratedAt    time.Time         `json:"generated_at"`
	Governance     GovernanceSummary `json:"governance"`
	Integrity      IntegritySummary  `json:"integrity"`
	Timeline       []timeline.Entry  `json:"timeline"`
	Webhooks       []timeline.Entry  `json:"webhooks"`
	Replays        []timeline.Entry  `json:"replays"`
	Policies       []timeline.Entry  `json:"policies"`
	CorrelationIDs []string          `json:"correlation_ids"`
	Checks         []Check           `json:"checks"`
	Compliant      bool              `json:"compliant"`
}

// Summary is the compact compliance status for a governance object.
type Summary struct {
	GovernanceID string    `json:"governance_id"`
	Lifecycle    string    `json:"lifecycle"`
	Verified     bool      `json:"verified"`
	Compliant    bool      `json:"compliant"`
	ChecksPassed int       `json:"checks_passed"`
	ChecksTotal  int       `json:"checks_total"`
	GeneratedAt  time.Time `json:"generated_at"`
}

// ReportPage is a paginated list of compliance summaries.
type ReportPage struct {
	Items      []Summary `json:"items"`
	NextCursor string    `json:"next_cursor,omitempty"`
}

// Metrics receives compliance observability signals.
type Metrics interface {
	IncQuery()
	IncExport()
	ObserveDuration(d time.Duration)
}

// NopMetrics discards signals.
type NopMetrics struct{}

// IncQuery implements Metrics.
func (NopMetrics) IncQuery() {}

// IncExport implements Metrics.
func (NopMetrics) IncExport() {}

// ObserveDuration implements Metrics.
func (NopMetrics) ObserveDuration(time.Duration) {}

// Service builds compliance evidence. It is read-only and stateless.
type Service struct {
	gov      GovernanceReader
	verifier Verifier
	timeline TimelineReader
	metrics  Metrics
	now      func() time.Time
}

// NewService builds a Service.
func NewService(gov GovernanceReader, verifier Verifier, tl TimelineReader, metrics Metrics) *Service {
	if metrics == nil {
		metrics = NopMetrics{}
	}
	return &Service{gov: gov, verifier: verifier, timeline: tl, metrics: metrics, now: func() time.Time { return time.Now().UTC() }}
}

// Evidence composes the full, deterministic evidence bundle for a governance id.
func (s *Service) Evidence(ctx context.Context, govID string) (Evidence, error) {
	s.metrics.IncQuery()
	start := s.now()
	defer func() { s.metrics.ObserveDuration(s.now().Sub(start)) }()
	return s.build(ctx, govID)
}

// Summary composes the compact compliance status.
func (s *Service) Summary(ctx context.Context, govID string) (Summary, error) {
	s.metrics.IncQuery()
	start := s.now()
	defer func() { s.metrics.ObserveDuration(s.now().Sub(start)) }()
	ev, err := s.build(ctx, govID)
	if err != nil {
		return Summary{}, err
	}
	return summaryOf(ev), nil
}

// Checks composes only the compliance-check results.
func (s *Service) Checks(ctx context.Context, govID string) ([]Check, error) {
	s.metrics.IncQuery()
	start := s.now()
	defer func() { s.metrics.ObserveDuration(s.now().Sub(start)) }()
	ev, err := s.build(ctx, govID)
	if err != nil {
		return nil, err
	}
	return ev.Checks, nil
}

// Reports lists compliance summaries across governance objects.
func (s *Service) Reports(ctx context.Context, cursor string, limit int) (ReportPage, error) {
	s.metrics.IncQuery()
	start := s.now()
	defer func() { s.metrics.ObserveDuration(s.now().Sub(start)) }()

	if limit <= 0 {
		limit = 50
	}
	page, err := s.gov.List(ctx, domain.ListParams{Limit: limit, Cursor: cursor})
	if err != nil {
		return ReportPage{}, err
	}
	out := ReportPage{Items: make([]Summary, 0, len(page.Items)), NextCursor: page.NextCursor}
	for _, obj := range page.Items {
		ev, err := s.build(ctx, obj.CfgID)
		if err != nil {
			return ReportPage{}, err
		}
		out.Items = append(out.Items, summaryOf(ev))
	}
	return out, nil
}

// build is the single deterministic composition used by every method.
func (s *Service) build(ctx context.Context, govID string) (Evidence, error) {
	obj, err := s.gov.Get(ctx, govID)
	if err != nil {
		return Evidence{}, err
	}
	chainID := domain.AuditChainID(govID)
	vr, err := s.verifier.VerifyChain(ctx, chainID)
	if err != nil {
		return Evidence{}, err
	}
	page, err := s.timeline.ByGovernance(ctx, govID, timeline.Filter{Limit: 10000})
	if err != nil {
		return Evidence{}, err
	}

	gov := GovernanceSummary{
		CfgID: obj.CfgID, Lifecycle: string(obj.Lifecycle), RetentionClass: string(obj.RetentionClass),
		Authority: string(obj.Authority), RowVersion: obj.RowVersion, RatifiedBy: obj.RatifiedBy,
		CreatedAt: obj.CreatedAt, UpdatedAt: obj.UpdatedAt,
	}
	integ := IntegritySummary{
		ChainID: chainID, Verified: vr.OK, Checked: vr.Checked, HeadSeq: vr.HeadSeq,
		FirstBreakSeq: vr.FirstBreakSeq, BreakReason: vr.BreakReason,
	}

	entries := page.Entries
	ev := Evidence{
		GovernanceID: govID, GeneratedAt: s.now(), Governance: gov, Integrity: integ,
		Timeline: entries,
		Webhooks: byComponent(entries, timeline.CompWebhook),
		Replays:  byComponent(entries, timeline.CompReplay),
		Policies: byComponent(entries, timeline.CompPolicy),
	}
	ev.CorrelationIDs = correlationIDs(entries)
	ev.Checks = evaluateChecks(gov, integ, entries)
	ev.Compliant = allPassed(ev.Checks)
	return ev, nil
}

func summaryOf(ev Evidence) Summary {
	passed := 0
	for _, c := range ev.Checks {
		if c.Passed {
			passed++
		}
	}
	return Summary{
		GovernanceID: ev.GovernanceID, Lifecycle: ev.Governance.Lifecycle, Verified: ev.Integrity.Verified,
		Compliant: ev.Compliant, ChecksPassed: passed, ChecksTotal: len(ev.Checks), GeneratedAt: ev.GeneratedAt,
	}
}

func byComponent(entries []timeline.Entry, component string) []timeline.Entry {
	out := make([]timeline.Entry, 0)
	for _, e := range entries {
		if e.Component == component {
			out = append(out, e)
		}
	}
	return out
}

func correlationIDs(entries []timeline.Entry) []string {
	seen := map[string]struct{}{}
	for _, e := range entries {
		for _, v := range e.Correlation {
			if v != "" {
				seen[v] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Strings(out) // deterministic ordering
	return out
}

func allPassed(cs []Check) bool {
	for _, c := range cs {
		if !c.Passed {
			return false
		}
	}
	return true
}
