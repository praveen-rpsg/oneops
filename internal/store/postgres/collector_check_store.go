package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/collector"
	"github.com/rpsg/oneops/internal/domain"
)

const (
	defaultCollectorCheckPageSize = 50
	maxCollectorCheckPageSize     = 500
)

const collectorCheckCols = `check_id, tenant_id, asset_id, type, target, interval_seconds,
	enabled, metric_prefix, snmp_community, last_run_at, last_status, row_version, created_at, updated_at`

// CollectorCheckStore administers collector_check: the admin CRUD API's
// tenant-scoped reads/writes (domain.CollectorCheckRepository) AND, when
// built over the PRIVILEGED pool, the scheduler's due-scan/record-run surface
// (collector.Store) — one struct, two roles depending on which pool
// constructs it, exactly mirroring NotificationStore/notification.Store.
//
// collector_check is TENANT-OWNED — it is in TenantOwnedTables and carries
// row-level security — so the tenant-scoped instance takes no tenant
// argument anywhere; the bound connection already is the boundary
// (ADR-TENANCY-002). The privileged instance instead serves every tenant's
// due checks from one process, the same category of cross-tenant background
// worker the webhook dispatcher, notification worker and telemetry
// rollup/retention workers already are.
type CollectorCheckStore struct {
	pool *pgxpool.Pool
}

// NewCollectorCheckStore builds the store over the given pool.
func NewCollectorCheckStore(pool *pgxpool.Pool) *CollectorCheckStore {
	return &CollectorCheckStore{pool: pool}
}

var (
	_ domain.CollectorCheckRepository = (*CollectorCheckStore)(nil)
	_ collector.Store                 = (*CollectorCheckStore)(nil)
)

func clampCollectorCheckPage(limit int) int {
	if limit <= 0 {
		return defaultCollectorCheckPageSize
	}
	if limit > maxCollectorCheckPageSize {
		return maxCollectorCheckPageSize
	}
	return limit
}

