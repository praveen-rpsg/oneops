package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/alerting"
	"github.com/rpsg/oneops/internal/domain"
)

// dependencySuppressionEdgeTypes restricts the dependency walk to the two
// asset_relationship types that mean "X needs Y to function"
// (ADR-ALERTING-003 §2), the identical filter the CMDB service-map
// projection already uses (AssetGraphRepo.RecursiveDependenciesOfTypes's own
// doc comment): connected_to (a network link) and member_of (grouping) are
// excluded because a firing on X caused by a merely-connected or
// commonly-grouped peer being down is not "collateral of Y's outage" in the
// sense this feature suppresses.
var dependencySuppressionEdgeTypes = []string{
	string(domain.RelationshipDependsOn),
	string(domain.RelationshipRunsOn),
}

// DefaultDependencySuppressionMaxDepth bounds how many hops of X's
// dependency closure Suppress will consider. The recursive CTE backing the
// walk (walkQueryOnTyped) is already cycle-safe by construction — a cycle in
// depends_on/runs_on cannot make it loop forever — so this cap is a
// defensive perf bound on the WORK DONE per would-be firing (one down-check
// row scanned per candidate node), not a correctness requirement: a real
// CMDB dependency chain deeper than this is vanishingly unlikely, and a
// pathological one is bounded rather than left to grow the down-check's
// candidate set without limit.
const DefaultDependencySuppressionMaxDepth = 10

// DependencySuppressionStore administers dependency_suppression's evaluator
// surface (alerting.DependencySuppressionChecker, E3.3b, ADR-ALERTING-003).
// Unlike MaintenanceWindowStore/AlertRuleStore's dual-role split, this type
// has exactly one role — there is no operator-facing CRUD for a dependency
// suppression, only the evaluator's own privileged write path (this
// table's own migration header comment) — but it still straddles TWO
// pools, because its two questions require different privilege levels:
//
//   - "is some dependency of X currently down": a privileged, cross-tenant
//     read of alert_rule (explicit tenant_id AND asset_id predicate,
//     ADR-TENANCY-012) — pool below.
//   - "what does X transitively depend on": a TENANT-SCOPED, RLS-enforced
//     traversal of asset_relationship — graph below. Bypassing RLS for this
//     read would let a firing on one tenant's asset be "explained away" by a
//     dependency edge that in fact belongs to a different tenant merely
//     sharing the same asset_id — exactly the class ADR-TENANCY-001 exists
//     to close, and the reason this type is handed a domain.TypedGraphTraversal
//     built over the tenant-scoped pool rather than querying
//     asset_relationship itself.
//
// Every call into graph below wraps ctx with domain.WithTenant so the
// tenant-scoped pool's PrepareConn (postgres.NewTenantScopedPool's own doc
// comment) binds the acquired connection to the SAME tenant this check is
// reasoning about — never a request caller's, since there is none: this
// runs on the evaluator's own background goroutine.
type DependencySuppressionStore struct {
	pool     *pgxpool.Pool
	graph    domain.TypedGraphTraversal
	maxDepth int
}

// NewDependencySuppressionStore builds the store over the given PRIVILEGED
// pool (the down-check read and the suppression-record write) and graph
// (the dependency walk — must be built over the TENANT-SCOPED pool, see this
// type's own doc comment). maxDepth <= 0 defaults to
// DefaultDependencySuppressionMaxDepth.
func NewDependencySuppressionStore(pool *pgxpool.Pool, graph domain.TypedGraphTraversal, maxDepth int) *DependencySuppressionStore {
	if maxDepth <= 0 {
		maxDepth = DefaultDependencySuppressionMaxDepth
	}
	return &DependencySuppressionStore{pool: pool, graph: graph, maxDepth: maxDepth}
}

var _ alerting.DependencySuppressionChecker = (*DependencySuppressionStore)(nil)

