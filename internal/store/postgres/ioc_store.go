package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
)

const (
	defaultIOCPageSize = 50
	maxIOCPageSize     = 500
)

const iocCols = `ioc_id, tenant_id, indicator_type, indicator_value, severity, enabled, description, source,
	row_version, created_at, updated_at`

// IOCStore administers ioc: E8.2a's indicator-of-compromise watchlist —
// built EXCLUSIVELY over the tenant-scoped pool, mirroring SecurityRuleStore.
// Unlike SecurityRuleStore.Create, there is no asset_id to re-verify: an IOC
// is not asset-scoped, it is a tenant-wide indicator (docs/PLATFORM-BUILD-PLAN.md
// §4, ADR-SOC-004).
//
// ioc is TENANT-OWNED — it is in TenantOwnedTables and carries row-level
// security — so this store takes no tenant argument anywhere; the bound
// connection already is the boundary (ADR-TENANCY-002).
type IOCStore struct {
	pool *pgxpool.Pool
}

// NewIOCStore builds the store over the given (tenant-scoped) pool.
func NewIOCStore(pool *pgxpool.Pool) *IOCStore {
	return &IOCStore{pool: pool}
}

var _ domain.IOCRepository = (*IOCStore)(nil)

func clampIOCPage(limit int) int {
	if limit <= 0 {
		return defaultIOCPageSize
	}
	if limit > maxIOCPageSize {
		return maxIOCPageSize
	}
	return limit
}

func scanIOC(sc scanner) (*domain.IOC, error) {
	var (
		i             domain.IOC
		indicatorType string
		severity      string
	)
	if err := sc.Scan(
		&i.IOCID, &i.TenantID, &indicatorType, &i.IndicatorValue, &severity, &i.Enabled, &i.Description, &i.Source,
		&i.RowVersion, &i.CreatedAt, &i.UpdatedAt,
	); err != nil {
		return nil, err
	}
	i.IndicatorType = domain.IOCIndicatorType(indicatorType)
	i.Severity = domain.IncidentSeverity(severity)
	return &i, nil
}

// Create inserts an entry. A duplicate (tenant_id, indicator_type,
// indicator_value) — the same indicator already on this tenant's watchlist —
// surfaces as domain.ErrConflict, never a silently-absorbed or
// cross-tenant-visible write; the constraint is tenant-scoped, so the same
// indicator_value may exist independently under a different tenant.
func (s *IOCStore) Create(ctx context.Context, i *domain.IOC) (*domain.IOC, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}

	row := s.pool.QueryRow(ctx, `
		INSERT INTO ioc
			(ioc_id, tenant_id, indicator_type, indicator_value, severity, enabled, description, source,
			 row_version, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,1,now(),now())
		RETURNING `+iocCols,
		i.IOCID, i.TenantID, string(i.IndicatorType), i.IndicatorValue, string(i.Severity), i.Enabled,
		i.Description, i.Source)

	created, err := scanIOC(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, domain.ErrConflict
		}
		return nil, fmt.Errorf("insert ioc: %w", err)
	}
	return created, nil
}

// Get returns an entry by identifier, or domain.ErrNotFound.
func (s *IOCStore) Get(ctx context.Context, iocID string) (*domain.IOC, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+iocCols+` FROM ioc WHERE ioc_id = $1`, iocID)
	i, err := scanIOC(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get ioc: %w", err)
	}
	return i, nil
}

// List returns a page of the caller's entries, keyset-paginated over ioc_id.
func (s *IOCStore) List(ctx context.Context, limit int, after string) ([]*domain.IOC, error) {
	limit = clampIOCPage(limit)
	rows, err := s.pool.Query(ctx, `
		SELECT `+iocCols+`
		  FROM ioc
		 WHERE ($1 = '' OR ioc_id > $1)
		 ORDER BY ioc_id
		 LIMIT $2`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list iocs: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.IOC, 0, limit)
	for rows.Next() {
		i, err := scanIOC(rows)
		if err != nil {
			return nil, fmt.Errorf("scan ioc: %w", err)
		}
		out = append(out, i)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate iocs: %w", err)
	}
	return out, nil
}

// Update changes one or more fields under optimistic locking. IndicatorType
// and IndicatorValue cannot be changed here — see domain.IOCPatch's doc
// comment.
func (s *IOCStore) Update(
	ctx context.Context, iocID string, rowVersion int64, patch domain.IOCPatch,
) (*domain.IOC, error) {
	current, err := s.Get(ctx, iocID)
	if err != nil {
		return nil, err
	}
	if patch.Severity != nil {
		current.Severity = *patch.Severity
	}
	if patch.Enabled != nil {
		current.Enabled = *patch.Enabled
	}
	if patch.Description != nil {
		current.Description = *patch.Description
	}
	if patch.Source != nil {
		current.Source = *patch.Source
	}
	if err := current.Validate(); err != nil {
		return nil, err
	}

	row := s.pool.QueryRow(ctx, `
		UPDATE ioc
		   SET severity = $3, enabled = $4, description = $5, source = $6,
		       row_version = row_version + 1, updated_at = now()
		 WHERE ioc_id = $1 AND row_version = $2
		RETURNING `+iocCols,
		iocID, rowVersion, string(current.Severity), current.Enabled, current.Description, current.Source)

	updated, err := scanIOC(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, s.explainMissedUpdate(ctx, iocID)
	}
	if err != nil {
		return nil, fmt.Errorf("update ioc: %w", err)
	}
	return updated, nil
}

// explainMissedUpdate distinguishes "gone" from "stale version" after an
// UPDATE matched no row — mirrors SecurityRuleStore.explainMissedUpdate.
func (s *IOCStore) explainMissedUpdate(ctx context.Context, iocID string) error {
	if _, err := s.Get(ctx, iocID); err != nil {
		return err
	}
	return domain.ErrVersionMismatch
}

// Delete removes an entry, or returns domain.ErrNotFound. Carries no
// row_version — see ADR-HARD-003, which applies here unchanged (mirroring
// SecurityRuleStore.Delete).
func (s *IOCStore) Delete(ctx context.Context, iocID string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM ioc WHERE ioc_id = $1`, iocID)
	if err != nil {
		return fmt.Errorf("delete ioc: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}
