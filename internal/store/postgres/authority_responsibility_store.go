package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/rpsg/oneops/internal/domain"
)

var _ domain.ResponsibilityGraph = (*AuthorityStore)(nil)

// Responsibilities returns the responsibility ids declared in the object's
// `responsibilities` metadata value (comma-delimited). Ownership is sourced only
// from Configuration Object metadata — never inferred from names. Absent
// metadata yields an empty set. Additive: no schema change.
func (s *AuthorityStore) Responsibilities(ctx context.Context, cfgID string) ([]string, error) {
	var value string
	err := s.pool.QueryRow(ctx,
		`SELECT value FROM configuration_metadata WHERE cfg_id = $1 AND key = 'responsibilities'`, cfgID,
	).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("responsibilities: %w", err)
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out, nil
}
