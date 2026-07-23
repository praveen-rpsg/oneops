package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/governance"
)

var _ governance.Store = (*GovernanceStore)(nil)

// GovernanceStore is the transaction-scoped persistence adapter for the
// Configuration Operations engine. The engine owns the transaction; this store
// only reads/writes within the passed pgx.Tx. It adds no schema and reuses the
// M1 configuration_object and M2 dependency_edge tables.
type GovernanceStore struct {
	pool *pgxpool.Pool
}

// NewGovernanceStore builds the store over the given pool.
func NewGovernanceStore(pool *pgxpool.Pool) *GovernanceStore {
	return &GovernanceStore{pool: pool}
}

// Begin starts the single transaction the engine owns for one operation.
func (s *GovernanceStore) Begin(ctx context.Context) (pgx.Tx, error) {
	return s.pool.Begin(ctx)
}

// GetForUpdate loads and row-locks the object within the operation's transaction.
func (s *GovernanceStore) GetForUpdate(ctx context.Context, tx pgx.Tx, cfgID string) (*domain.ConfigObject, error) {
	obj, err := scanCO(tx.QueryRow(ctx,
		`SELECT `+coColumns+` FROM configuration_object WHERE cfg_id = $1 FOR UPDATE`, cfgID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("governance get: %w", err)
	}
	return obj, nil
}

// ApplyDimensions writes the new dimensional values under optimistic concurrency,
// returning the new row_version. A version mismatch yields domain.ErrVersionMismatch.
func (s *GovernanceStore) ApplyDimensions(ctx context.Context, tx pgx.Tx, cfgID string, expected int64, lifecycle domain.Lifecycle, retention domain.RetentionClass, authority domain.Authority) (int64, error) {
	var rv int64
	err := tx.QueryRow(ctx, `
		UPDATE configuration_object
		   SET lifecycle = $1, retention_class = $2, authority = $3,
		       row_version = row_version + 1, updated_at = now()
		 WHERE cfg_id = $4 AND row_version = $5
		RETURNING row_version`,
		string(lifecycle), string(retention), string(authority), cfgID, expected).Scan(&rv)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, domain.ErrVersionMismatch
	}
	if err != nil {
		return 0, fmt.Errorf("governance apply dimensions: %w", err)
	}
	return rv, nil
}

// CountDependents returns the number of incoming dependency edges for cfgID.
func (s *GovernanceStore) CountDependents(ctx context.Context, tx pgx.Tx, cfgID string) (int, error) {
	var n int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM dependency_edge WHERE to_cfg = $1`, cfgID).Scan(&n); err != nil {
		return 0, fmt.Errorf("governance count dependents: %w", err)
	}
	return n, nil
}

// RecordEdge inserts a dependency edge within the operation's transaction,
// reusing the M2 dependency_edge table unchanged. The schema's foreign keys make
// a missing endpoint a domain.ErrNotFound and its uniqueness constraint makes a
// duplicate edge a domain.ErrConflict, so the engine needs no separate existence
// check. It adds no schema.
func (s *GovernanceStore) RecordEdge(ctx context.Context, tx pgx.Tx, from, to string, kind domain.EdgeKind) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO dependency_edge (id, from_cfg, to_cfg, edge_kind)
		VALUES ($1,$2,$3,$4)`,
		domain.NewID(), from, to, string(kind))
	switch {
	case err == nil:
		return nil
	case isUniqueViolation(err):
		return domain.ErrConflict
	case isForeignKeyViolation(err):
		return domain.ErrNotFound
	default:
		return fmt.Errorf("governance record edge: %w", err)
	}
}

// RemoveObject deletes the object under optimistic concurrency. A version
// mismatch (or missing object) yields domain.ErrVersionMismatch.
func (s *GovernanceStore) RemoveObject(ctx context.Context, tx pgx.Tx, cfgID string, expected int64) error {
	tag, err := tx.Exec(ctx,
		`DELETE FROM configuration_object WHERE cfg_id = $1 AND row_version = $2`, cfgID, expected)
	if err != nil {
		return fmt.Errorf("governance remove: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrVersionMismatch
	}
	return nil
}
