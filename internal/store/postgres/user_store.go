package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
)

const userColumns = `user_id, email, display_name, status, row_version, created_at, updated_at`

// defaultUserPageSize bounds an unbounded List. A caller that asks for
// everything gets a page, not the whole table.
const defaultUserPageSize = 50

// maxUserPageSize caps what a caller may ask for.
const maxUserPageSize = 500

// UserStore is the PostgreSQL implementation of domain.UserRepository.
//
// app_user is GLOBAL: it carries no tenant_id, is not in TenantOwnedTables, and
// is not under row-level security (ADR-IDENTITY-002 §3). Nothing here takes a
// tenant, and nothing here may be scoped by one — a person exists before, and
// independently of, any membership.
type UserStore struct {
	pool *pgxpool.Pool
}

// NewUserStore builds a user repository over the given pool.
func NewUserStore(pool *pgxpool.Pool) *UserStore {
	return &UserStore{pool: pool}
}

var _ domain.UserRepository = (*UserStore)(nil)

func scanUser(s scanner) (*domain.User, error) {
	var (
		u      domain.User
		status string
	)
	if err := s.Scan(
		&u.UserID, &u.Email, &u.DisplayName, &status,
		&u.RowVersion, &u.CreatedAt, &u.UpdatedAt,
	); err != nil {
		return nil, err
	}
	u.Status = domain.UserStatus(status)
	return &u, nil
}

