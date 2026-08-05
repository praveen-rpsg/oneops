package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/alerting"
	"github.com/rpsg/oneops/internal/domain"
)

const (
	defaultMaintenanceWindowPageSize = 50
	maxMaintenanceWindowPageSize     = 500
)

const maintenanceWindowCols = `window_id, tenant_id, asset_id, starts_at, ends_at, reason, created_by,
	suppressed_count, last_suppressed_at, row_version, created_at, updated_at`

// MaintenanceWindowStore administers maintenance_window: the admin CRUD API's
// tenant-scoped reads/writes (domain.MaintenanceWindowRepository) AND, when
// built over the PRIVILEGED pool, the evaluator's active-window/suppression
// surface (alerting.MaintenanceWindowChecker) — one struct, two roles
// depending on which pool constructs it, exactly mirroring
// AlertRuleStore/alerting.Store (E3.1/E3.2) and IncidentStore/
// alerting.IncidentCorrelator (E4.1).
//
// maintenance_window is TENANT-OWNED — it is in TenantOwnedTables and
// carries row-level security — so the tenant-scoped instance takes no
// tenant argument anywhere; the bound connection already is the boundary
// (ADR-TENANCY-002). The privileged instance instead answers "is (tenantID,
// assetID) inside an active window right now" for every tenant's firing
// rules from one process, the same category of cross-tenant background
// consumer the evaluator's telemetry read and E4.1 correlation already are.
type MaintenanceWindowStore struct {
	pool *pgxpool.Pool
}

// NewMaintenanceWindowStore builds the store over the given pool.
func NewMaintenanceWindowStore(pool *pgxpool.Pool) *MaintenanceWindowStore {
	return &MaintenanceWindowStore{pool: pool}
}

var _ domain.MaintenanceWindowRepository = (*MaintenanceWindowStore)(nil)

func clampMaintenanceWindowPage(limit int) int {
	if limit <= 0 {
		return defaultMaintenanceWindowPageSize
	}
	if limit > maxMaintenanceWindowPageSize {
		return maxMaintenanceWindowPageSize
	}
	return limit
}

