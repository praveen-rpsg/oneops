package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
)

const (
	defaultOnCallSchedulePageSize    = 50
	maxOnCallSchedulePageSize        = 500
	defaultOnCallParticipantPageSize = 50
	maxOnCallParticipantPageSize     = 500
)

const onCallScheduleCols = `schedule_id, tenant_id, name, handoff_interval_seconds, rotation_start_at,
	status, row_version, created_at, updated_at`

const onCallParticipantCols = `participant_id, tenant_id, schedule_id, user_id, position,
	row_version, created_at, updated_at`

// OnCallScheduleStore administers on_call_schedule/on_call_participant
// (E5.2a).
//
// Both tables are TENANT-OWNED — they are in TenantOwnedTables and carry
// row-level security — so this store is built from the tenant-scoped pool
// and takes no tenant argument anywhere: the bound connection already is the
// boundary (ADR-TENANCY-002), exactly like TeamStore. Unlike
// AlertRuleStore/MaintenanceWindowStore, there is no privileged-pool second
// role here: nothing in E5.2a runs as a cross-tenant background worker (see
// domain.OnCallScheduleRepository's doc comment).
type OnCallScheduleStore struct {
	pool *pgxpool.Pool
}

// NewOnCallScheduleStore builds the store over the given pool.
func NewOnCallScheduleStore(pool *pgxpool.Pool) *OnCallScheduleStore {
	return &OnCallScheduleStore{pool: pool}
}

var _ domain.OnCallScheduleRepository = (*OnCallScheduleStore)(nil)

func clampOnCallSchedulePage(limit int) int {
	if limit <= 0 {
		return defaultOnCallSchedulePageSize
	}
	if limit > maxOnCallSchedulePageSize {
		return maxOnCallSchedulePageSize
	}
	return limit
}

func clampOnCallParticipantPage(limit int) int {
	if limit <= 0 {
		return defaultOnCallParticipantPageSize
	}
	if limit > maxOnCallParticipantPageSize {
		return maxOnCallParticipantPageSize
	}
	return limit
}

func scanOnCallSchedule(sc scanner) (*domain.OnCallSchedule, error) {
	var (
		s      domain.OnCallSchedule
		status string
	)
	if err := sc.Scan(
		&s.ScheduleID, &s.TenantID, &s.Name, &s.HandoffIntervalSeconds, &s.RotationStartAt,
		&status, &s.RowVersion, &s.CreatedAt, &s.UpdatedAt,
	); err != nil {
		return nil, err
	}
	s.Status = domain.OnCallScheduleStatus(status)
	return &s, nil
}

