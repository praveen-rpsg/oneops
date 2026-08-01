package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
)

const (
	assetColumns    = `asset_id, tenant_id, type, name, attributes, status, row_version, created_at, updated_at`
	assetRelColumns = `relationship_id, tenant_id, from_asset_id, to_asset_id, type, row_version, created_at, updated_at`
)

// defaultAssetPageSize bounds an unbounded List; maxAssetPageSize caps what a
// caller may ask for. Same shape as the other tenant-owned stores.
const (
	defaultAssetPageSize = 50
	maxAssetPageSize     = 500
)

// AssetStore administers the CMDB: assets and the relationships between them.
//
// asset and asset_relationship are TENANT-OWNED — both are in
// TenantOwnedTables and carry row-level security — so this store is built
// from the tenant-scoped pool and takes no tenant argument anywhere: the
// bound connection is the boundary. A caller cannot name another tenant's
// asset, because the policy makes the row invisible rather than because a
// predicate remembered to exclude it (ADR-TENANCY-002).
//
// Mutations here do NOT pass through withAdminAudit — see the doc comment on
// domain.AssetRepository for why: ADR-AUDIT-007 §6.2 scopes that chokepoint to
// five named identity-governance tables, and Asset is not one of them.
type AssetStore struct {
	pool *pgxpool.Pool
}

// NewAssetStore builds the asset repository over the given pool.
func NewAssetStore(pool *pgxpool.Pool) *AssetStore {
	return &AssetStore{pool: pool}
}

var _ domain.AssetRepository = (*AssetStore)(nil)

func clampAssetPage(limit int) int {
	if limit <= 0 {
		return defaultAssetPageSize
	}
	if limit > maxAssetPageSize {
		return maxAssetPageSize
	}
	return limit
}

func scanAsset(s scanner) (*domain.Asset, error) {
	var (
		a          domain.Asset
		status     string
		attrsBytes []byte
	)
	if err := s.Scan(
		&a.AssetID, &a.TenantID, &a.Type, &a.Name, &attrsBytes, &status,
		&a.RowVersion, &a.CreatedAt, &a.UpdatedAt,
	); err != nil {
		return nil, err
	}
	a.Status = domain.AssetStatus(status)
	a.Attributes = map[string]any{}
	if len(attrsBytes) > 0 {
		if err := json.Unmarshal(attrsBytes, &a.Attributes); err != nil {
			return nil, fmt.Errorf("unmarshal asset attributes: %w", err)
		}
	}
	return &a, nil
}

func scanAssetRelationship(s scanner) (*domain.AssetRelationship, error) {
	var (
		r    domain.AssetRelationship
		kind string
	)
	if err := s.Scan(
		&r.RelationshipID, &r.TenantID, &r.FromAssetID, &r.ToAssetID, &kind,
		&r.RowVersion, &r.CreatedAt, &r.UpdatedAt,
	); err != nil {
		return nil, err
	}
	r.Type = domain.RelationshipType(kind)
	return &r, nil
}

// Create inserts an asset.
func (s *AssetStore) Create(ctx context.Context, a *domain.Asset) (*domain.Asset, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}
	attrs, err := json.Marshal(a.Attributes)
	if err != nil {
		return nil, fmt.Errorf("marshal asset attributes: %w", err)
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO asset (asset_id, tenant_id, type, name, attributes, status)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+assetColumns,
		a.AssetID, a.TenantID, a.Type, a.Name, attrs, string(a.Status))

	created, err := scanAsset(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, domain.ErrConflict
		}
		return nil, fmt.Errorf("insert asset: %w", err)
	}
	return created, nil
}

// Get returns an asset by identifier.
func (s *AssetStore) Get(ctx context.Context, assetID string) (*domain.Asset, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+assetColumns+` FROM asset WHERE asset_id = $1`, assetID)
	a, err := scanAsset(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get asset: %w", err)
	}
	return a, nil
}

// List returns a page of the caller's assets, keyset-paginated over
// asset_id — a ULID, and therefore ordered by creation.
func (s *AssetStore) List(ctx context.Context, limit int, after string) ([]*domain.Asset, error) {
	limit = clampAssetPage(limit)

	rows, err := s.pool.Query(ctx, `
		SELECT `+assetColumns+`
		  FROM asset
		 WHERE ($1 = '' OR asset_id > $1)
		 ORDER BY asset_id
		 LIMIT $2`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list assets: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.Asset, 0, limit)
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			return nil, fmt.Errorf("scan asset: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate assets: %w", err)
	}
	return out, nil
}