// Suppress implements alerting.DependencySuppressionChecker: walks assetID's
// depends_on/runs_on closure (bounded to maxDepth), checks whether any node
// in it is currently "down" (an enabled, firing alert_rule of its own), and,
// if so, atomically records the suppression against EVERY down root found —
// naming the shallowest/lexicographically-first one as rootAssetID for the
// caller's log line (ADR-ALERTING-003 §3 "recording"). Recovery
// (firing->ok) is never checked here — the evaluator only calls this for a
// would-be ok->firing transition, mirroring
// Evaluator.maintenanceSuppressed's own gate.
func (s *DependencySuppressionStore) Suppress(ctx context.Context, tenantID, assetID string, at time.Time) (string, bool, error) {
	// The walk is RLS-scoped: bind the connection it acquires to the SAME
	// tenant this firing belongs to — never the caller's, since there is no
	// request caller here — the identical non-decision ADR-TENANCY-012
	// already requires of every other privileged-path read in this package.
	scopedCtx := domain.WithTenant(ctx, &domain.Tenant{TenantID: tenantID})
	nodes, err := s.graph.RecursiveDependenciesOfTypes(scopedCtx, assetID, dependencySuppressionEdgeTypes)
	if err != nil {
		return "", false, fmt.Errorf("walk dependency closure: %w", err)
	}
	if len(nodes) == 0 {
		return "", false, nil
	}

	// nodes is already ordered by (depth, node_id) ascending
	// (walkQueryOnTyped's own ORDER BY) — the defensive depth cap is applied
	// here as a post-filter on that same result, not as a second query.
	candidates := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if n.Depth > s.maxDepth {
			continue
		}
		candidates = append(candidates, n.CfgID)
	}
	if len(candidates) == 0 {
		return "", false, nil
	}

	// The down-check: a PLAIN SELECT — never folded into a write-CTE, unlike
	// MaintenanceWindowStore.Suppress's check-and-record — so
	// internal/arch.TestPrivilegedReads_AreScopedToATenant actually covers
	// it (that guard's own doc comment records the CTE-with-UPDATE blind
	// spot this design deliberately avoids). This connection is the
	// privileged pool (RLS off, ADR-TENANCY-002), so without the explicit
	// tenant_id predicate two tenants sharing an asset_id would both be
	// read as "down" for each other.
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT asset_id
		  FROM alert_rule
		 WHERE tenant_id = $1 AND asset_id = ANY($2) AND enabled = true AND last_state = 'firing'
		 ORDER BY asset_id`, tenantID, candidates)
	if err != nil {
		return "", false, fmt.Errorf("check dependency down-state: %w", err)
	}
	down := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return "", false, fmt.Errorf("scan down asset: %w", err)
		}
		down[id] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return "", false, fmt.Errorf("iterate down assets: %w", err)
	}
	rows.Close()
	if len(down) == 0 {
		return "", false, nil
	}

	// Record every down root found this tick — never only the first — so
	// the visible bookkeeping names every genuine cause, not merely the
	// nearest one. rootAssetID (the method's return value) is the first
	// encountered in nodes' own (depth, node_id) order: the
	// shallowest, then lexicographically-first, down dependency.
	var rootAssetID string
	for _, n := range nodes {
		if !down[n.CfgID] {
			continue
		}
		if rootAssetID == "" {
			rootAssetID = n.CfgID
		}
		if err := s.record(ctx, tenantID, assetID, n.CfgID, at); err != nil {
			return "", false, fmt.Errorf("record dependency suppression: %w", err)
		}
	}
	return rootAssetID, true, nil
}

// record atomically upserts one (tenant_id, affected_asset_id,
// root_asset_id) row's suppressed_count/last_suppressed_at — the same
// "never a silent drop" guarantee maintenance_window's own atomic
// check-and-record makes, expressed as an INSERT ... ON CONFLICT DO UPDATE
// because, unlike a maintenance window, there is no pre-existing row to
// find-and-lock the first time a pair suppresses: Postgres serialises
// concurrent upserts against the same conflict key itself, so two rules on
// the same asset firing in the same tick, from different goroutines, still
// never lose an increment.
func (s *DependencySuppressionStore) record(ctx context.Context, tenantID, affectedAssetID, rootAssetID string, at time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO dependency_suppression
			(suppression_id, tenant_id, affected_asset_id, root_asset_id, suppressed_count, last_suppressed_at, row_version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 1, $5, 1, now(), now())
		ON CONFLICT (tenant_id, affected_asset_id, root_asset_id)
		DO UPDATE SET suppressed_count   = dependency_suppression.suppressed_count + 1,
		              last_suppressed_at = EXCLUDED.last_suppressed_at,
		              row_version        = dependency_suppression.row_version + 1,
		              updated_at         = now()`,
		domain.NewID(), tenantID, affectedAssetID, rootAssetID, at)
	if err != nil {
		return fmt.Errorf("upsert dependency suppression: %w", err)
	}
	return nil
}
