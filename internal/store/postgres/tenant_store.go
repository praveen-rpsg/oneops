package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
)

const tenantColumns = `tenant_id, slug, name, external_id, status, row_version, created_at, updated_at`

// TenantStore is the PostgreSQL implementation of domain.TenantRepository.
type TenantStore struct {
	pool *pgxpool.Pool
}

// NewTenantStore builds a tenant repository over the given pool.
func NewTenantStore(pool *pgxpool.Pool) *TenantStore {
	return &TenantStore{pool: pool}
}

var _ domain.TenantRepository = (*TenantStore)(nil)

func scanTenant(s scanner) (*domain.Tenant, error) {
	var (
		t      domain.Tenant
		status string
	)
	if err := s.Scan(
		&t.TenantID, &t.Slug, &t.Name, &t.ExternalID, &status,
		&t.RowVersion, &t.CreatedAt, &t.UpdatedAt,
	); err != nil {
		return nil, err
	}
	t.Status = domain.TenantStatus(status)
	return &t, nil
}

// Create inserts a tenant. A duplicate slug or external id is a conflict, not a
// server error: both are unique by index and both are caller-supplied.
func (s *TenantStore) Create(ctx context.Context, t *domain.Tenant) (*domain.Tenant, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO tenant (tenant_id, slug, name, external_id, status)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+tenantColumns,
		t.TenantID, t.Slug, t.Name, t.ExternalID, string(t.Status))

	created, err := scanTenant(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, domain.ErrConflict
		}
		return nil, fmt.Errorf("insert tenant: %w", err)
	}
	return created, nil
}

// Get returns a tenant by its platform identifier.
func (s *TenantStore) Get(ctx context.Context, tenantID string) (*domain.Tenant, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+tenantColumns+` FROM tenant WHERE tenant_id = $1`, tenantID)
	t, err := scanTenant(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get tenant: %w", err)
	}
	return t, nil
}

// GetByExternalID resolves the identifier asserted in a bearer token.
//
// An empty external id never matches. The column defaults to empty for tenants
// with no identity provider bound, so matching on it would let a token with no
// tenant claim resolve to an arbitrary unbound tenant.
func (s *TenantStore) GetByExternalID(ctx context.Context, externalID string) (*domain.Tenant, error) {
	if externalID == "" {
		return nil, domain.ErrNotFound
	}
	row := s.pool.QueryRow(ctx,
		`SELECT `+tenantColumns+` FROM tenant WHERE external_id = $1`, externalID)
	t, err := scanTenant(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get tenant by external id: %w", err)
	}
	return t, nil
}

// List returns all tenants ordered by slug. The tenant table is administrative
// and small by construction — one row per customer — so it is not paginated.
func (s *TenantStore) List(ctx context.Context) ([]*domain.Tenant, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+tenantColumns+` FROM tenant ORDER BY slug`)
	if err != nil {
		return nil, fmt.Errorf("list tenants: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.Tenant, 0, 16)
	for rows.Next() {
		t, err := scanTenant(rows)
		if err != nil {
			return nil, fmt.Errorf("scan tenant: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tenants: %w", err)
	}
	return out, nil
}

// SetStatus suspends or reactivates a tenant under optimistic concurrency,
// matching the row_version discipline used by the configuration registry.
func (s *TenantStore) SetStatus(
	ctx context.Context, tenantID string, rowVersion int64, status domain.TenantStatus,
) (*domain.Tenant, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE tenant
		   SET status = $3, row_version = row_version + 1, updated_at = now()
		 WHERE tenant_id = $1 AND row_version = $2
		RETURNING `+tenantColumns,
		tenantID, rowVersion, string(status))

	t, err := scanTenant(row)
	if errors.Is(err, pgx.ErrNoRows) {
		// Either the tenant is gone or the caller held a stale version. Probing
		// distinguishes them so the caller gets 404 or 409 rather than one
		// ambiguous failure.
		if _, getErr := s.Get(ctx, tenantID); getErr != nil {
			return nil, getErr
		}
		return nil, domain.ErrVersionMismatch
	}
	if err != nil {
		return nil, fmt.Errorf("set tenant status: %w", err)
	}
	return t, nil
}
