// Package grouping is E4.2's leader-gated, topology-aware incident-grouping
// reconciler (ADR-ALERTING-004). It is complementary to, and separate from,
// internal/alerting's E3.3b dependency-aware suppression: E3.3b PREVENTS a
// new collateral page while a root is already firing at transition time;
// this package GROUPS the incidents that get created anyway (a root detected
// after its collateral, a root's rule firing later, or a suppression that did
// not apply) so an operator sees one root incident with N grouped collateral
// rather than N+1 equal pages.
//
// There is deliberately no reified Group/Correlation/Alert/Event noun here
// (docs/PLATFORM-BUILD-PLAN.md §4, Vol II §5.3): the whole mechanism is one
// nullable self-FK column, incident.root_incident_id — the same reduced-
// concept treatment alert_rule.current_incident_id (E4.1) already gets. A
// "group" is DERIVED (every incident sharing a root_incident_id, plus that
// root), never a stored membership row.
//
// This is a SEPARATE leader-gated background pass, not a hook inside
// alerting.Evaluator.correlate: grouping must reconsider the CURRENT full set
// of open alert-incidents per tenant on every tick — a root can appear or
// resolve independently of any one rule's own transition — so it is a
// periodic reconciliation sweep, mirroring alerting.Evaluator's own shape
// (Config/Store port/Run/RunOnce), not an inline side effect of one firing.
package grouping

import (
	"context"

	"github.com/rpsg/oneops/internal/domain"
)

// OpenAlertIncident is the narrow projection of an open, alert-sourced
// incident the Reconciler needs: enough to place it in the dependency graph
// (AssetID) and to know its current grouping state
// (IncidentID/RootIncidentID) without pulling the full domain.Incident shape
// across a privileged, cross-tenant read.
type OpenAlertIncident struct {
	IncidentID     string
	AssetID        string
	RootIncidentID *string
	// SymptomClass is E4.3's (ADR-ALERTING-007) class gate input: a
	// resource-class incident is never rooted under a down dependency,
	// however deep — see Reconciler.reconcileTenant's own doc comment. It
	// does NOT change what "down" means (the down map below is built from
	// every OpenAlertIncident regardless of class): a resource-class incident
	// on Y still marks Y down for whichever OTHER incident's closure Y
	// appears in.
	SymptomClass domain.AlertSymptomClass
}

// Store is the privileged, cross-tenant persistence port the Reconciler
// depends on. *postgres.IncidentGroupingStore satisfies it, built over the
// PRIVILEGED pool — the same dual-role split every other E3/E4 background
// worker in this platform uses (alerting.Store, alerting.IncidentCorrelator):
// the admin CRUD API (domain.IncidentRepository) is built from the
// tenant-scoped pool instead.
//
// Every method carries tenantID explicitly and as an EXPLICIT SQL predicate
// in its implementation: this pool has row-level security switched off, so
// nothing else confines a read or write to one tenant (ADR-TENANCY-012).
type Store interface {
	// TenantsWithOpenAlertIncidents returns every tenant_id that currently
	// has at least one open (not resolved/closed), alert-sourced incident —
	// the Reconciler's per-tenant unit of work for one sweep. Deliberately
	// tenant-AGNOSTIC by design (it exists to discover the tenant set, not to
	// disclose one tenant's row data to another): it returns only tenant_id
	// values, never incident rows.
	TenantsWithOpenAlertIncidents(ctx context.Context) ([]string, error)
	// OpenAlertIncidents returns tenantID's open, alert-sourced incidents
	// that carry a non-empty asset_id, ordered by incident_id for
	// deterministic, reproducible reconciliation output.
	OpenAlertIncidents(ctx context.Context, tenantID string) ([]OpenAlertIncident, error)
	// SetRootIncidentID overwrites incidentID's root_incident_id (nil
	// clears it) — the ONLY write path for this column (domain.Incident.
	// RootIncidentID's own doc comment): no HTTP route touches it, and it is
	// never folded into the row_version-guarded admin Update path. tenantID
	// is an explicit predicate the statement itself carries (ADR-TENANCY-012).
	SetRootIncidentID(ctx context.Context, tenantID, incidentID string, rootIncidentID *string) error
}

// Graph is the narrow traversal capability the Reconciler needs: assetID's
// transitive depends_on/runs_on closure with depths — domain.
// TypedGraphTraversal's own method, restated here as its own interface so
// this package depends on exactly the one method it calls, not the whole
// traversal contract. The caller MUST build this over the TENANT-SCOPED
// pool, never the privileged one — see Reconciler's own doc comment for why
// bypassing RLS on asset_relationship would let one tenant's grouping decision
// be explained away by another tenant's edge (ADR-TENANCY-001), the identical
// reasoning postgres.DependencySuppressionStore's own doc comment already
// states for E3.3b's down-check.
type Graph interface {
	RecursiveDependenciesOfTypes(ctx context.Context, id string, types []string) ([]domain.TraversalNode, error)
}

// Metrics receives Reconciler observability signals. NopMetrics discards
// them.
type Metrics interface {
	// IncTenantsProcessed counts one tenant's incidents having been
	// reconciled in one RunOnce pass (whether or not anything changed).
	IncTenantsProcessed()
	// IncIncidentsChanged counts one incident whose root_incident_id was
	// written (set, cleared, or re-pointed) this pass — the noise-reduction
	// payoff signal: a non-zero rate means grouping is actively collapsing
	// storm noise, a steady zero on a stable topology is the expected,
	// converged (idempotent) state.
	IncIncidentsChanged()
	IncErrors()
}

// NopMetrics is the no-op Metrics.
type NopMetrics struct{}

func (NopMetrics) IncTenantsProcessed() {}
func (NopMetrics) IncIncidentsChanged() {}
func (NopMetrics) IncErrors()           {}
