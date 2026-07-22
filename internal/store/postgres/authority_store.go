package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
)

// AuthorityStore provides the read-only data the AuthorityResolver consumes:
// the M2 graph accessors (embedded GraphRepo — unchanged) plus baseline
// membership and object existence over the M1 configuration_object table. It is
// additive: it modifies no M1 or M2 code, and adds no schema.
type AuthorityStore struct {
	*GraphRepo
	pool *pgxpool.Pool
}

// NewAuthorityStore builds the store over the given pool.
func NewAuthorityStore(pool *pgxpool.Pool) *AuthorityStore {
	return &AuthorityStore{GraphRepo: NewGraphRepo(pool), pool: pool}
}

var _ domain.AuthorityGraph = (*AuthorityStore)(nil)

// BaselineRoots returns the cfg_ids of current-baseline members (Retention =
// current_baseline is the in-force-baseline signal — State Model §4/§9.3-1),
// ordered by cfg_id for deterministic resolution.
func (s *AuthorityStore) BaselineRoots(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT cfg_id FROM configuration_object WHERE retention_class = 'current_baseline' ORDER BY cfg_id`)
	if err != nil {
		return nil, fmt.Errorf("baseline roots: %w", err)
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
	return ids, rows.Err()
}

// ObjectExists reports whether a Configuration Object with cfgID exists.
func (s *AuthorityStore) ObjectExists(ctx context.Context, cfgID string) (bool, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM configuration_object WHERE cfg_id = $1)`, cfgID,
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("object exists: %w", err)
	}
	return exists, nil
}
