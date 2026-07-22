package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/rpsg/oneops/internal/domain"
)

var _ domain.CitationGraph = (*AuthorityStore)(nil)

// Citations returns the citation ids declared in the object's `citations`
// metadata value (comma-delimited). Citations are logical identifiers sourced
// only from Configuration Object metadata — never inferred from names, scanned
// from source, or read from document bodies. Absent metadata yields an empty
// set. Segments are returned as stored (whitespace preserved, empties kept) so
// the evaluator can reject malformed entries; the evaluator also rejects
// duplicates and references to unknown objects. Additive: no schema change.
func (s *AuthorityStore) Citations(ctx context.Context, cfgID string) ([]string, error) {
	var value string
	err := s.pool.QueryRow(ctx,
		`SELECT value FROM configuration_metadata WHERE cfg_id = $1 AND key = 'citations'`, cfgID,
	).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("citations: %w", err)
	}
	return strings.Split(value, ","), nil
}
