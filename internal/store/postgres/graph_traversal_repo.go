package postgres

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/rpsg/oneops/internal/domain"
)

var _ domain.GraphTraversal = (*GraphRepo)(nil)

// walkQuery builds a cycle-safe recursive-CTE traversal returning each reachable
// node once at its minimum depth, ordered deterministically by (depth, cfg_id).
// near/far are code-controlled column names selecting direction:
// forward  = near "from_cfg", far "to_cfg"  (dependencies);
// reverse  = near "to_cfg",   far "from_cfg" (dependents).
func walkQuery(near, far string) string {
	return fmt.Sprintf(`
WITH RECURSIVE walk AS (
    SELECT e.%[2]s AS cfg_id, 1 AS depth,
           ARRAY[e.%[1]s, e.%[2]s] AS path,
           (e.%[2]s = e.%[1]s) AS cyc
      FROM dependency_edge e
     WHERE e.%[1]s = $1
    UNION ALL
    SELECT e.%[2]s, w.depth + 1, w.path || e.%[2]s,
           e.%[2]s = ANY(w.path)
      FROM dependency_edge e
      JOIN walk w ON e.%[1]s = w.cfg_id
     WHERE NOT w.cyc
)
SELECT cfg_id, depth FROM (
    SELECT DISTINCT ON (cfg_id) cfg_id, depth
      FROM walk WHERE NOT cyc
     ORDER BY cfg_id, depth
) t
ORDER BY depth, cfg_id`, near, far)
}

// cycleQuery builds a recursive traversal that returns the closing path of every
// cycle reachable from the root, carrying the full path so callers can report
// the complete cycle. Ordering is deterministic.
func cycleQuery(near, far string) string {
	return fmt.Sprintf(`
WITH RECURSIVE walk AS (
    SELECT e.%[2]s AS cur,
           ARRAY[e.%[1]s, e.%[2]s] AS path,
           (e.%[2]s = e.%[1]s) AS closed
      FROM dependency_edge e
     WHERE e.%[1]s = $1
    UNION ALL
    SELECT e.%[2]s, w.path || e.%[2]s,
           e.%[2]s = ANY(w.path)
      FROM dependency_edge e
      JOIN walk w ON e.%[1]s = w.cur
     WHERE NOT w.closed
)
SELECT path FROM walk WHERE closed ORDER BY path`, near, far)
}

func directionColumns(dir domain.Direction) (near, far string) {
	if dir == domain.DirectionDependents {
		return "to_cfg", "from_cfg"
	}
	return "from_cfg", "to_cfg"
}

// Dependencies returns the direct (one-hop) dependency node ids of cfgID.
func (r *GraphRepo) Dependencies(ctx context.Context, cfgID string) ([]string, error) {
	ctx, span := graphTracer.Start(ctx, "GraphRepo.Dependencies")
	defer span.End()
	return r.queryIDs(ctx, span,
		`SELECT DISTINCT to_cfg FROM dependency_edge WHERE from_cfg = $1 ORDER BY to_cfg`, cfgID)
}

// Dependents returns the direct (one-hop) dependent node ids of cfgID.
func (r *GraphRepo) Dependents(ctx context.Context, cfgID string) ([]string, error) {
	ctx, span := graphTracer.Start(ctx, "GraphRepo.Dependents")
	defer span.End()
	return r.queryIDs(ctx, span,
		`SELECT DISTINCT from_cfg FROM dependency_edge WHERE to_cfg = $1 ORDER BY from_cfg`, cfgID)
}

// RecursiveDependencies returns the transitive forward closure with depths.
func (r *GraphRepo) RecursiveDependencies(ctx context.Context, cfgID string) ([]domain.TraversalNode, error) {
	ctx, span := graphTracer.Start(ctx, "GraphRepo.RecursiveDependencies")
	defer span.End()
	return r.queryNodes(ctx, span, walkQuery("from_cfg", "to_cfg"), cfgID)
}

// RecursiveDependents returns the transitive reverse closure with depths.
func (r *GraphRepo) RecursiveDependents(ctx context.Context, cfgID string) ([]domain.TraversalNode, error) {
	ctx, span := graphTracer.Start(ctx, "GraphRepo.RecursiveDependents")
	defer span.End()
	return r.queryNodes(ctx, span, walkQuery("to_cfg", "from_cfg"), cfgID)
}

// CyclePaths returns the closing paths of cycles reachable from cfgID.
func (r *GraphRepo) CyclePaths(ctx context.Context, cfgID string, dir domain.Direction) ([]domain.GraphPath, error) {
	ctx, span := graphTracer.Start(ctx, "GraphRepo.CyclePaths")
	defer span.End()

	near, far := directionColumns(dir)
	rows, err := r.pool.Query(ctx, cycleQuery(near, far), cfgID)
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

func (r *GraphRepo) queryIDs(ctx context.Context, span trace.Span, sql, arg string) ([]string, error) {
	rows, err := r.pool.Query(ctx, sql, arg)
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

func (r *GraphRepo) queryNodes(ctx context.Context, span trace.Span, sql, arg string) ([]domain.TraversalNode, error) {
	rows, err := r.pool.Query(ctx, sql, arg)
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
