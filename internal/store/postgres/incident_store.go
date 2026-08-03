package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
)

const (
	incidentColumns = `incident_id, tenant_id, title, description, severity, status,
		asset_id, assignee_user_id, row_version, created_at, updated_at,
		acknowledged_at, resolved_at, closed_at`
	incidentEventColumns = `event_id, tenant_id, incident_id, kind, field, old_value, new_value, actor, row_version, occurred_at`
)

// defaultIncidentPageSize/maxIncidentPageSize bound an unbounded List, the
// same shape every other tenant-owned store in this package uses.
const (
	defaultIncidentPageSize = 50
	maxIncidentPageSize     = 500
)

// IncidentStore administers Incidents: the work item, its lifecycle, its
// assignment, and its append-only timeline (E5.1).
//
// incident and incident_event are TENANT-OWNED — both are in
// postgres.TenantOwnedTables and carry row-level security — so this store is
// built from the tenant-scoped pool and takes no tenant argument anywhere:
// the bound connection is the boundary, exactly like AssetStore.
type IncidentStore struct {
	pool *pgxpool.Pool
}

// NewIncidentStore builds the incident repository over the given pool.
func NewIncidentStore(pool *pgxpool.Pool) *IncidentStore {
	return &IncidentStore{pool: pool}
}

var _ domain.IncidentRepository = (*IncidentStore)(nil)

func clampIncidentPage(limit int) int {
	if limit <= 0 {
		return defaultIncidentPageSize
	}
	if limit > maxIncidentPageSize {
		return maxIncidentPageSize
	}
	return limit
}

func scanIncident(s scanner) (*domain.Incident, error) {
	var (
		inc      domain.Incident
		severity string
		status   string
	)
	if err := s.Scan(
		&inc.IncidentID, &inc.TenantID, &inc.Title, &inc.Description, &severity, &status,
		&inc.AssetID, &inc.AssigneeUserID, &inc.RowVersion, &inc.CreatedAt, &inc.UpdatedAt,
		&inc.AcknowledgedAt, &inc.ResolvedAt, &inc.ClosedAt,
	); err != nil {
		return nil, err
	}
	inc.Severity = domain.IncidentSeverity(severity)
	inc.Status = domain.IncidentStatus(status)
	return &inc, nil
}

func scanIncidentEvent(s scanner) (*domain.IncidentEvent, error) {
	var (
		e    domain.IncidentEvent
		kind string
	)
	if err := s.Scan(
		&e.EventID, &e.TenantID, &e.IncidentID, &kind, &e.Field,
		&e.OldValue, &e.NewValue, &e.Actor, &e.RowVersion, &e.OccurredAt,
	); err != nil {
		return nil, err
	}
	e.Kind = domain.IncidentEventKind(kind)
	return &e, nil
}

// getForUpdateTx reads incidentID's current row under FOR UPDATE, inside tx,
// so the caller holds the row lock for the rest of its transaction — this
// table's equivalent of ADR-AUDIT-006's chain-head serialisation, since a
// timeline row is written from exactly this read in the same transaction.
// Mirrors AssetStore.getForUpdateTx exactly.
func (s *IncidentStore) getForUpdateTx(ctx context.Context, tx pgx.Tx, incidentID string) (*domain.Incident, error) {
	row := tx.QueryRow(ctx, `SELECT `+incidentColumns+` FROM incident WHERE incident_id = $1 FOR UPDATE`, incidentID)
	inc, err := scanIncident(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get incident for update: %w", err)
	}
	return inc, nil
}

// verifyAssetExists confirms assetID names a Configuration Item visible to
// this store's own tenant-scoped, RLS-enforced connection before
// incident.asset_id is written — mirrors AssetStore.verifyOwnerTeamExists,
// applied here to Incident's linked CI instead of Asset's owning team.
func (s *IncidentStore) verifyAssetExists(ctx context.Context, assetID string) error {
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM asset WHERE asset_id = $1)`, assetID,
	).Scan(&exists); err != nil {
		return fmt.Errorf("check linked asset exists: %w", err)
	}
	if !exists {
		return domain.ErrNotFound
	}
	return nil
}

// verifyAssigneeExists confirms userID is a person the caller's tenant
// actually knows — via an ACTIVE membership row, not app_user's own table,
// which carries no tenant_id at all (ADR-IDENTITY-002 §3.1). Mirrors
// AssetStore.verifyOwnerUserExists exactly, applied to an assignee instead
// of an owner.
func (s *IncidentStore) verifyAssigneeExists(ctx context.Context, userID string) error {
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM membership WHERE user_id = $1 AND status = 'active')`, userID,
	).Scan(&exists); err != nil {
		return fmt.Errorf("check assignee exists: %w", err)
	}
	if !exists {
		return domain.ErrNotFound
	}
	return nil
}