// Update changes name, attributes and/or status under optimistic locking. A
// nil name/status or nil attributes map leaves that field unchanged —
// COALESCE against a NULL bind parameter, the same way a caller signals "not
// supplied" rather than "clear it".
func (s *AssetStore) Update(
	ctx context.Context, assetID string, rowVersion int64,
	name *string, attributes map[string]any, status *domain.AssetStatus,
) (*domain.Asset, error) {
	var namePtr *string
	if name != nil {
		trimmed := strings.TrimSpace(*name)
		if trimmed == "" {
			return nil, domain.NewValidationError("name", "must not be empty")
		}
		if len(trimmed) > domain.MaxAssetNameLength {
			return nil, domain.NewValidationError("name", "must be at most 200 characters")
		}
		namePtr = &trimmed
	}

	var statusPtr *string
	if status != nil {
		if !status.Valid() {
			return nil, domain.NewValidationError("status", "must be one of: active, retired")
		}
		v := string(*status)
		statusPtr = &v
	}

	var attrs []byte
	if attributes != nil {
		b, err := json.Marshal(attributes)
		if err != nil {
			return nil, fmt.Errorf("marshal asset attributes: %w", err)
		}
		attrs = b
	}

	row := s.pool.QueryRow(ctx, `
		UPDATE asset
		   SET name        = COALESCE($3, name),
		       attributes  = COALESCE($4, attributes),
		       status      = COALESCE($5, status),
		       row_version = row_version + 1,
		       updated_at  = now()
		 WHERE asset_id = $1 AND row_version = $2
		RETURNING `+assetColumns,
		assetID, rowVersion, namePtr, attrs, statusPtr)

	a, err := scanAsset(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, s.explainMissedUpdate(ctx, assetID)
	}
	if err != nil {
		return nil, fmt.Errorf("update asset: %w", err)
	}
	return a, nil
}

// Delete removes an asset. Its relationships go with it (ON DELETE CASCADE).
func (s *AssetStore) Delete(ctx context.Context, assetID string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM asset WHERE asset_id = $1`, assetID)
	if err != nil {
		return fmt.Errorf("delete asset: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// explainMissedUpdate distinguishes "gone" from "stale version" after an
// UPDATE matched no row, so the caller gets 404 or 409 rather than one
// ambiguous failure. This mirrors TeamStore.explainMissedUpdate.
func (s *AssetStore) explainMissedUpdate(ctx context.Context, assetID string) error {
	if _, err := s.Get(ctx, assetID); err != nil {
		return err
	}
	return domain.ErrVersionMismatch
}

// CreateRelationship inserts a relationship between two assets.
//
// Both endpoints are confirmed to exist on THIS store's own tenant-scoped,
// RLS-enforced connection before the INSERT runs. That confirmation is load
// bearing, not defensive: PostgreSQL's foreign-key triggers run with the
// constraint's own privileges and BYPASS row-level security on the
// referenced table (a documented PostgreSQL behaviour, not a bug), so the
// `REFERENCES asset (asset_id)` constraint alone would let a caller name
// another tenant's asset_id and have the FK accept it — creating a
// cross-tenant edge the CMDB graph would then traverse. The existence check
// below runs the same policy the rest of this store's reads do, so a
// cross-tenant id is indistinguishable from one that does not exist at all,
// and both report ErrNotFound (ADR-ASSET-001 §6).
func (s *AssetStore) CreateRelationship(ctx context.Context, r *domain.AssetRelationship) (*domain.AssetRelationship, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	for _, assetID := range []string{r.FromAssetID, r.ToAssetID} {
		var exists bool
		if err := s.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM asset WHERE asset_id = $1)`, assetID,
		).Scan(&exists); err != nil {
			return nil, fmt.Errorf("check asset exists: %w", err)
		}
		if !exists {
			return nil, domain.ErrNotFound
		}
	}

	row := s.pool.QueryRow(ctx, `
		INSERT INTO asset_relationship (relationship_id, tenant_id, from_asset_id, to_asset_id, type)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+assetRelColumns,
		r.RelationshipID, r.TenantID, r.FromAssetID, r.ToAssetID, string(r.Type))

	created, err := scanAssetRelationship(row)
	if err != nil {
		switch {
		case isUniqueViolation(err):
			return nil, domain.ErrConflict
		case isForeignKeyViolation(err):
			return nil, domain.ErrNotFound
		default:
			return nil, fmt.Errorf("insert asset relationship: %w", err)
		}
	}
	return created, nil
}

// DeleteRelationship removes a relationship by id, or returns ErrNotFound.
func (s *AssetStore) DeleteRelationship(ctx context.Context, relationshipID string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM asset_relationship WHERE relationship_id = $1`, relationshipID)
	if err != nil {
		return fmt.Errorf("delete asset relationship: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// RelationshipsFrom returns the direct out-edges of assetID (no traversal).
func (s *AssetStore) RelationshipsFrom(ctx context.Context, assetID string) ([]*domain.AssetRelationship, error) {
	return s.queryRelationships(ctx,
		`SELECT `+assetRelColumns+` FROM asset_relationship WHERE from_asset_id = $1 ORDER BY created_at, relationship_id`,
		assetID)
}

// RelationshipsTo returns the direct in-edges of assetID (no traversal).
func (s *AssetStore) RelationshipsTo(ctx context.Context, assetID string) ([]*domain.AssetRelationship, error) {
	return s.queryRelationships(ctx,
		`SELECT `+assetRelColumns+` FROM asset_relationship WHERE to_asset_id = $1 ORDER BY created_at, relationship_id`,
		assetID)
}

func (s *AssetStore) queryRelationships(ctx context.Context, sql, arg string) ([]*domain.AssetRelationship, error) {
	rows, err := s.pool.Query(ctx, sql, arg)
	if err != nil {
		return nil, fmt.Errorf("query asset relationships: %w", err)
	}
	defer rows.Close()

	var out []*domain.AssetRelationship
	for rows.Next() {
		r, err := scanAssetRelationship(rows)
		if err != nil {
			return nil, fmt.Errorf("scan asset relationship: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate asset relationships: %w", err)
	}
	return out, nil
}
