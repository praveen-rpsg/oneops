package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
)

const settingColumns = `tenant_id, key, value, row_version, created_at, updated_at`

// SettingStore administers a tenant's setting overrides.
//
// setting is TENANT-OWNED — it is in TenantOwnedTables and carries row-level
// security — so this store is built from the tenant-scoped pool and GetAll
// takes no tenant argument: the bound connection is the boundary. A caller
// cannot name another tenant's override, because the policy makes the row
// invisible rather than because a predicate remembered to exclude it
// (ADR-TENANCY-002).
//
// Mutations here do NOT pass through withAdminAudit — see the doc comment on
// domain.SettingRepository for why: ADR-AUDIT-007 §6.2 scopes that chokepoint
// to five named identity-governance tables, and setting is not one of them.
type SettingStore struct {
	pool *pgxpool.Pool
}

// NewSettingStore builds the setting repository over the given pool.
func NewSettingStore(pool *pgxpool.Pool) *SettingStore {
	return &SettingStore{pool: pool}
}

var _ domain.SettingRepository = (*SettingStore)(nil)

func scanSetting(s scanner) (*domain.Setting, error) {
	var st domain.Setting
	if err := s.Scan(
		&st.TenantID, &st.Key, &st.Value, &st.RowVersion, &st.CreatedAt, &st.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &st, nil
}

// GetAll returns every stored override for the bound tenant, ordered by key
// so a caller (and a test) sees a deterministic sequence.
func (s *SettingStore) GetAll(ctx context.Context) ([]*domain.Setting, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+settingColumns+` FROM setting ORDER BY key`)
	if err != nil {
		return nil, fmt.Errorf("list settings: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.Setting, 0)
	for rows.Next() {
		st, err := scanSetting(rows)
		if err != nil {
			return nil, fmt.Errorf("scan setting: %w", err)
		}
		out = append(out, st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate settings: %w", err)
	}
	return out, nil
}

// Upsert creates or updates one setting override under optimistic locking.
//
// s.RowVersion == 0 means "create — no override exists yet": a plain INSERT
// that fails closed with ErrVersionMismatch if the (tenant_id, key) pair
// already has a row, via ON CONFLICT DO NOTHING plus a follow-up check that
// distinguishes "already exists" from a genuine failure — the same shape
// TeamStore.AddMember uses for its own idempotent conflict, except here the
// conflict IS the failure the caller asked to be told about.
//
// s.RowVersion > 0 means "update — I last read this version": an UPDATE
// gated on row_version matching, mirroring TeamStore.UpdateProfile.
func (s *SettingStore) Upsert(ctx context.Context, st *domain.Setting) (*domain.Setting, error) {
	if err := st.Validate(); err != nil {
		return nil, err
	}

	if st.RowVersion == 0 {
		row := s.pool.QueryRow(ctx, `
			INSERT INTO setting (tenant_id, key, value)
			VALUES ($1, $2, $3)
			ON CONFLICT (tenant_id, key) DO NOTHING
			RETURNING `+settingColumns,
			st.TenantID, st.Key, st.Value)
		created, err := scanSetting(row)
		if errors.Is(err, pgx.ErrNoRows) {
			// The conflict target matched: an override already exists, and the
			// caller claimed to be creating one from nothing. That is a stale
			// expectation, reported the same way a stale UPDATE is.
			return nil, domain.ErrVersionMismatch
		}
		if err != nil {
			return nil, fmt.Errorf("insert setting: %w", err)
		}
		return created, nil
	}

	row := s.pool.QueryRow(ctx, `
		UPDATE setting
		   SET value = $4, row_version = row_version + 1, updated_at = now()
		 WHERE tenant_id = $1 AND key = $2 AND row_version = $3
		RETURNING `+settingColumns,
		st.TenantID, st.Key, st.RowVersion, st.Value)
	updated, err := scanSetting(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrVersionMismatch
	}
	if err != nil {
		return nil, fmt.Errorf("update setting: %w", err)
	}
	return updated, nil
}
