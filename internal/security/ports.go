// Package security is the leader-gated SIEM detector (E8.1b-2): it derives a
// firing by evaluating each enabled domain.SecurityRule against
// security_observation and, on a state TRANSITION only (ok->firing or
// firing->ok), creates or links a domain.Incident (source=security). It
// mirrors internal/alerting's evaluator shape exactly — Config/Store/Run/
// RunOnce, keyset paging, bounded concurrency, transition-only discipline —
// applied to security_observation counts instead of telemetry threshold
// breaches (docs/PLATFORM-BUILD-PLAN.md E8.1b-2, ADR-SOC-001, ADR-SOC-002).
//
// There is deliberately no reified Alert/Detection/Correlation type anywhere
// in this package (docs/PLATFORM-BUILD-PLAN.md §4): a firing is a decision
// this package makes each tick, not a row it stores. What IS stored is the
// rule (config, E8.1b-1) and the Incident a transition creates or links to
// (E5.1).
package security

import (
	"context"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// Store is the persistence port the SecurityDetector depends on.
// *postgres.SecurityRuleStore satisfies it when built over the PRIVILEGED
// pool, exactly as AlertRuleStore satisfies alerting.Store over that same
// pool: the detector serves every tenant's enabled rules from one process,
// while the admin CRUD API (domain.SecurityRuleRepository) is built from the
// tenant-scoped pool instead.
type Store interface {
	// EnabledRules returns up to limit enabled rules, across every tenant,
	// keyset-paginated over rule_id — mirrors alerting.Store.EnabledRules.
	EnabledRules(ctx context.Context, limit int, after string) ([]*domain.SecurityRule, error)
	// RecordTransition CAS-updates last_state/last_transition_at/
	// current_incident_id, fenced on rowVersion — mirrors
	// alerting.Store.RecordTransition exactly, including currentIncidentID
	// travelling in the SAME statement as the state transition (nil clears
	// the link, a non-nil value sets it). Returns domain.ErrVersionMismatch
	// on a stale rowVersion, domain.ErrNotFound if the rule no longer exists
	// (deleted mid-tick).
	RecordTransition(ctx context.Context, ruleID string, rowVersion int64, state domain.SecurityRuleState, at time.Time, currentIncidentID *string) (*domain.SecurityRule, error)
}

// ObservationCounter is the narrow, tenant-explicit security_observation read
// the detector needs — mirrors alerting.TelemetryReader's own narrowing of
// domain.TelemetryRepository to QueryRangeForTenant alone, for the identical
// reason: domain.SecurityObservationRepository.QueryRange's isolation is
// RLS-only and unsafe to call from this package's privileged connection.
type ObservationCounter interface {
	CountForTenant(ctx context.Context, tenantID, assetID, observationType string, minSeverity domain.ObservationSeverity, from, to time.Time) (int, error)
}

// IncidentCorrelator is this package's create-or-link port: wires a security
// rule's ok->firing/firing->ok transitions into Incident (E5.1) creation/
// linking — the exact SECURITY analog of alerting.IncidentCorrelator.
// *postgres.IncidentStore satisfies it when built over the PRIVILEGED pool —
// the same dual-role split alerting.IncidentCorrelator already documents —
// while the admin CRUD API (domain.IncidentRepository) is built from the
// tenant-scoped pool.
//
// Every method is given tenantID explicitly (either directly, or via
// want.TenantID) and carries it as a SQL predicate in its implementation:
// this pool has row-level security switched off, so nothing else confines a
// correlation read/write to one tenant (ADR-TENANCY-012). The detector
// always sources tenantID from the firing rule's own row, never a value
// guessed or cached across rules — the same non-decision
// alerting.IncidentCorrelator's own doc comment requires.
//
// The interface is deliberately narrow: it has no method that can change an
// Incident's Status — this package correlates, it does not remediate,
// exactly like E4.1's own scope.
type IncidentCorrelator interface {
	// FindOrCreateOpenSecurityIncident returns the id of want.TenantID's OPEN
	// (status not resolved or closed), security-sourced incident already
	// linked to want.AssetID — appending one incident_event row of kind
	// security_note carrying noteOnLink to it — or inserts want itself
	// (Source is asserted to already be domain.IncidentSourceSecurity;
	// Validate is re-run) when none exists, recording the usual
	// IncidentEventCreated row instead. actor names the writer for either
	// timeline row (the detector's own system identity, never a request's
	// ActorFrom(ctx)).
	//
	// Atomicity is a database-level constraint, not an application lock —
	// see postgres.IncidentStore.FindOrCreateOpenSecurityIncident's doc
	// comment for how two rules firing on the same (tenant, asset)
	// concurrently, from different detector goroutines, can never both
	// create a row. This incident is SEPARATE from any open alert-sourced
	// incident on the same asset — Source distinguishes the two correlation
	// paths, so an alert firing and a security firing on the same CI always
	// produce two incidents, never one merged row.
	FindOrCreateOpenSecurityIncident(ctx context.Context, want *domain.Incident, actor, noteOnLink string) (incidentID string, err error)
	// AppendSecurityNote appends one incident_event row of kind security_note
	// to incidentID, first re-verifying — under an explicit tenant_id
	// predicate — that incidentID names a row owned by tenantID. Returns
	// domain.ErrNotFound otherwise — mirrors
	// alerting.IncidentCorrelator.AppendAlertNote exactly.
	AppendSecurityNote(ctx context.Context, tenantID, incidentID, note, actor string) error
}

// Metrics receives detector observability signals. NopMetrics discards them.
type Metrics interface {
	IncEvaluated()
	IncFired()
	IncRecovered()
	IncErrors()
}

// NopMetrics is the no-op Metrics.
type NopMetrics struct{}

func (NopMetrics) IncEvaluated() {}
func (NopMetrics) IncFired()     {}
func (NopMetrics) IncRecovered() {}
func (NopMetrics) IncErrors()    {}

// ---------------------------------------------------------------------------
// E8.2b: the IOCMatcher's own ports (ioc_matcher.go, ADR-SOC-005). A SEPARATE
// pair of interfaces from Store/ObservationCounter above, mirroring the
// SAME split-privileged-surface reasoning: the IOCMatcher reads a different
// config table (ioc, not security_rule) over a different SQL shape (a
// bounded row read across every asset/type in a tenant's window, not a
// per-asset/type COUNT), so it gets its own minimal ports rather than
// widening Store/ObservationCounter to carry both rules and iocs.

// IOCLister is the leader-gated IOCMatcher's own privileged read over ioc
// (E8.2b). *postgres.IOCMatcherStore satisfies it when built over the
// PRIVILEGED pool — DELIBERATELY SEPARATE from IOCStore (the admin CRUD API,
// domain.IOCRepository, tenant-scoped), the identical dual-role split
// SecurityRuleDetectorStore/SecurityRuleStore already draws, and for the
// identical reason: keeping this port's (and its implementation's) method
// set to exactly these two methods means IOCStore.Create/Update/Delete are
// never reachable from a privileged instance.
type IOCLister interface {
	// TenantsWithEnabledIOCs returns every tenant_id that currently holds at
	// least one enabled ioc — mirrors grouping.Store.TenantsWithOpenAlertIncidents:
	// tenant-agnostic, discloses only which tenants have work, never any
	// tenant's row data. This is what bounds RunOnce to tenants with
	// something to match against, instead of sweeping every tenant that ever
	// registered (the same "only tenants with work" discipline
	// TenantsWithOpenAlertIncidents applies to grouping).
	TenantsWithEnabledIOCs(ctx context.Context) ([]string, error)
	// EnabledIOCsForTenant returns up to limit of tenantID's enabled iocs,
	// keyset-paginated over ioc_id — mirrors Store.EnabledRules narrowed to
	// one tenant via an EXPLICIT tenant_id predicate (ADR-TENANCY-012): this
	// connection has row-level security switched off, so nothing else
	// confines the read to tenantID.
	EnabledIOCsForTenant(ctx context.Context, tenantID string, limit int, after string) ([]*domain.IOC, error)
}

// ObservationWindowReader is the IOCMatcher's own privileged read over
// security_observation — narrower in SCOPE than ObservationCounter (which
// fixes one asset_id/observation_type and only counts) but WIDER in SHAPE (it
// returns full rows, Attributes included, across every asset/type a tenant
// reported): an IOC match is checked against everything the tenant observed
// in the trailing window, not one rule's own asset/type pair.
// *postgres.SecurityObservationCounterStore satisfies both this and
// ObservationCounter — one privileged type, two narrow ports, mirroring how
// *postgres.IncidentStore satisfies both alerting.IncidentCorrelator and
// IncidentCorrelator above.
type ObservationWindowReader interface {
	// RecentForTenant returns up to limit of tenantID's security_observation
	// rows with observed_at in (from, to], ordered by the table's own natural
	// key (observed_at, asset_id, observation_type, source) ascending —
	// EXCLUSIVE of from, so consecutive tiled windows ([a,b] then [b,c]) never
	// double-return the row observed exactly at the shared boundary. after,
	// when non-nil, is the previous page's own LAST row: security_observation
	// has no surrogate id (its own migration's doc comment), so the natural
	// key compared as a whole (a Postgres ROW value) is the only usable
	// keyset cursor once asset_id/observation_type are no longer fixed to one
	// pair — contrast domain.SecurityObservationRepository.QueryRange, which
	// fixes both and so can key on observed_at alone.
	RecentForTenant(ctx context.Context, tenantID string, from, to time.Time, limit int, after *domain.SecurityObservation) ([]domain.SecurityObservation, error)
}

// IOCMatcherMetrics receives IOCMatcher observability signals — a SEPARATE
// interface from Metrics above: "rules evaluated/fired/recovered" and
// "observations checked against the watchlist/matched" are different
// signals a dashboard needs to tell apart (different underlying detection
// pipelines, per securityDetectorSystemActor's own doc comment reasoning).
type IOCMatcherMetrics interface {
	IncObservationsChecked()
	IncMatched()
	IncTenantsProcessed()
	IncErrors()
}

// NopIOCMatcherMetrics is the no-op IOCMatcherMetrics.
type NopIOCMatcherMetrics struct{}

func (NopIOCMatcherMetrics) IncObservationsChecked() {}
func (NopIOCMatcherMetrics) IncMatched()             {}
func (NopIOCMatcherMetrics) IncTenantsProcessed()    {}
func (NopIOCMatcherMetrics) IncErrors()              {}
