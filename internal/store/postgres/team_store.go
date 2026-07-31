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
	teamColumns           = `team_id, tenant_id, org_id, name, status, row_version, created_at, updated_at`
	teamMembershipColumns = `team_membership_id, tenant_id, team_id, user_id, row_version, created_at, updated_at`
)

// defaultTeamPageSize bounds an unbounded List; maxTeamPageSize caps what a
// caller may ask for. Same shape as the other identity stores.
const (
	defaultTeamPageSize = 50
	maxTeamPageSize     = 500
)

// TeamStore administers teams and their membership.
//
// team and team_membership are TENANT-OWNED — both are in TenantOwnedTables
// and carry row-level security — so this store is built from the
// tenant-scoped pool and takes no tenant argument anywhere. The bound
// connection is the boundary: a caller cannot name another tenant's team,
// because the policy makes the row invisible rather than because a predicate
// remembered to exclude it (ADR-TENANCY-002).
//
// Mutations here do NOT pass through withAdminAudit — see the doc comment on
// domain.TeamRepository for why: ADR-AUDIT-007 §6.2 scopes that chokepoint to
// five named identity-governance tables, and Team is not one of them. Team
// still gets everything that IS this store's to give: row-level security,
// tenant scoping, and optimistic locking on every mutation.
type TeamStore struct {
	pool *pgxpool.Pool
}

// NewTeamStore builds the team repository over the given pool.
func NewTeamStore(pool *pgxpool.Pool) *TeamStore {
	return &TeamStore{pool: pool}
}

var _ domain.TeamRepository = (*TeamStore)(nil)

// clampTeamPage bounds a requested page size.
func clampTeamPage(limit int) int {
	if limit <= 0 {
		return defaultTeamPageSize
	}
	if limit > maxTeamPageSize {
		return maxTeamPageSize
	}
	return limit
}

func scanTeam(s scanner) (*domain.Team, error) {
	var (
		t      domain.Team
		status string
	)
	if err := s.Scan(
		&t.TeamID, &t.TenantID, &t.OrgID, &t.Name, &status,
		&t.RowVersion, &t.CreatedAt, &t.UpdatedAt,
	); err != nil {
		return nil, err
	}
	t.Status = domain.TeamStatus(status)
	return &t, nil
}

func scanTeamMembership(s scanner) (*domain.TeamMembership, error) {
	var m domain.TeamMembership
	if err := s.Scan(
		&m.TeamMembershipID, &m.TenantID, &m.TeamID, &m.UserID,
		&m.RowVersion, &m.CreatedAt, &m.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &m, nil
}

// Create inserts a team.
func (s *TeamStore) Create(ctx context.Context, t *domain.Team) (*domain.Team, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO team (team_id, tenant_id, org_id, name, status)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+teamColumns,
		t.TeamID, t.TenantID, t.OrgID, t.Name, string(t.Status))

	created, err := scanTeam(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, domain.ErrConflict
		}
		return nil, fmt.Errorf("insert team: %w", err)
	}
	return created, nil
}

// Get returns a team by identifier.
func (s *TeamStore) Get(ctx context.Context, teamID string) (*domain.Team, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+teamColumns+` FROM team WHERE team_id = $1`, teamID)
	t, err := scanTeam(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get team: %w", err)
	}
	return t, nil
}

