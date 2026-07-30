package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
)

const organizationColumns = `org_id, tenant_id, slug, name, status, row_version, created_at, updated_at`

// defaultOrgPageSize bounds an unbounded List; maxOrgPageSize caps what a
// caller may ask for. Both mirror the user registry rather than inventing a
// second paging policy.
const (
	defaultOrgPageSize = 50
	maxOrgPageSize     = 500
)

// OrganizationStore is the PostgreSQL implementation of
// domain.OrganizationRepository.
//
// organization is GLOBAL: no tenant_id ownership, absent from
// TenantOwnedTables, no row-level security (ADR-IDENTITY-002 §3). Nothing here
// is scoped by tenant — the tenant_id column is the boundary this organisation
// is realised as, not the row's owner.
type OrganizationStore struct {
	pool *pgxpool.Pool
}

// NewOrganizationStore builds an organisation repository over the given pool.
func NewOrganizationStore(pool *pgxpool.Pool) *OrganizationStore {
	return &OrganizationStore{pool: pool}
}

var _ domain.OrganizationRepository = (*OrganizationStore)(nil)

func scanOrganization(s scanner) (*domain.Organization, error) {
	var (
		o      domain.Organization
		status string
	)
	if err := s.Scan(
		&o.OrgID, &o.TenantID, &o.Slug, &o.Name, &status,
		&o.RowVersion, &o.CreatedAt, &o.UpdatedAt,
	); err != nil {
		return nil, err
	}
	o.Status = domain.OrganizationStatus(status)
	return &o, nil
}

// Create inserts the tenant and the organisation in one transaction.
//
// Both rows or neither (ADR-IDENTITY-001 §7.1). An organisation whose tenant
// insert succeeded and whose own insert did not would be an isolation boundary
// nothing points at; the reverse would be an Identity scope with no isolation,
// reachable and unprotected. The transaction is what makes the 1:1 true at
// every instant rather than eventually.
//
// A duplicate slug is a conflict, not a server error: it is caller-supplied and
// unique by constraint on both tables.
func (s *OrganizationStore) Create(ctx context.Context, o *domain.Organization) (*domain.Organization, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin organization create: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The tenant is created first: organization.tenant_id references it, so the
	// other order fails on the foreign key.
	if _, err := tx.Exec(ctx, `
		INSERT INTO tenant (tenant_id, slug, name, status)
		VALUES ($1, $2, $3, 'active')`,
		o.TenantID, o.Slug, o.Name); err != nil {
		if isUniqueViolation(err) {
			return nil, domain.ErrConflict
		}
		return nil, fmt.Errorf("insert tenant for organization: %w", err)
	}

	row := tx.QueryRow(ctx, `
		INSERT INTO organization (org_id, tenant_id, slug, name, status)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+organizationColumns,
		o.OrgID, o.TenantID, o.Slug, o.Name, string(o.Status))

	created, err := scanOrganization(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, domain.ErrConflict
		}
		return nil, fmt.Errorf("insert organization: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit organization create: %w", err)
	}
	return created, nil
}

// Get returns an organisation by platform identifier.
func (s *OrganizationStore) Get(ctx context.Context, orgID string) (*domain.Organization, error) {
	return s.one(ctx, `SELECT `+organizationColumns+` FROM organization WHERE org_id = $1`, orgID)
}

// GetByTenant resolves the organisation a tenant realises.
func (s *OrganizationStore) GetByTenant(ctx context.Context, tenantID string) (*domain.Organization, error) {
	if tenantID == "" {
		// An empty tenant matches nothing, and any match would be a bug.
		return nil, domain.ErrNotFound
	}
	return s.one(ctx, `SELECT `+organizationColumns+` FROM organization WHERE tenant_id = $1`, tenantID)
}

// GetBySlug resolves an organisation by its addressable name.
func (s *OrganizationStore) GetBySlug(ctx context.Context, slug string) (*domain.Organization, error) {
	if slug == "" {
		return nil, domain.ErrNotFound
	}
	return s.one(ctx, `SELECT `+organizationColumns+` FROM organization WHERE slug = $1`, slug)
}

func (s *OrganizationStore) one(ctx context.Context, query string, arg any) (*domain.Organization, error) {
	o, err := scanOrganization(s.pool.QueryRow(ctx, query, arg))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get organization: %w", err)
	}
	return o, nil
}

// List returns a page ordered by org_id, which is a ULID and therefore ordered
// by creation. Keyset pagination is used rather than OFFSET so a page does not
// shift under concurrent inserts.
func (s *OrganizationStore) List(ctx context.Context, limit int, after string) ([]*domain.Organization, error) {
	if limit <= 0 {
		limit = defaultOrgPageSize
	}
	if limit > maxOrgPageSize {
		limit = maxOrgPageSize
	}

	rows, err := s.pool.Query(ctx, `
		SELECT `+organizationColumns+`
		  FROM organization
		 WHERE ($1 = '' OR org_id > $1)
		 ORDER BY org_id
		 LIMIT $2`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list organizations: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.Organization, 0, limit)
	for rows.Next() {
		o, err := scanOrganization(rows)
		if err != nil {
			return nil, fmt.Errorf("scan organization: %w", err)
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate organizations: %w", err)
	}
	return out, nil
}

// SetStatus suspends or reactivates, cascading to the realising tenant.
//
// The cascade is the point (ADR-IDENTITY-001 §8.3). Suspending the organisation
// alone would leave its tenant serving requests for a scope nobody may enter,
// and the authentication boundary reads tenant status, not organisation status
// — so a half-applied suspension would be no suspension at all.
//
// Both writes share one transaction, and the row-version guard is on the
// organisation: it is the record the caller read.
func (s *OrganizationStore) SetStatus(
	ctx context.Context, orgID string, rowVersion int64, status domain.OrganizationStatus,
) (*domain.Organization, error) {
	if !status.Valid() {
		return nil, domain.NewValidationError("status", "must be one of: active, suspended")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin organization status change: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row := tx.QueryRow(ctx, `
		UPDATE organization
		   SET status = $3, row_version = row_version + 1, updated_at = now()
		 WHERE org_id = $1 AND row_version = $2
		RETURNING `+organizationColumns,
		orgID, rowVersion, string(status))

	updated, err := scanOrganization(row)
	if errors.Is(err, pgx.ErrNoRows) {
		// Either it is gone or the caller held a stale version. Probing
		// distinguishes them so the caller gets 404 or 409, not one ambiguous
		// failure — the same shape TenantStore.SetStatus uses.
		if _, getErr := s.Get(ctx, orgID); getErr != nil {
			return nil, getErr
		}
		return nil, domain.ErrVersionMismatch
	}
	if err != nil {
		return nil, fmt.Errorf("set organization status: %w", err)
	}

	tenantStatus := domain.TenantActive
	if status == domain.OrganizationSuspended {
		tenantStatus = domain.TenantSuspended
	}
	if _, err := tx.Exec(ctx, `
		UPDATE tenant
		   SET status = $2, row_version = row_version + 1, updated_at = now()
		 WHERE tenant_id = $1`,
		updated.TenantID, string(tenantStatus)); err != nil {
		return nil, fmt.Errorf("cascade status to tenant: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit organization status change: %w", err)
	}
	return updated, nil
}
