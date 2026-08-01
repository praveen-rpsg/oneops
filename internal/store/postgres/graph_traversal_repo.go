package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/rpsg/oneops/internal/domain"
)

var _ domain.GraphTraversal = (*GraphRepo)(nil)

// walkQueryOn builds a cycle-safe recursive-CTE traversal over the given edge
// table, returning each reachable node once at its minimum depth, ordered
// deterministically by (depth, node_id). table, near and far are
// code-controlled identifiers — never request input — selecting the edge
// table and its direction:
// forward = near "from_*", far "to_*"  (dependencies);
// reverse = near "to_*",   far "from_*" (dependents).
//
// This is the one traversal engine the platform has. AssetGraphRepo below
// reuses it unchanged, over asset_relationship's own column names, rather
// than a second implementation of the same recursive CTE (ADR-ASSET-001 §4).
func walkQueryOn(table, near, far string) string {
	return fmt.Sprintf(`
WITH RECURSIVE walk AS (
    SELECT e.%[2]s AS node_id, 1 AS depth,
           ARRAY[e.%[1]s, e.%[2]s] AS path,
           (e.%[2]s = e.%[1]s) AS cyc
      FROM %[3]s e
     WHERE e.%[1]s = $1
    UNION ALL
    SELECT e.%[2]s, w.depth + 1, w.path || e.%[2]s,
           e.%[2]s = ANY(w.path)
      FROM %[3]s e
      JOIN walk w ON e.%[1]s = w.node_id
     WHERE NOT w.cyc
)
SELECT node_id, depth FROM (
    SELECT DISTINCT ON (node_id) node_id, depth
      FROM walk WHERE NOT cyc
     ORDER BY node_id, depth
) t
ORDER BY depth, node_id`, near, far, table)
}

// cycleQueryOn builds a recursive traversal over the given edge table that
// returns the closing path of every cycle reachable from the root, carrying
// the full path so callers can report the complete cycle. Ordering is
// deterministic.
func cycleQueryOn(table, near, far string) string {
	return fmt.Sprintf(`
WITH RECURSIVE walk AS (
    SELECT e.%[2]s AS cur,
           ARRAY[e.%[1]s, e.%[2]s] AS path,
           (e.%[2]s = e.%[1]s) AS closed
      FROM %[3]s e
     WHERE e.%[1]s = $1
    UNION ALL
    SELECT e.%[2]s, w.path || e.%[2]s,
           e.%[2]s = ANY(w.path)
      FROM %[3]s e
      JOIN walk w ON e.%[1]s = w.cur
     WHERE NOT w.closed
)
SELECT path FROM walk WHERE closed ORDER BY path`, near, far, table)
}

// walkQuery/cycleQuery are the dependency_edge instantiations of the generic
// queries above, kept as their own names because graph_perf_test.go's EXPLAIN
// harness calls them directly.
func walkQuery(near, far string) string  { return walkQueryOn("dependency_edge", near, far) }
func cycleQuery(near, far string) string { return cycleQueryOn("dependency_edge", near, far) }

func directionColumns(dir domain.Direction) (near, far string) {
	return columnsFor(dir, "from_cfg", "to_cfg")
}

// columnsFor resolves a traversal direction against a table's own forward
// column names: forward walks near=fromCol,far=toCol; reverse swaps them.
func columnsFor(dir domain.Direction, fromCol, toCol string) (near, far string) {
	if dir == domain.DirectionDependents {
		return toCol, fromCol
	}
	return fromCol, toCol
}

// Dependencies returns the direct (one-hop) dependency node ids of cfgID.
func (r *GraphRepo) Dependencies(ctx context.Context, cfgID string) ([]string, error) {
	ctx, span := graphTracer.Start(ctx, "GraphRepo.Dependencies")
	defer span.End()
	return queryGraphIDs(ctx, r.pool, span,
		`SELECT DISTINCT to_cfg FROM dependency_edge WHERE from_cfg = $1 ORDER BY to_cfg`, cfgID)
}

// Dependents returns the direct (one-hop) dependent node ids of cfgID.
func (r *GraphRepo) Dependents(ctx context.Context, cfgID string) ([]string, error) {
	ctx, span := graphTracer.Start(ctx, "GraphRepo.Dependents")
	defer span.End()
	return queryGraphIDs(ctx, r.pool, span,
		`SELECT DISTINCT from_cfg FROM dependency_edge WHERE to_cfg = $1 ORDER BY from_cfg`, cfgID)
}

// RecursiveDependencies returns the transitive forward closure with depths.
func (r *GraphRepo) RecursiveDependencies(ctx context.Context, cfgID string) ([]domain.TraversalNode, error) {
	ctx, span := graphTracer.Start(ctx, "GraphRepo.RecursiveDependencies")
	defer span.End()
	return queryGraphNodes(ctx, r.pool, span, walkQuery("from_cfg", "to_cfg"), cfgID)
}

// RecursiveDependents returns the transitive reverse closure with depths.
func (r *GraphRepo) RecursiveDependents(ctx context.Context, cfgID string) ([]domain.TraversalNode, error) {
	ctx, span := graphTracer.Start(ctx, "GraphRepo.RecursiveDependents")
	defer span.End()
	return queryGraphNodes(ctx, r.pool, span, walkQuery("to_cfg", "from_cfg"), cfgID)
}

// CyclePaths returns the closing paths of cycles reachable from cfgID.
func (r *GraphRepo) CyclePaths(ctx context.Context, cfgID string, dir domain.Direction) ([]domain.GraphPath, error) {
	ctx, span := graphTracer.Start(ctx, "GraphRepo.CyclePaths")
	defer span.End()

	near, far := directionColumns(dir)
	return queryGraphPaths(ctx, r.pool, span, cycleQuery(near, far), cfgID)
}

// queryGraphIDs, queryGraphNodes and queryGraphPaths are the shared read
// helpers behind every domain.GraphTraversal implementation in this package —
// GraphRepo (dependency_edge) and AssetGraphRepo (asset_relationship) both
// call them, rather than each hand-rolling its own row scanning.
func queryGraphIDs(ctx context.Context, pool *pgxpool.Pool, span trace.Span, sql, arg string) ([]string, error) {
	rows, err := pool.Query(ctx, sql, arg)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "query ids")
		return nil, fmt.Errorf("query ids: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

func queryGraphNodes(ctx context.Context, pool *pgxpool.Pool, span trace.Span, sql, arg string) ([]domain.TraversalNode, error) {
	rows, err := pool.Query(ctx, sql, arg)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "query nodes")
		return nil, fmt.Errorf("query nodes: %w", err)
	}
	defer rows.Close()

	var nodes []domain.TraversalNode
	for rows.Next() {
		var n domain.TraversalNode
		if err := rows.Scan(&n.CfgID, &n.Depth); err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return nodes, nil
}

func queryGraphPaths(ctx context.Context, pool *pgxpool.Pool, span trace.Span, sql, arg string) ([]domain.GraphPath, error) {
	rows, err := pool.Query(ctx, sql, arg)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "cycle paths")
		return nil, fmt.Errorf("cycle paths: %w", err)
	}
	defer rows.Close()

	var paths []domain.GraphPath
	for rows.Next() {
		var nodes []string
		if err := rows.Scan(&nodes); err != nil {
			return nil, err
		}
		paths = append(paths, domain.GraphPath{Nodes: nodes})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return paths, nil
}