// ListByOrg returns a page of an organisation's teams, ordered by team_id — a
// ULID, and therefore ordered by creation. Keyset rather than OFFSET so a page
// does not shift under concurrent inserts.
func (s *TeamStore) ListByOrg(
	ctx context.Context, orgID string, limit int, after string,
) ([]*domain.Team, error) {
	limit = clampTeamPage(limit)

	rows, err := s.pool.Query(ctx, `
		SELECT `+teamColumns+`
		  FROM team
		 WHERE org_id = $1 AND ($2 = '' OR team_id > $2)
		 ORDER BY team_id
		 LIMIT $3`, orgID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.Team, 0, limit)
	for rows.Next() {
		t, err := scanTeam(rows)
		if err != nil {
			return nil, fmt.Errorf("scan team: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate teams: %w", err)
	}
	return out, nil
}

// UpdateProfile renames a team under optimistic locking.
//
// It cannot change status. Lifecycle moves go through SetStatus, which is the
// one place a transition is checked — two paths that both write status would
// be two answers to what a legal move is (the same split UserStore uses).
func (s *TeamStore) UpdateProfile(
	ctx context.Context, teamID string, rowVersion int64, name string,
) (*domain.Team, error) {
	if len(name) > domain.MaxTeamNameLength {
		return nil, domain.NewValidationError("name", "must be at most 200 characters")
	}
	if name == "" {
		return nil, domain.NewValidationError("name", "must not be empty")
	}

	row := s.pool.QueryRow(ctx, `
		UPDATE team
		   SET name = $3, row_version = row_version + 1, updated_at = now()
		 WHERE team_id = $1 AND row_version = $2
		RETURNING `+teamColumns,
		teamID, rowVersion, name)

	t, err := scanTeam(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, s.explainMissedUpdate(ctx, teamID)
	}
	if err != nil {
		return nil, fmt.Errorf("update team profile: %w", err)
	}
	return t, nil
}

// SetStatus archives or reactivates a team under optimistic locking.
func (s *TeamStore) SetStatus(
	ctx context.Context, teamID string, rowVersion int64, status domain.TeamStatus,
) (*domain.Team, error) {
	if !status.Valid() {
		return nil, domain.NewValidationError("status", "must be one of: active, archived")
	}

	row := s.pool.QueryRow(ctx, `
		UPDATE team
		   SET status = $3, row_version = row_version + 1, updated_at = now()
		 WHERE team_id = $1 AND row_version = $2
		RETURNING `+teamColumns,
		teamID, rowVersion, string(status))

	t, err := scanTeam(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, s.explainMissedUpdate(ctx, teamID)
	}
	if err != nil {
		return nil, fmt.Errorf("set team status: %w", err)
	}
	return t, nil
}

// explainMissedUpdate distinguishes "gone" from "stale version" after an
// UPDATE matched no row, so the caller gets 404 or 409 rather than one
// ambiguous failure. This mirrors UserStore.explainMissedUpdate.
func (s *TeamStore) explainMissedUpdate(ctx context.Context, teamID string) error {
	if _, err := s.Get(ctx, teamID); err != nil {
		return err
	}
	return domain.ErrVersionMismatch
}

// AddMember binds a user to a team.
//
// uq_team_membership_team_user makes a second row impossible. Adding a user
// who is already a member is idempotent: the conflict is resolved by DO
// NOTHING, and the existing row is fetched and returned rather than treated as
// a failure — the same shape MembershipStore.Grant uses for its own conflict.
func (s *TeamStore) AddMember(ctx context.Context, m *domain.TeamMembership) (*domain.TeamMembership, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}

	row := s.pool.QueryRow(ctx, `
		INSERT INTO team_membership (team_membership_id, tenant_id, team_id, user_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (team_id, user_id) DO NOTHING
		RETURNING `+teamMembershipColumns,
		m.TeamMembershipID, m.TenantID, m.TeamID, m.UserID)

	added, err := scanTeamMembership(row)
	if errors.Is(err, pgx.ErrNoRows) {
		// The conflict target matched and the insert was suppressed: the user is
		// already on the team. The existing row is what the caller gets.
		added, err = scanTeamMembership(s.pool.QueryRow(ctx,
			`SELECT `+teamMembershipColumns+` FROM team_membership WHERE team_id = $1 AND user_id = $2`,
			m.TeamID, m.UserID))
	}
	if err != nil {
		return nil, fmt.Errorf("add team member: %w", err)
	}
	return added, nil
}

// RemoveMember unbinds a user from a team.
//
// Unlike Membership.Revoke, this is a real DELETE: team_membership carries no
// status because it authorises nothing on its own (see the doc comment on
// domain.TeamMembership), so there is no administrative record whose subject
// the row must survive for.
func (s *TeamStore) RemoveMember(ctx context.Context, teamID, userID string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM team_membership WHERE team_id = $1 AND user_id = $2`, teamID, userID)
	if err != nil {
		return fmt.Errorf("remove team member: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// ListMembers returns a page of a team's members, keyset-paginated over
// team_membership_id.
func (s *TeamStore) ListMembers(
	ctx context.Context, teamID string, limit int, after string,
) ([]*domain.TeamMembership, error) {
	limit = clampTeamPage(limit)

	rows, err := s.pool.Query(ctx, `
		SELECT `+teamMembershipColumns+`
		  FROM team_membership
		 WHERE team_id = $1 AND ($2 = '' OR team_membership_id > $2)
		 ORDER BY team_membership_id
		 LIMIT $3`, teamID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list team members: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.TeamMembership, 0, limit)
	for rows.Next() {
		m, err := scanTeamMembership(rows)
		if err != nil {
			return nil, fmt.Errorf("scan team membership: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate team members: %w", err)
	}
	return out, nil
}
