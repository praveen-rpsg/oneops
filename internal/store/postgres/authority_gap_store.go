package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/rpsg/oneops/internal/domain"
)

var _ domain.CoverageGraph = (*AuthorityStore)(nil)

// Coverage returns the operational-coverage ids declared in the object's
// `coverage` metadata value (comma-delimited). Coverage ids are opaque logical
// identifiers sourced only from Configuration Object metadata — never inferred
// from names, scanned from source, read from document bodies, or probed from
// runtime systems. Absent metadata yields an empty set. Segments are returned as
// stored (whitespace preserved, empties kept) so the evaluator can reject
// malformed entries; the evaluator also rejects duplicates. Additive: no schema
// change.
func (s *AuthorityStore) Coverage(ctx context.Context, cfgID string) ([]string, error) {
	var value string
	err := s.pool.QueryRow(ctx,
		`SELECT value FROM configuration_metadata WHERE cfg_id = $1 AND key = 'coverage'`, cfgID,
	).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("coverage: %w", err)
	}
	return strings.Split(value, ","), nil
}