func scanCollectorCheck(sc scanner) (*domain.CollectorCheck, error) {
	var (
		c             domain.CollectorCheck
		snmpCommunity *string
		lastRunAt     *time.Time
		lastStatus    *string
	)
	if err := sc.Scan(
		&c.CheckID, &c.TenantID, &c.AssetID, &c.Type, &c.Target, &c.IntervalSeconds,
		&c.Enabled, &c.MetricPrefix, &snmpCommunity, &lastRunAt, &lastStatus, &c.RowVersion, &c.CreatedAt, &c.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if snmpCommunity != nil {
		c.SNMPCommunity = *snmpCommunity
	}
	c.LastRunAt = lastRunAt
	if lastStatus != nil {
		c.LastStatus = domain.CollectorCheckStatus(*lastStatus)
	}
	return &c, nil
}

// Create inserts a check, re-verifying AssetID against this store's own
// tenant-scoped connection first — see domain.CollectorCheckRepository.
// Create's doc comment. RLS on asset filters the existence check to the
// caller's own tenant already: an id belonging to another tenant simply does
// not come back, indistinguishable from one that does not exist at all — the
// same shape AssetStore.CreateRelationship uses for its own endpoints.
func (s *CollectorCheckStore) Create(ctx context.Context, c *domain.CollectorCheck) (*domain.CollectorCheck, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM asset WHERE asset_id = $1)`, c.AssetID,
	).Scan(&exists); err != nil {
		return nil, fmt.Errorf("check collector check asset id: %w", err)
	}
	if !exists {
		return nil, domain.ErrNotFound
	}

	row := s.pool.QueryRow(ctx, `
		INSERT INTO collector_check
			(check_id, tenant_id, asset_id, type, target, interval_seconds, enabled, metric_prefix,
			 snmp_community, row_version, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,1,now(),now())
		RETURNING `+collectorCheckCols,
		c.CheckID, c.TenantID, c.AssetID, c.Type, c.Target, c.IntervalSeconds, c.Enabled, c.MetricPrefix,
		nullString(c.SNMPCommunity))

	created, err := scanCollectorCheck(row)
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("insert collector check: %w", err)
	}
	return created, nil
}

// Get returns a check by identifier, or domain.ErrNotFound.
func (s *CollectorCheckStore) Get(ctx context.Context, checkID string) (*domain.CollectorCheck, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+collectorCheckCols+` FROM collector_check WHERE check_id = $1`, checkID)
	c, err := scanCollectorCheck(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get collector check: %w", err)
	}
	return c, nil
}

// List returns a page of the caller's checks, keyset-paginated over check_id.
func (s *CollectorCheckStore) List(ctx context.Context, limit int, after string) ([]*domain.CollectorCheck, error) {
	limit = clampCollectorCheckPage(limit)
	rows, err := s.pool.Query(ctx, `
		SELECT `+collectorCheckCols+`
		  FROM collector_check
		 WHERE ($1 = '' OR check_id > $1)
		 ORDER BY check_id
		 LIMIT $2`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list collector checks: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.CollectorCheck, 0, limit)
	for rows.Next() {
		c, err := scanCollectorCheck(rows)
		if err != nil {
			return nil, fmt.Errorf("scan collector check: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate collector checks: %w", err)
	}
	return out, nil
}

// Update changes one or more fields under optimistic locking. AssetID cannot
// be changed here — see domain.CollectorCheckPatch's doc comment — nor can
// LastRunAt/LastStatus, which RecordRun owns exclusively.
func (s *CollectorCheckStore) Update(
	ctx context.Context, checkID string, rowVersion int64, patch domain.CollectorCheckPatch,
) (*domain.CollectorCheck, error) {
	current, err := s.Get(ctx, checkID)
	if err != nil {
		return nil, err
	}
	if patch.Type != nil {
		current.Type = *patch.Type
	}
	if patch.Target != nil {
		current.Target = *patch.Target
	}
	if patch.IntervalSeconds != nil {
		current.IntervalSeconds = *patch.IntervalSeconds
	}
	if patch.Enabled != nil {
		current.Enabled = *patch.Enabled
	}
	if patch.MetricPrefix != nil {
		current.MetricPrefix = *patch.MetricPrefix
	}
	if patch.SNMPCommunity != nil {
		current.SNMPCommunity = *patch.SNMPCommunity
	}
	if err := current.Validate(); err != nil {
		return nil, err
	}

	row := s.pool.QueryRow(ctx, `
		UPDATE collector_check
		   SET type = $3, target = $4, interval_seconds = $5, enabled = $6, metric_prefix = $7,
		       snmp_community = $8, row_version = row_version + 1, updated_at = now()
		 WHERE check_id = $1 AND row_version = $2
		RETURNING `+collectorCheckCols,
		checkID, rowVersion, current.Type, current.Target, current.IntervalSeconds, current.Enabled, current.MetricPrefix,
		nullString(current.SNMPCommunity))

	updated, err := scanCollectorCheck(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, s.explainMissedUpdate(ctx, checkID)
	}
	if err != nil {
		return nil, fmt.Errorf("update collector check: %w", err)
	}
	return updated, nil
}

// explainMissedUpdate distinguishes "gone" from "stale version" after an
// UPDATE matched no row — mirrors TeamStore.explainMissedUpdate.
func (s *CollectorCheckStore) explainMissedUpdate(ctx context.Context, checkID string) error {
	if _, err := s.Get(ctx, checkID); err != nil {
		return err
	}
	return domain.ErrVersionMismatch
}

// Delete removes a check, or returns domain.ErrNotFound.
func (s *CollectorCheckStore) Delete(ctx context.Context, checkID string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM collector_check WHERE check_id = $1`, checkID)
	if err != nil {
		return fmt.Errorf("delete collector check: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// DueChecks implements collector.Store: enabled checks, across every tenant
// when built over the privileged pool, whose interval has elapsed since
// LastRunAt (or that have never run), oldest-due first. See collector.Store's
// doc comment for why a plain scan (rather than FOR UPDATE SKIP LOCKED) is
// sufficient here.
func (s *CollectorCheckStore) DueChecks(ctx context.Context, now time.Time, limit int) ([]*domain.CollectorCheck, error) {
	if limit <= 0 {
		limit = defaultCollectorCheckPageSize
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+collectorCheckCols+`
		  FROM collector_check
		 WHERE enabled = true
		   AND (last_run_at IS NULL OR last_run_at <= $1::timestamptz - make_interval(secs => interval_seconds))
		 ORDER BY last_run_at NULLS FIRST
		 LIMIT $2`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("due-scan collector checks: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.CollectorCheck, 0, limit)
	for rows.Next() {
		c, err := scanCollectorCheck(rows)
		if err != nil {
			return nil, fmt.Errorf("scan due collector check: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate due collector checks: %w", err)
	}
	return out, nil
}

// RecordRun implements collector.Store — see its doc comment for why this
// deliberately does not touch row_version/updated_at.
func (s *CollectorCheckStore) RecordRun(ctx context.Context, checkID string, runAt time.Time, status domain.CollectorCheckStatus) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE collector_check SET last_run_at = $2, last_status = $3 WHERE check_id = $1`,
		checkID, runAt, string(status))
	if err != nil {
		return fmt.Errorf("record collector check run: %w", err)
	}
	return nil
}
