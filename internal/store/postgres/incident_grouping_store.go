package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/grouping"
)

// openAlertIncidentStatuses mirrors FindOrCreateOpenAlertIncident's own
// definition of "open" for an alert-sourced incident exactly (E4.1): not
// resolved, not closed. Grouping must use the identical definition — a
// "resolved" root is no longer a down signal, and its former children must
// stop pointing at it on the very next reconciliation pass (E4.2's
// self-healing requirement).
const openAlertIncidentPredicate = `source = 'alert' AND status NOT IN ('resolved', 'closed')`

// IncidentGroupingStore administers E4.2's reconciliation surface
// (grouping.Store, ADR-ALERTING-004): the privileged, cross-tenant reads and
// writes internal/grouping's leader-gated Reconciler needs. Built over the
// PRIVILEGED pool — the same dual-role split IncidentStore's own E4.1
// correlation surface uses (that type's own doc comment): the tenant-scoped
// admin CRUD API is domain.IncidentRepository, built from a SEPARATE
// *IncidentStore instance over the tenant-scoped pool.
//
// Every statement here carries an explicit tenant_id predicate sourced from
// the caller's own argument, never assumed from RLS (ADR-TENANCY-012): this
// connection has row-level security switched off.
type IncidentGroupingStore struct {
	pool *pgxpool.Pool
}

// NewIncidentGroupingStore builds the store over the given PRIVILEGED pool.
func NewIncidentGroupingStore(pool *pgxpool.Pool) *IncidentGroupingStore {
	return &IncidentGroupingStore{pool: pool}
}

var _ grouping.Store = (*IncidentGroupingStore)(nil)

// TenantsWithOpenAlertIncidents implements grouping.Store. Deliberately
// tenant-agnostic (see grouping.Store's own doc comment): it discloses only
// which tenants have work, never any tenant's row data.
func (s *IncidentGroupingStore) TenantsWithOpenAlertIncidents(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT tenant_id
		  FROM incident
		 WHERE `+openAlertIncidentPredicate+`
		 ORDER BY tenant_id`)
	if err != nil {
		return nil, fmt.Errorf("list tenants with open alert incidents: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var tenantID string
		if err := rows.Scan(&tenantID); err != nil {
			return nil, fmt.Errorf("scan tenant_id: %w", err)
		}
		out = append(out, tenantID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tenants: %w", err)
	}
	return out, nil
}

// OpenAlertIncidents implements grouping.Store. tenant_id is an explicit
// predicate on this privileged connection (ADR-TENANCY-012), REQUIRED because
// RLS is off on this pool. NOTE on coverage (do not overstate it): the static
// guard internal/arch.TestPrivilegedReads_AreScopedToATenant does NOT cover
// this read — its detector matches an `asset_id = $/ANY` disclosure shape, but
// this read filters `asset_id IS NOT NULL`, so it is outside the guard's
// documented asset_id-disclosure recall bound (privileged_read_test.go:59-64)
// and the guard stays green even if this predicate were removed. Tenant
// isolation here is enforced SOLELY by the live, mutation-proven integration
// test TestIncidentGroupingStore_OpenAlertIncidentsIsTenantIsolated — keep it.
func (s *IncidentGroupingStore) OpenAlertIncidents(ctx context.Context, tenantID string) ([]grouping.OpenAlertIncident, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT incident_id, asset_id, root_incident_id
		  FROM incident
		 WHERE tenant_id = $1 AND `+openAlertIncidentPredicate+` AND asset_id IS NOT NULL
		 ORDER BY incident_id`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list open alert incidents: %w", err)
	}
	defer rows.Close()

	var out []grouping.OpenAlertIncident
	for rows.Next() {
		var inc grouping.OpenAlertIncident
		var assetID *string
		if err := rows.Scan(&inc.IncidentID, &assetID, &inc.RootIncidentID); err != nil {
			return nil, fmt.Errorf("scan open alert incident: %w", err)
		}
		if assetID != nil {
			inc.AssetID = *assetID
		}
		out = append(out, inc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate open alert incidents: %w", err)
	}
	return out, nil
}

// SetRootIncidentID implements grouping.Store: the ONLY write path for
// incident.root_incident_id (domain.Incident.RootIncidentID's own doc
// comment) — no HTTP route touches this column, and it is never folded into
// IncidentStore.Update's row_version-guarded patch. tenantID is an explicit
// predicate the UPDATE itself carries, confining the write to the caller's
// own tenant even though this pool has row-level security switched off.
//
// row_version is bumped in the same statement, the same "any write to this
// row invalidates a stale optimistic-lock read" discipline
// IncidentStore.Update/SetStatus/Assign already apply to every other column
// change on this table — a grouping decision is a real, visible change to
// the row an operator's next PATCH must not silently overwrite the effect of.
func (s *IncidentGroupingStore) SetRootIncidentID(ctx context.Context, tenantID, incidentID string, rootIncidentID *string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE incident
		   SET root_incident_id = $3, row_version = row_version + 1, updated_at = now()
		 WHERE incident_id = $1 AND tenant_id = $2`,
		incidentID, tenantID, rootIncidentID)
	if err != nil {
		if isForeignKeyViolation(err) {
			// rootIncidentID no longer names an existing incident (a
			// vanishingly unlikely race, since Incident has no Delete
			// method — see incident.go's own doc comment — but the FK's
			// ON DELETE SET NULL means this can only ever be a stale read
			// racing an equally-stale root's own concurrent update, not a
			// real dangling reference). The next reconciliation pass
			// recomputes from fresh state.
			return fmt.Errorf("set root_incident_id: candidate root no longer exists: %w", err)
		}
		return fmt.Errorf("set root_incident_id: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("set root_incident_id: no incident %q in tenant %q", incidentID, tenantID)
	}
	return nil
}