// recordEvent appends one incident_event row inside tx, so it commits or
// aborts with the mutation it describes. Mirrors AssetStore.recordChange.
func (s *IncidentStore) recordEvent(
	ctx context.Context, tx pgx.Tx, tenantID, incidentID string,
	kind domain.IncidentEventKind, field string, oldValue, newValue *string,
	actor string, rowVersion int64,
) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO incident_event
			(event_id, tenant_id, incident_id, kind, field, old_value, new_value, actor, row_version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		domain.NewID(), tenantID, incidentID, string(kind), field, oldValue, newValue, actor, rowVersion)
	if err != nil {
		return fmt.Errorf("record incident event: %w", err)
	}
	return nil
}

// Create inserts an incident and records the single IncidentEventCreated
// timeline row in the same transaction. When inc.AssetID/inc.AssigneeUserID
// is set, both are re-verified against this store's own tenant-scoped
// connection before the INSERT runs.
func (s *IncidentStore) Create(ctx context.Context, inc *domain.Incident) (*domain.Incident, error) {
	if err := inc.Validate(); err != nil {
		return nil, err
	}
	actor, ok := domain.ActorFrom(ctx)
	if !ok {
		return nil, domain.ErrNoActor
	}
	if inc.AssetID != nil {
		if err := s.verifyAssetExists(ctx, *inc.AssetID); err != nil {
			return nil, err
		}
	}
	if inc.AssigneeUserID != nil {
		if err := s.verifyAssigneeExists(ctx, *inc.AssigneeUserID); err != nil {
			return nil, err
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row := tx.QueryRow(ctx, `
		INSERT INTO incident (incident_id, tenant_id, title, description, severity, status, asset_id, assignee_user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING `+incidentColumns,
		inc.IncidentID, inc.TenantID, inc.Title, inc.Description, string(inc.Severity), string(inc.Status),
		inc.AssetID, inc.AssigneeUserID)

	created, err := scanIncident(row)
	if err != nil {
		if isForeignKeyViolation(err) {
			// Belt-and-suspenders against a TOCTOU between the existence
			// checks above and this INSERT — the same defensive branch
			// AssetStore.insertAssetRowTx takes for its own owner references.
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("insert incident: %w", err)
	}
	if err := s.recordEvent(ctx, tx, created.TenantID, created.IncidentID,
		domain.IncidentEventCreated, "", nil, nil, actor, created.RowVersion); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return created, nil
}

// Get returns an incident by identifier.
func (s *IncidentStore) Get(ctx context.Context, incidentID string) (*domain.Incident, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+incidentColumns+` FROM incident WHERE incident_id = $1`, incidentID)
	inc, err := scanIncident(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get incident: %w", err)
	}
	return inc, nil
}

// List returns a page of the caller's incidents, keyset-paginated over
// incident_id — a ULID, and therefore ordered by creation. Unlike
// AssetRepository.List, there is no default status exclusion: a closed
// incident remains operationally relevant history, not noise to hide.
func (s *IncidentStore) List(ctx context.Context, limit int, after string, status domain.IncidentStatus) ([]*domain.Incident, error) {
	limit = clampIncidentPage(limit)
	if status != "" && !status.Valid() {
		return nil, domain.NewValidationError("status",
			"must be one of: open, acknowledged, investigating, resolved, closed, reopened")
	}

	var (
		rows pgx.Rows
		err  error
	)
	if status == "" {
		rows, err = s.pool.Query(ctx, `
			SELECT `+incidentColumns+`
			  FROM incident
			 WHERE ($1 = '' OR incident_id > $1)
			 ORDER BY incident_id
			 LIMIT $2`, after, limit)
	} else {
		rows, err = s.pool.Query(ctx, `
			SELECT `+incidentColumns+`
			  FROM incident
			 WHERE status = $1 AND ($2 = '' OR incident_id > $2)
			 ORDER BY incident_id
			 LIMIT $3`, string(status), after, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("list incidents: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.Incident, 0, limit)
	for rows.Next() {
		inc, err := scanIncident(rows)
		if err != nil {
			return nil, fmt.Errorf("scan incident: %w", err)
		}
		out = append(out, inc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate incidents: %w", err)
	}
	return out, nil
}

// Update changes one or more non-lifecycle, non-assignee fields of patch
// under optimistic locking. Writes NO timeline row — see
// domain.IncidentEventKind's doc comment for why the timeline is narrower
// than asset_change_history's field-audit shape. The pre-image is read under
// FOR UPDATE inside the same transaction as the write, mirroring
// AssetStore.Update's identical reasoning.
func (s *IncidentStore) Update(
	ctx context.Context, incidentID string, rowVersion int64, patch domain.IncidentPatch,
) (*domain.Incident, error) {
	if _, ok := domain.ActorFrom(ctx); !ok {
		return nil, domain.ErrNoActor
	}

	var titlePtr *string
	if patch.Title != nil {
		trimmed := strings.TrimSpace(*patch.Title)
		if trimmed == "" {
			return nil, domain.NewValidationError("title", "must not be empty")
		}
		if len(trimmed) > domain.MaxIncidentTitleLength {
			return nil, domain.NewValidationError("title", "must be at most 200 characters")
		}
		titlePtr = &trimmed
	}

	var descPtr *string
	if patch.Description != nil {
		if len(*patch.Description) > domain.MaxIncidentDescriptionLength {
			return nil, domain.NewValidationError("description", "must be at most 10000 characters")
		}
		descPtr = patch.Description
	}

	var severityPtr *string
	if patch.Severity != nil {
		if !patch.Severity.Valid() {
			return nil, domain.NewValidationError("severity", "must be one of: critical, high, medium, low")
		}
		v := string(*patch.Severity)
		severityPtr = &v
	}

	// touchAsset is false when the patch field is nil ("leave unchanged");
	// true with a nil value clears the link; true with a non-nil value sets
	// it, re-verified first — the same tri-state shape Asset.OwnerTeamID
	// uses.
	touchAsset := patch.AssetID != nil
	var assetValue *string
	if touchAsset {
		if trimmed := strings.TrimSpace(*patch.AssetID); trimmed != "" {
			if err := s.verifyAssetExists(ctx, trimmed); err != nil {
				return nil, err
			}
			assetValue = &trimmed
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	current, err := s.getForUpdateTx(ctx, tx, incidentID)
	if err != nil {
		return nil, err
	}
	if current.RowVersion != rowVersion {
		return nil, domain.ErrVersionMismatch
	}

	row := tx.QueryRow(ctx, `
		UPDATE incident
		   SET title       = COALESCE($3, title),
		       description = COALESCE($4, description),
		       severity    = COALESCE($5, severity),
		       asset_id    = CASE WHEN $6 THEN $7 ELSE asset_id END,
		       row_version = row_version + 1,
		       updated_at  = now()
		 WHERE incident_id = $1 AND row_version = $2
		RETURNING `+incidentColumns,
		incidentID, rowVersion, titlePtr, descPtr, severityPtr, touchAsset, assetValue)

	updated, err := scanIncident(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, s.explainMissedUpdate(ctx, incidentID)
	}
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("update incident: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return updated, nil
}

// SetStatus performs a lifecycle transition under optimistic locking, guarded
// by IncidentStatus.CanTransitionTo — the single authority for status
// changes (mirrors AssetStore.SetStatus). An illegal move returns
// *IncidentTransitionError (ErrInvalidTransition). Records one
// IncidentEventStatusTransitioned timeline row in the same transaction as
// the UPDATE, and sets AcknowledgedAt/ResolvedAt/ClosedAt when the
// corresponding state is entered.
func (s *IncidentStore) SetStatus(
	ctx context.Context, incidentID string, rowVersion int64, status domain.IncidentStatus,
) (*domain.Incident, error) {
	if !status.Valid() {
		return nil, domain.NewValidationError("status",
			"must be one of: open, acknowledged, investigating, resolved, closed, reopened")
	}
	actor, ok := domain.ActorFrom(ctx)
	if !ok {
		return nil, domain.ErrNoActor
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	current, err := s.getForUpdateTx(ctx, tx, incidentID)
	if err != nil {
		return nil, err
	}
	if current.RowVersion != rowVersion {
		return nil, domain.ErrVersionMismatch
	}
	if !current.Status.CanTransitionTo(status) {
		return nil, domain.NewIncidentTransitionError(current.Status, status)
	}

	// acknowledged_at/resolved_at/closed_at are overwritten (not latched) on
	// re-entry — see domain.Incident's doc comment. Every other transition
	// leaves all three untouched.
	var setClause string
	switch status {
	case domain.IncidentAcknowledged:
		setClause = `, acknowledged_at = now()`
	case domain.IncidentResolved:
		setClause = `, resolved_at = now()`
	case domain.IncidentClosed:
		setClause = `, closed_at = now()`
	}

	row := tx.QueryRow(ctx, `
		UPDATE incident
		   SET status = $3, row_version = row_version + 1, updated_at = now()`+setClause+`
		 WHERE incident_id = $1 AND row_version = $2
		RETURNING `+incidentColumns,
		incidentID, rowVersion, string(status))

	updated, err := scanIncident(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, s.explainMissedUpdate(ctx, incidentID)
	}
	if err != nil {
		return nil, fmt.Errorf("set incident status: %w", err)
	}

	oldVal, newVal := string(current.Status), string(status)
	if err := s.recordEvent(ctx, tx, updated.TenantID, incidentID,
		domain.IncidentEventStatusTransitioned, "status", &oldVal, &newVal, actor, updated.RowVersion); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return updated, nil
}

// Assign changes the incident's assignee under optimistic locking — the
// single authority for that field, mirroring SetStatus's exclusivity from
// Update. assigneeUserID nil clears the assignment; non-nil is re-verified
// via an ACTIVE membership row before being written. Records one
// IncidentEventAssigned timeline row in the same transaction as the UPDATE.
func (s *IncidentStore) Assign(
	ctx context.Context, incidentID string, rowVersion int64, assigneeUserID *string,
) (*domain.Incident, error) {
	actor, ok := domain.ActorFrom(ctx)
	if !ok {
		return nil, domain.ErrNoActor
	}
	var normalized *string
	if assigneeUserID != nil {
		trimmed := strings.TrimSpace(*assigneeUserID)
		if trimmed == "" {
			return nil, domain.NewValidationError("assignee_user_id",
				"must not be blank when supplied; omit it to clear the assignment")
		}
		if err := s.verifyAssigneeExists(ctx, trimmed); err != nil {
			return nil, err
		}
		normalized = &trimmed
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	current, err := s.getForUpdateTx(ctx, tx, incidentID)
	if err != nil {
		return nil, err
	}
	if current.RowVersion != rowVersion {
		return nil, domain.ErrVersionMismatch
	}

	row := tx.QueryRow(ctx, `
		UPDATE incident
		   SET assignee_user_id = $3, row_version = row_version + 1, updated_at = now()
		 WHERE incident_id = $1 AND row_version = $2
		RETURNING `+incidentColumns,
		incidentID, rowVersion, normalized)

	updated, err := scanIncident(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, s.explainMissedUpdate(ctx, incidentID)
	}
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("assign incident: %w", err)
	}

	if !strPtrEqual(current.AssigneeUserID, normalized) {
		if err := s.recordEvent(ctx, tx, updated.TenantID, incidentID,
			domain.IncidentEventAssigned, "assignee_user_id", current.AssigneeUserID, normalized,
			actor, updated.RowVersion); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return updated, nil
}

// Timeline returns a page of incidentID's append-only history,
// keyset-paginated over event_id — a ULID, and therefore chronological
// ascending, the same shape AssetStore.History uses.
func (s *IncidentStore) Timeline(
	ctx context.Context, incidentID string, limit int, after string,
) ([]*domain.IncidentEvent, error) {
	limit = clampIncidentPage(limit)

	rows, err := s.pool.Query(ctx, `
		SELECT `+incidentEventColumns+`
		  FROM incident_event
		 WHERE incident_id = $1 AND ($2 = '' OR event_id > $2)
		 ORDER BY event_id
		 LIMIT $3`, incidentID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list incident timeline: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.IncidentEvent, 0, limit)
	for rows.Next() {
		e, err := scanIncidentEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan incident event: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate incident timeline: %w", err)
	}
	return out, nil
}

// explainMissedUpdate distinguishes "gone" from "stale version" after an
// UPDATE matched no row, so the caller gets 404 or 409 rather than one
// ambiguous failure. Mirrors AssetStore.explainMissedUpdate.
func (s *IncidentStore) explainMissedUpdate(ctx context.Context, incidentID string) error {
	if _, err := s.Get(ctx, incidentID); err != nil {
		return err
	}
	return domain.ErrVersionMismatch
}