// Create inserts a user. A duplicate email is a conflict, not a server error:
// the address is caller-supplied and unique platform-wide, and uq_user_email is
// case-insensitive because the column is citext — so 'A@b.com' collides with
// 'a@b.com' and the caller is told so rather than getting a second account.
// The write runs inside withAdminAudit: the row and its administrative audit
// record commit together or neither does (ADR-AUDIT-007 §6.9). app_user is
// global and has no organisation, so the act lands on the platform chain.
func (s *UserStore) Create(ctx context.Context, u *domain.User) (*domain.User, error) {
	var created *domain.User
	err := withAdminAudit(ctx, s.pool,
		func() []domain.AdminAct {
			return []domain.AdminAct{{
				Operation: domain.AdminUserCreated,
				Subject:   domain.AdminSubject{UserID: u.UserID},
				Detail:    map[string]any{"email": string(domain.NormalizeEmail(u.Email))},
			}}
		},
		func(tx pgx.Tx) error {
			row := tx.QueryRow(ctx, `
				INSERT INTO app_user (user_id, email, display_name, status)
				VALUES ($1, $2, $3, $4)
				RETURNING `+userColumns,
				u.UserID, domain.NormalizeEmail(u.Email), u.DisplayName, string(u.Status))

			var scanErr error
			created, scanErr = scanUser(row)
			if scanErr != nil {
				if isUniqueViolation(scanErr) {
					return domain.ErrConflict
				}
				return fmt.Errorf("insert user: %w", scanErr)
			}
			return nil
		})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// Get returns a user by platform identifier.
func (s *UserStore) Get(ctx context.Context, userID string) (*domain.User, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+userColumns+` FROM app_user WHERE user_id = $1`, userID)
	u, err := scanUser(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	return u, nil
}

// GetByEmail resolves an address case-insensitively.
//
// The comparison is written against lower(email::text) rather than relying on
// citext's operator. citext's `=` lives in the extension's schema and is found
// through search_path — and an operator cannot be schema-qualified — so a
// deployment whose search_path omits that schema silently gets case-sensitive
// text equality and no error. Uniqueness is unaffected either way (the index
// compares citext to citext internally); lookups are not. Writing the fold
// explicitly makes this query correct under any search_path.
func (s *UserStore) GetByEmail(ctx context.Context, email string) (*domain.User, error) {
	normalized := domain.NormalizeEmail(email)
	if normalized == "" {
		// An empty address matches nothing. Querying for it would scan the table
		// to return nothing, and any match would be a bug.
		return nil, domain.ErrNotFound
	}
	row := s.pool.QueryRow(ctx,
		`SELECT `+userColumns+` FROM app_user WHERE lower(email::text) = $1`, normalized)
	u, err := scanUser(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get user by email: %w", err)
	}
	return u, nil
}

// List returns a page of users ordered by user_id, which is a ULID and
// therefore lexicographically ordered by creation time. Keyset pagination is
// used rather than OFFSET so a page does not shift under concurrent inserts.
func (s *UserStore) List(ctx context.Context, limit int, after string) ([]*domain.User, error) {
	if limit <= 0 {
		limit = defaultUserPageSize
	}
	if limit > maxUserPageSize {
		limit = maxUserPageSize
	}

	rows, err := s.pool.Query(ctx, `
		SELECT `+userColumns+`
		  FROM app_user
		 WHERE ($1 = '' OR user_id > $1)
		 ORDER BY user_id
		 LIMIT $2`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	users := make([]*domain.User, 0, limit)
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate users: %w", err)
	}
	return users, nil
}

// UpdateProfile changes descriptive fields under optimistic locking.
//
// It cannot change status. Lifecycle moves go through SetStatus, which is the
// one place a transition is checked — two paths that both write status would be
// two answers to what a legal move is.
func (s *UserStore) UpdateProfile(
	ctx context.Context, userID string, rowVersion int64, displayName string,
) (*domain.User, error) {
	if len(displayName) > domain.MaxDisplayNameLength {
		return nil, domain.NewValidationError("display_name", "must be at most 200 characters")
	}

	row := s.pool.QueryRow(ctx, `
		UPDATE app_user
		   SET display_name = $3, row_version = row_version + 1, updated_at = now()
		 WHERE user_id = $1 AND row_version = $2
		RETURNING `+userColumns,
		userID, rowVersion, displayName)

	u, err := scanUser(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, s.explainMissedUpdate(ctx, userID)
	}
	if err != nil {
		return nil, fmt.Errorf("update user profile: %w", err)
	}
	return u, nil
}

// SetStatus performs a lifecycle transition under optimistic locking.
//
// The transition is checked against the state the database currently holds, not
// against one the caller asserts: a caller that read the user, then had it
// deactivated underneath them, must not be able to revive it by supplying the
// old state. The row-version guard makes that read-then-write safe.
func (s *UserStore) SetStatus(
	ctx context.Context, userID string, rowVersion int64, status domain.UserStatus,
) (*domain.User, error) {
	if !status.Valid() {
		return nil, domain.NewValidationError("status",
			"must be one of: invited, active, suspended, deactivated")
	}

	current, err := s.Get(ctx, userID)
	if err != nil {
		return nil, err
	}
	if current.RowVersion != rowVersion {
		return nil, domain.ErrVersionMismatch
	}
	if !current.Status.CanTransitionTo(status) {
		return nil, domain.NewTransitionError(current.Status, status)
	}

	row := s.pool.QueryRow(ctx, `
		UPDATE app_user
		   SET status = $3, row_version = row_version + 1, updated_at = now()
		 WHERE user_id = $1 AND row_version = $2
		RETURNING `+userColumns,
		userID, rowVersion, string(status))

	u, err := scanUser(row)
	if errors.Is(err, pgx.ErrNoRows) {
		// Lost the race between the check above and this write.
		return nil, s.explainMissedUpdate(ctx, userID)
	}
	if err != nil {
		return nil, fmt.Errorf("set user status: %w", err)
	}
	return u, nil
}

// explainMissedUpdate distinguishes "gone" from "stale version" after an UPDATE
// matched no row, so the caller gets 404 or 409 rather than one ambiguous
// failure. This mirrors TenantStore.SetStatus.
func (s *UserStore) explainMissedUpdate(ctx context.Context, userID string) error {
	if _, err := s.Get(ctx, userID); err != nil {
		return err
	}
	return domain.ErrVersionMismatch
}
