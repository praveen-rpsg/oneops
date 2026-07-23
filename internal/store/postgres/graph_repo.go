package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/rpsg/oneops/internal/domain"
)

const edgeColumns = `id, from_cfg, to_cfg, edge_kind, created_at, updated_at`

const defaultEdgeListLimit = 100

var graphTracer = otel.Tracer("github.com/rpsg/oneops/internal/store/postgres")

// GraphRepo is the PostgreSQL implementation of domain.GraphRepository. It is a
// pure storage layer: no traversal, recursive SQL, or authority logic.
type GraphRepo struct {
	pool *pgxpool.Pool
}

// NewGraphRepo builds a repository over the given pool.
func NewGraphRepo(pool *pgxpool.Pool) *GraphRepo {
	return &GraphRepo{pool: pool}
}

var _ domain.GraphRepository = (*GraphRepo)(nil)

func scanEdge(s scanner) (*domain.DependencyEdge, error) {
	var (
		e    domain.DependencyEdge
		kind string
	)
	if err := s.Scan(&e.ID, &e.FromCfg, &e.ToCfg, &kind, &e.CreatedAt, &e.UpdatedAt); err != nil {
		return nil, err
	}
	e.EdgeKind = domain.EdgeKind(kind)
	return &e, nil
}

// CreateEdge inserts an edge, mapping storage errors to domain errors.
func (r *GraphRepo) CreateEdge(ctx context.Context, edge *domain.DependencyEdge) (*domain.DependencyEdge, error) {
	ctx, span := graphTracer.Start(ctx, "GraphRepo.CreateEdge",
		trace.WithAttributes(attribute.String("edge.kind", string(edge.EdgeKind))))
	defer span.End()

	if err := edge.Validate(); err != nil {
		return nil, err
	}
	if edge.ID == "" {
		edge.ID = domain.NewID()
	}
	err := r.pool.QueryRow(ctx, `
		INSERT INTO dependency_edge (id, from_cfg, to_cfg, edge_kind)
		VALUES ($1,$2,$3,$4)
		RETURNING created_at, updated_at`,
		edge.ID, edge.FromCfg, edge.ToCfg, string(edge.EdgeKind),
	).Scan(&edge.CreatedAt, &edge.UpdatedAt)
	if err != nil {
		switch {
		case isUniqueViolation(err):
			return nil, domain.ErrConflict
		case isForeignKeyViolation(err):
			return nil, domain.ErrNotFound
		default:
			span.RecordError(err)
			span.SetStatus(codes.Error, "create edge")
			return nil, fmt.Errorf("insert edge: %w", err)
		}
	}
	return edge, nil
}

// DeleteEdge removes an edge by id, or returns domain.ErrNotFound.
func (r *GraphRepo) DeleteEdge(ctx context.Context, id string) error {
	ctx, span := graphTracer.Start(ctx, "GraphRepo.DeleteEdge")
	defer span.End()

	tag, err := r.pool.Exec(ctx, `DELETE FROM dependency_edge WHERE id = $1`, id)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "delete edge")
		return fmt.Errorf("delete edge: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// Edge returns an edge by id, or domain.ErrNotFound.
func (r *GraphRepo) Edge(ctx context.Context, id string) (*domain.DependencyEdge, error) {
	ctx, span := graphTracer.Start(ctx, "GraphRepo.Edge")
	defer span.End()

	edge, err := scanEdge(r.pool.QueryRow(ctx,
		`SELECT `+edgeColumns+` FROM dependency_edge WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		span.RecordError(err)
		span.SetStatus(codes.Error, "get edge")
		return nil, err
	}
	return edge, nil
}

// EdgesFrom returns the direct out-edges of fromCfg (single hop, no traversal).
func (r *GraphRepo) EdgesFrom(ctx context.Context, fromCfg string) ([]*domain.DependencyEdge, error) {
	ctx, span := graphTracer.Start(ctx, "GraphRepo.EdgesFrom")
	defer span.End()
	return r.queryEdges(ctx, span,
		`SELECT `+edgeColumns+` FROM dependency_edge WHERE from_cfg = $1 ORDER BY created_at, id`, fromCfg)
}

// EdgesTo returns the direct in-edges of toCfg (single hop, no traversal).
func (r *GraphRepo) EdgesTo(ctx context.Context, toCfg string) ([]*domain.DependencyEdge, error) {
	ctx, span := graphTracer.Start(ctx, "GraphRepo.EdgesTo")
	defer span.End()
	return r.queryEdges(ctx, span,
		`SELECT `+edgeColumns+` FROM dependency_edge WHERE to_cfg = $1 ORDER BY created_at, id`, toCfg)
}

// Exists reports whether an edge (fromCfg,toCfg,kind) is present.
func (r *GraphRepo) Exists(ctx context.Context, fromCfg, toCfg string, kind domain.EdgeKind) (bool, error) {
	ctx, span := graphTracer.Start(ctx, "GraphRepo.Exists")
	defer span.End()

	var exists bool
	if err := r.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM dependency_edge
		WHERE from_cfg = $1 AND to_cfg = $2 AND edge_kind = $3)`,
		fromCfg, toCfg, string(kind),
	).Scan(&exists); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "exists edge")
		return false, fmt.Errorf("edge exists: %w", err)
	}
	return exists, nil
}

// List returns up to limit edges ordered by (created_at, id).
func (r *GraphRepo) List(ctx context.Context, limit int) ([]*domain.DependencyEdge, error) {
	ctx, span := graphTracer.Start(ctx, "GraphRepo.List")
	defer span.End()
	if limit <= 0 {
		limit = defaultEdgeListLimit
	}
	return r.queryEdges(ctx, span,
		`SELECT `+edgeColumns+` FROM dependency_edge ORDER BY created_at, id LIMIT $1`, limit)
}

func (r *GraphRepo) queryEdges(ctx context.Context, span trace.Span, sql string, args ...any) ([]*domain.DependencyEdge, error) {
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "query edges")
		return nil, fmt.Errorf("query edges: %w", err)
	}
	defer rows.Close()

	var edges []*domain.DependencyEdge
	for rows.Next() {
		edge, err := scanEdge(rows)
		if err != nil {
			return nil, err
		}
		edges = append(edges, edge)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return edges, nil
}

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}