func scanOnCallParticipant(sc scanner) (*domain.OnCallParticipant, error) {
	var p domain.OnCallParticipant
	if err := sc.Scan(
		&p.ParticipantID, &p.TenantID, &p.ScheduleID, &p.UserID, &p.Position,
		&p.RowVersion, &p.CreatedAt, &p.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &p, nil
}

// Create inserts a schedule.
func (s *OnCallScheduleStore) Create(ctx context.Context, sch *domain.OnCallSchedule) (*domain.OnCallSchedule, error) {
	if err := sch.Validate(); err != nil {
		return nil, err
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO on_call_schedule
			(schedule_id, tenant_id, name, handoff_interval_seconds, rotation_start_at, status, row_version, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,1,now(),now())
		RETURNING `+onCallScheduleCols,
		sch.ScheduleID, sch.TenantID, sch.Name, sch.HandoffIntervalSeconds, sch.RotationStartAt, string(sch.Status))

	created, err := scanOnCallSchedule(row)
	if err != nil {
		return nil, fmt.Errorf("insert on-call schedule: %w", err)
	}
	return created, nil
}

// Get returns a schedule by identifier, or domain.ErrNotFound.
func (s *OnCallScheduleStore) Get(ctx context.Context, scheduleID string) (*domain.OnCallSchedule, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+onCallScheduleCols+` FROM on_call_schedule WHERE schedule_id = $1`, scheduleID)
	sch, err := scanOnCallSchedule(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get on-call schedule: %w", err)
	}
	return sch, nil
}

// List returns a page of the caller's schedules, keyset-paginated over
// schedule_id.
func (s *OnCallScheduleStore) List(ctx context.Context, limit int, after string) ([]*domain.OnCallSchedule, error) {
	limit = clampOnCallSchedulePage(limit)
	rows, err := s.pool.Query(ctx, `
		SELECT `+onCallScheduleCols+`
		  FROM on_call_schedule
		 WHERE ($1 = '' OR schedule_id > $1)
		 ORDER BY schedule_id
		 LIMIT $2`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list on-call schedules: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.OnCallSchedule, 0, limit)
	for rows.Next() {
		sch, err := scanOnCallSchedule(rows)
		if err != nil {
			return nil, fmt.Errorf("scan on-call schedule: %w", err)
		}
		out = append(out, sch)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate on-call schedules: %w", err)
	}
	return out, nil
}

// Update changes one or more fields under optimistic locking.
func (s *OnCallScheduleStore) Update(
	ctx context.Context, scheduleID string, rowVersion int64, patch domain.OnCallSchedulePatch,
) (*domain.OnCallSchedule, error) {
	current, err := s.Get(ctx, scheduleID)
	if err != nil {
		return nil, err
	}
	if patch.Name != nil {
		current.Name = *patch.Name
	}
	if patch.HandoffIntervalSeconds != nil {
		current.HandoffIntervalSeconds = *patch.HandoffIntervalSeconds
	}
	if patch.RotationStartAt != nil {
		current.RotationStartAt = *patch.RotationStartAt
	}
	if patch.Status != nil {
		current.Status = *patch.Status
	}
	if err := current.Validate(); err != nil {
		return nil, err
	}

	row := s.pool.QueryRow(ctx, `
		UPDATE on_call_schedule
		   SET name = $3, handoff_interval_seconds = $4, rotation_start_at = $5, status = $6,
		       row_version = row_version + 1, updated_at = now()
		 WHERE schedule_id = $1 AND row_version = $2
		RETURNING `+onCallScheduleCols,
		scheduleID, rowVersion, current.Name, current.HandoffIntervalSeconds, current.RotationStartAt, string(current.Status))

	updated, err := scanOnCallSchedule(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, s.explainMissedUpdate(ctx, scheduleID)
	}
	if err != nil {
		return nil, fmt.Errorf("update on-call schedule: %w", err)
	}
	return updated, nil
}

// explainMissedUpdate distinguishes "gone" from "stale version" after an
// UPDATE matched no row — mirrors TeamStore.explainMissedUpdate.
func (s *OnCallScheduleStore) explainMissedUpdate(ctx context.Context, scheduleID string) error {
	if _, err := s.Get(ctx, scheduleID); err != nil {
		return err
	}
	return domain.ErrVersionMismatch
}

// Delete removes a schedule; ON DELETE CASCADE removes its participants with
// it.
func (s *OnCallScheduleStore) Delete(ctx context.Context, scheduleID string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM on_call_schedule WHERE schedule_id = $1`, scheduleID)
	if err != nil {
		return fmt.Errorf("delete on-call schedule: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// verifyActiveMember confirms userID is a person the caller's tenant
// actually knows — via an ACTIVE membership row, not app_user's own table,
// which carries no tenant_id at all (ADR-IDENTITY-002 §3.1). Mirrors
// IncidentStore.verifyAssigneeExists exactly, applied to a participant
// instead of an assignee.
func (s *OnCallScheduleStore) verifyActiveMember(ctx context.Context, tx pgx.Tx, userID string) error {
	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM membership WHERE user_id = $1 AND status = 'active')`, userID,
	).Scan(&exists); err != nil {
		return fmt.Errorf("check participant user exists: %w", err)
	}
	if !exists {
		return domain.ErrNotFound
	}
	return nil
}

// lockScheduleTx takes FOR UPDATE on scheduleID's own row, serialising
// concurrent AddParticipant/RemoveParticipant/ReorderParticipants calls
// against the same schedule — the same lock-the-owning-row technique
// IncidentStore.getForUpdateTx uses before writing its own timeline. It also
// confirms scheduleID exists, returning domain.ErrNotFound otherwise.
func (s *OnCallScheduleStore) lockScheduleTx(ctx context.Context, tx pgx.Tx, scheduleID string) error {
	var exists bool
	err := tx.QueryRow(ctx,
		`SELECT true FROM on_call_schedule WHERE schedule_id = $1 FOR UPDATE`, scheduleID,
	).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock on-call schedule: %w", err)
	}
	return nil
}

// AddParticipant appends userID to scheduleID's roster at the next position.
func (s *OnCallScheduleStore) AddParticipant(
	ctx context.Context, scheduleID, userID string,
) (*domain.OnCallParticipant, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := s.lockScheduleTx(ctx, tx, scheduleID); err != nil {
		return nil, err
	}
	if err := s.verifyActiveMember(ctx, tx, userID); err != nil {
		return nil, err
	}

	var nextPosition int
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(position) + 1, 0) FROM on_call_participant WHERE schedule_id = $1`, scheduleID,
	).Scan(&nextPosition); err != nil {
		return nil, fmt.Errorf("compute next on-call participant position: %w", err)
	}

	p, err := domain.NewOnCallParticipant(scheduleID, domain.TenantIDFrom(ctx), userID, nextPosition)
	if err != nil {
		return nil, err
	}

	row := tx.QueryRow(ctx, `
		INSERT INTO on_call_participant
			(participant_id, tenant_id, schedule_id, user_id, position, row_version, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,1,now(),now())
		RETURNING `+onCallParticipantCols,
		p.ParticipantID, p.TenantID, p.ScheduleID, p.UserID, p.Position)

	added, err := scanOnCallParticipant(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, domain.ErrConflict
		}
		if isForeignKeyViolation(err) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("insert on-call participant: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return added, nil
}

// RemoveParticipant removes one participant and closes the resulting gap in
// position, keeping positions contiguous 0..N-1.
func (s *OnCallScheduleStore) RemoveParticipant(ctx context.Context, scheduleID, participantID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := s.lockScheduleTx(ctx, tx, scheduleID); err != nil {
		return err
	}

	var removedPosition int
	err = tx.QueryRow(ctx,
		`DELETE FROM on_call_participant WHERE participant_id = $1 AND schedule_id = $2 RETURNING position`,
		participantID, scheduleID,
	).Scan(&removedPosition)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("delete on-call participant: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE on_call_participant
		   SET position = position - 1, row_version = row_version + 1, updated_at = now()
		 WHERE schedule_id = $1 AND position > $2`,
		scheduleID, removedPosition); err != nil {
		return fmt.Errorf("compact on-call participant positions: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// ListParticipants returns a page of scheduleID's roster in rotation order,
// keyset-paginated over position itself.
func (s *OnCallScheduleStore) ListParticipants(
	ctx context.Context, scheduleID string, limit int, after string,
) ([]*domain.OnCallParticipant, error) {
	limit = clampOnCallParticipantPage(limit)
	afterPosition := -1
	if after != "" {
		n, err := strconv.Atoi(after)
		if err != nil {
			return nil, domain.NewValidationError("after", "must be an integer position cursor")
		}
		afterPosition = n
	}

	rows, err := s.pool.Query(ctx, `
		SELECT `+onCallParticipantCols+`
		  FROM on_call_participant
		 WHERE schedule_id = $1 AND position > $2
		 ORDER BY position
		 LIMIT $3`, scheduleID, afterPosition, limit)
	if err != nil {
		return nil, fmt.Errorf("list on-call participants: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.OnCallParticipant, 0, limit)
	for rows.Next() {
		p, err := scanOnCallParticipant(rows)
		if err != nil {
			return nil, fmt.Errorf("scan on-call participant: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate on-call participants: %w", err)
	}
	return out, nil
}

// ReorderParticipants atomically replaces scheduleID's rotation order.
//
// participantIDs must be exactly the schedule's current participant set, in
// the desired new order. The set-equality check happens inside the same
// transaction that holds the schedule row lock (lockScheduleTx), so a
// concurrent Add/Remove cannot race between the check and the write.
//
// uq_on_call_participant_schedule_position is DEFERRABLE INITIALLY
// DEFERRED (20260901000001), so the per-row UPDATEs below may transiently
// pass through a duplicate position (e.g. swapping positions 0 and 1) —
// PostgreSQL checks the constraint at COMMIT, once every row has its final,
// non-colliding position — see that migration's own comment for why a
// non-deferred constraint would reject this.
func (s *OnCallScheduleStore) ReorderParticipants(
	ctx context.Context, scheduleID string, participantIDs []string,
) ([]*domain.OnCallParticipant, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := s.lockScheduleTx(ctx, tx, scheduleID); err != nil {
		return nil, err
	}

	rows, err := tx.Query(ctx, `SELECT participant_id FROM on_call_participant WHERE schedule_id = $1`, scheduleID)
	if err != nil {
		return nil, fmt.Errorf("read on-call participants for reorder: %w", err)
	}
	current := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan on-call participant id: %w", err)
		}
		current[id] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate on-call participant ids: %w", err)
	}
	rows.Close()

	if len(participantIDs) != len(current) {
		return nil, domain.NewValidationError("participant_ids",
			"must name exactly the schedule's current participants, no more, no fewer")
	}
	seen := make(map[string]bool, len(participantIDs))
	for _, id := range participantIDs {
		if !current[id] {
			return nil, domain.NewValidationError("participant_ids",
				fmt.Sprintf("%q is not a participant of this schedule", id))
		}
		if seen[id] {
			return nil, domain.NewValidationError("participant_ids", fmt.Sprintf("%q is repeated", id))
		}
		seen[id] = true
	}

	for i, id := range participantIDs {
		if _, err := tx.Exec(ctx, `
			UPDATE on_call_participant
			   SET position = $3, row_version = row_version + 1, updated_at = now()
			 WHERE participant_id = $1 AND schedule_id = $2`,
			id, scheduleID, i); err != nil {
			return nil, fmt.Errorf("reorder on-call participant: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit reorder: %w", err)
	}
	return s.ListParticipants(ctx, scheduleID, len(participantIDs), "")
}

// OnCall answers "who is on call for scheduleID at `at`" — see
// domain.OnCallScheduleRepository.OnCall's doc comment. This is a pure read:
// nothing is written. display_name is resolved from app_user, a global
// table outside row-level security (ADR-IDENTITY-002 §3.1) that
// oneops_app has ordinary SELECT on, purely for display — the same
// class of read verifyActiveMember already performs against membership.
func (s *OnCallScheduleStore) OnCall(ctx context.Context, scheduleID string, at time.Time) (*domain.OnCallResolution, error) {
	sch, err := s.Get(ctx, scheduleID)
	if err != nil {
		return nil, err
	}

	rows, err := s.pool.Query(ctx, `
		SELECT p.user_id, u.display_name
		  FROM on_call_participant p
		  LEFT JOIN app_user u ON u.user_id = p.user_id
		 WHERE p.schedule_id = $1
		 ORDER BY p.position`, scheduleID)
	if err != nil {
		return nil, fmt.Errorf("read on-call roster: %w", err)
	}
	defer rows.Close()

	var userIDs, displayNames []string
	for rows.Next() {
		var userID string
		var displayName *string
		if err := rows.Scan(&userID, &displayName); err != nil {
			return nil, fmt.Errorf("scan on-call roster row: %w", err)
		}
		userIDs = append(userIDs, userID)
		if displayName != nil {
			displayNames = append(displayNames, *displayName)
		} else {
			displayNames = append(displayNames, "")
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate on-call roster: %w", err)
	}

	res := &domain.OnCallResolution{ScheduleID: scheduleID, At: at}
	idx, ok := domain.OnCallRotationIndex(len(userIDs), sch.RotationStartAt, sch.HandoffIntervalSeconds, at)
	if !ok {
		return res, nil
	}
	userID := userIDs[idx]
	res.UserID = &userID
	if displayNames[idx] != "" {
		dn := displayNames[idx]
		res.DisplayName = &dn
	}
	return res, nil
}