func scanMaintenanceWindow(sc scanner) (*domain.MaintenanceWindow, error) {
	var (
		w                domain.MaintenanceWindow
		lastSuppressedAt *time.Time
	)
	if err := sc.Scan(
		&w.WindowID, &w.TenantID, &w.AssetID, &w.StartsAt, &w.EndsAt, &w.Reason, &w.CreatedBy,
		&w.SuppressedCount, &lastSuppressedAt, &w.RowVersion, &w.CreatedAt, &w.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if lastSuppressedAt != nil {
		w.LastSuppressedAt = *lastSuppressedAt
	}
	return &w, nil
}

// Create inserts a window, re-verifying AssetID against this store's own
// tenant-scoped connection first — see domain.MaintenanceWindowRepository.
// Create's doc comment. RLS on asset filters the existence check to the
// caller's own tenant already: an id belonging to another tenant simply does
// not come back, indistinguishable from one that does not exist at all — the
// same shape AlertRuleStore.Create uses. CreatedBy is taken from
// ActorFrom(ctx), never from w, mirroring IncidentStore.Create's discipline.
func (s *MaintenanceWindowStore) Create(ctx context.Context, w *domain.MaintenanceWindow) (*domain.MaintenanceWindow, error) {
	if err := w.Validate(); err != nil {
		return nil, err
	}
	actor, ok := domain.ActorFrom(ctx)
	if !ok {
		return nil, domain.ErrNoActor
	}
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM asset WHERE asset_id = $1)`, w.AssetID,
	).Scan(&exists); err != nil {
		return nil, fmt.Errorf("check maintenance window asset id: %w", err)
	}
	if !exists {
		return nil, domain.ErrNotFound
	}

	row := s.pool.QueryRow(ctx, `
		INSERT INTO maintenance_window
			(window_id, tenant_id, asset_id, starts_at, ends_at, reason, created_by, row_version, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,1,now(),now())
		RETURNING `+maintenanceWindowCols,
		w.WindowID, w.TenantID, w.AssetID, w.StartsAt, w.EndsAt, w.Reason, actor)

	created, err := scanMaintenanceWindow(row)
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("insert maintenance window: %w", err)
	}
	return created, nil
}

// Get returns a window by identifier, or domain.ErrNotFound.
func (s *MaintenanceWindowStore) Get(ctx context.Context, windowID string) (*domain.MaintenanceWindow, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+maintenanceWindowCols+` FROM maintenance_window WHERE window_id = $1`, windowID)
	w, err := scanMaintenanceWindow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get maintenance window: %w", err)
	}
	return w, nil
}

// List returns a page of the caller's windows, keyset-paginated over window_id.
func (s *MaintenanceWindowStore) List(ctx context.Context, limit int, after string) ([]*domain.MaintenanceWindow, error) {
	limit = clampMaintenanceWindowPage(limit)
	rows, err := s.pool.Query(ctx, `
		SELECT `+maintenanceWindowCols+`
		  FROM maintenance_window
		 WHERE ($1 = '' OR window_id > $1)
		 ORDER BY window_id
		 LIMIT $2`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list maintenance windows: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.MaintenanceWindow, 0, limit)
	for rows.Next() {
		w, err := scanMaintenanceWindow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan maintenance window: %w", err)
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate maintenance windows: %w", err)
	}
	return out, nil
}

// Delete removes a window, or returns domain.ErrNotFound.
func (s *MaintenanceWindowStore) Delete(ctx context.Context, windowID string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM maintenance_window WHERE window_id = $1`, windowID)
	if err != nil {
		return fmt.Errorf("delete maintenance window: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// E3.3a evaluator surface (alerting.MaintenanceWindowChecker): the ONLY
// method on this type meant to be called from a *MaintenanceWindowStore
// built over the PRIVILEGED pool — the same dual-role split AlertRuleStore/
// alerting.Store and IncidentStore/alerting.IncidentCorrelator already use.
// This connection has row-level security switched off (ADR-TENANCY-002), so
// this statement carries an explicit tenant_id predicate, sourced from the
// caller's own alert_rule row via the evaluator (ADR-TENANCY-012) — never
// assumed from RLS.
var _ alerting.MaintenanceWindowChecker = (*MaintenanceWindowStore)(nil)

// Suppress implements alerting.MaintenanceWindowChecker: reports whether an
// ACTIVE window (at ∈ [starts_at, ends_at), half-open — ADR-ALERTING-002 §3)
// covers (tenantID, assetID) at `at` and, if one does, atomically increments
// its suppressed_count/last_suppressed_at in the SAME statement.
//
// Atomicity is the database's, not Go's: the SELECT ... FOR UPDATE inside
// the CTE serialises a concurrent Suppress call racing on the same window
// (two different alert rules on the same asset firing in the same
// evaluation tick, from different goroutines) behind whichever transaction
// gets there first — the identical pattern
// IncidentStore.FindOrCreateOpenAlertIncident already uses for the same
// class of race (E4.1).
func (s *MaintenanceWindowStore) Suppress(
	ctx context.Context, tenantID, assetID string, at time.Time,
) (string, bool, error) {
	row := s.pool.QueryRow(ctx, `
		WITH active AS (
		    SELECT window_id
		      FROM maintenance_window
		     WHERE tenant_id = $1 AND asset_id = $2
		       AND starts_at <= $3 AND ends_at > $3
		     ORDER BY window_id
		     LIMIT 1
		     FOR UPDATE
		)
		UPDATE maintenance_window w
		   SET suppressed_count = suppressed_count + 1, last_suppressed_at = $3, updated_at = now()
		  FROM active a
		 WHERE w.window_id = a.window_id
		RETURNING w.window_id`, tenantID, assetID, at)

	var windowID string
	if err := row.Scan(&windowID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("check/record maintenance window suppression: %w", err)
	}
	return windowID, true, nil
}
