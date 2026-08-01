package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
)

// AssetGraphRepo is the CMDB's domain.GraphTraversal adapter: it answers the
// same traversal questions GraphRepo answers over dependency_edge, but over
// asset_relationship. It reuses the recursive-CTE query builders in
// graph_traversal_repo.go unchanged — walkQueryOn/cycleQueryOn are already
// parameterised on table and column names for exactly this reason — so the
// CMDB gets the configuration-object dependency graph's traversal engine
// without a second implementation of it (ADR-ASSET-001 §4).
type AssetGraphRepo struct {
	pool *pgxpool.Pool
}

// NewAssetGraphRepo builds a repository over the given (tenant-scoped) pool.
func NewAssetGraphRepo(pool *pgxpool.Pool) *AssetGraphRepo {
	return &AssetGraphRepo{pool: pool}
}

var (
	_ domain.GraphTraversal      = (*AssetGraphRepo)(nil)
	_ domain.TypedGraphTraversal = (*AssetGraphRepo)(nil)
)

const assetRelationshipTable = "asset_relationship"

// Dependencies returns the direct (one-hop) out-neighbours of assetID.
func (r *AssetGraphRepo) Dependencies(ctx context.Context, assetID string) ([]string, error) {
	ctx, span := graphTracer.Start(ctx, "AssetGraphRepo.Dependencies")
	defer span.End()
	return queryGraphIDs(ctx, r.pool, span,
		`SELECT DISTINCT to_asset_id FROM asset_relationship WHERE from_asset_id = $1 ORDER BY to_asset_id`, assetID)
}

// Dependents returns the direct (one-hop) in-neighbours of assetID.
func (r *AssetGraphRepo) Dependents(ctx context.Context, assetID string) ([]string, error) {
	ctx, span := graphTracer.Start(ctx, "AssetGraphRepo.Dependents")
	defer span.End()
	return queryGraphIDs(ctx, r.pool, span,
		`SELECT DISTINCT from_asset_id FROM asset_relationship WHERE to_asset_id = $1 ORDER BY from_asset_id`, assetID)
}

// RecursiveDependencies returns the transitive forward closure with depths.
func (r *AssetGraphRepo) RecursiveDependencies(ctx context.Context, assetID string) ([]domain.TraversalNode, error) {
	ctx, span := graphTracer.Start(ctx, "AssetGraphRepo.RecursiveDependencies")
	defer span.End()
	near, far := columnsFor(domain.DirectionDependencies, "from_asset_id", "to_asset_id")
	return queryGraphNodes(ctx, r.pool, span, walkQueryOn(assetRelationshipTable, near, far), assetID)
}

// RecursiveDependents returns the transitive reverse closure with depths.
func (r *AssetGraphRepo) RecursiveDependents(ctx context.Context, assetID string) ([]domain.TraversalNode, error) {
	ctx, span := graphTracer.Start(ctx, "AssetGraphRepo.RecursiveDependents")
	defer span.End()
	near, far := columnsFor(domain.DirectionDependents, "from_asset_id", "to_asset_id")
	return queryGraphNodes(ctx, r.pool, span, walkQueryOn(assetRelationshipTable, near, far), assetID)
}

// RecursiveDependenciesOfTypes returns the transitive forward closure
// reachable using only edges whose type is in types. The CMDB service-map
// endpoint calls this with {depends_on, runs_on} to compose a
// business_service from its supporting CIs, excluding connected_to (a
// network link, not composition) and member_of (grouping, not composition) —
// domain.TypedGraphTraversal (E1.2).
func (r *AssetGraphRepo) RecursiveDependenciesOfTypes(ctx context.Context, assetID string, types []string) ([]domain.TraversalNode, error) {
	ctx, span := graphTracer.Start(ctx, "AssetGraphRepo.RecursiveDependenciesOfTypes")
	defer span.End()
	near, far := columnsFor(domain.DirectionDependencies, "from_asset_id", "to_asset_id")
	return queryGraphNodesTyped(ctx, r.pool, span, walkQueryOnTyped(assetRelationshipTable, near, far), assetID, types)
}

// CyclePaths returns the closing paths of cycles reachable from assetID.
func (r *AssetGraphRepo) CyclePaths(ctx context.Context, assetID string, dir domain.Direction) ([]domain.GraphPath, error) {
	ctx, span := graphTracer.Start(ctx, "AssetGraphRepo.CyclePaths")
	defer span.End()
	near, far := columnsFor(dir, "from_asset_id", "to_asset_id")
	return queryGraphPaths(ctx, r.pool, span, cycleQueryOn(assetRelationshipTable, near, far), assetID)
}
