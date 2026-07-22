package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// IdempotencyStore persists idempotent responses keyed by client-supplied keys.
type IdempotencyStore struct {
	pool *pgxpool.Pool
}

// NewIdempotencyStore builds a store over the pool.
func NewIdempotencyStore(pool *pgxpool.Pool) *IdempotencyStore {
	return &IdempotencyStore{pool: pool}
}

// Lookup returns a stored response for key, if present.
func (s *IdempotencyStore) Lookup(ctx context.Context, key string) (status int, body []byte, found bool, err error) {
	err = s.pool.QueryRow(ctx,
		`SELECT status, response_body FROM idempotency_key WHERE id = $1`, key,
	).Scan(&status, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil, false, nil
	}
	if err != nil {
		return 0, nil, false, fmt.Errorf("idempotency lookup: %w", err)
	}
	return status, body, true, nil
}

// Save records a response. Concurrent saves for the same key are a no-op.
func (s *IdempotencyStore) Save(ctx context.Context, key, method, path string, status int, body []byte) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO idempotency_key (id, method, path, status, response_body)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (id) DO NOTHING`,
		key, method, path, status, body)
	if err != nil {
		return fmt.Errorf("idempotency save: %w", err)
	}
	return nil
}
