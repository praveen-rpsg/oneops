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
