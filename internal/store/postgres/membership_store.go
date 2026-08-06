package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
)

// defaultMembershipPageSize bounds an unbounded List; maxMembershipPageSize
// caps what a caller may ask for. Same shape as the other identity stores.
const (
	defaultMembershipPageSize = 50
	maxMembershipPageSize     = 500
)

// MembershipStore administers memberships.
//
// membership is TENANT-OWNED — it is in TenantOwnedTables and carries row-level
// security — so this store is built from the tenant-scoped pool and takes no
// tenant argument anywhere. The bound connection is the boundary: a caller
// cannot name another tenant's membership, because the policy makes the row
// invisible rather than because a predicate remembered to exclude it
// (ADR-TENANCY-002).
//
// Every mutation runs inside withAdminAudit, the constitutional chokepoint. No
// audit logic is written here: the grant and its record commit in one
// transaction or neither does, and that is the chokepoint's guarantee, not this
// store's (ADR-AUDIT-007 §6.9).
type MembershipStore struct {
	pool *pgxpool.Pool
}

// NewMembershipStore builds the membership repository over the given pool.
func NewMembershipStore(pool *pgxpool.Pool) *MembershipStore {
	return &MembershipStore{pool: pool}
}

var _ domain.MembershipRepository = (*MembershipStore)(nil)

// clampMembershipPage bounds a requested page size.
//
// Extracted so the bound can be asserted directly. Proving it through the store
// would mean seeding more rows than the cap — five hundred audited grants — to
// observe a difference, which is slow enough that the assertion would not be
// written, and an unasserted bound is not a bound.
func clampMembershipPage(limit int) int {
	if limit <= 0 {
		return defaultMembershipPageSize
	}
	if limit > maxMembershipPageSize {
		return maxMembershipPageSize
	}
	return limit
}

// Grant binds a user to an organisation.
//
// A membership that already exists is reactivated rather than duplicated:
// uq_membership_org_user makes a second row impossible, and re-granting is what
// re-inviting someone means. An already-active membership is returned as it
// stands, so a repeated grant does not consume a row version a concurrent
// administrator is holding.
func (s *MembershipStore) Grant(ctx context.Context, m *domain.Membership) (*domain.Membership, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}

	var granted *domain.Membership
	err := withAdminAudit(ctx, s.pool,
		func() []domain.AdminAct {
			return []domain.AdminAct{{
				Operation: domain.AdminMembershipGranted,
				Subject: domain.AdminSubject{
					OrgID: m.OrgID, TenantID: m.TenantID, UserID: m.UserID,
				},
				Detail: map[string]any{"membership_id": granted.MembershipID},
			}}
		},
		func(tx pgx.Tx) error {
			row := tx.QueryRow(ctx, `
				INSERT INTO membership (membership_id, tenant_id, org_id, user_id, status)
				VALUES ($1, $2, $3, $4, 'active')
				ON CONFLICT (org_id, user_id) DO UPDATE
				   SET status = 'active',
				       row_version = membership.row_version + 1,
				       updated_at = now()
				RETURNING `+membershipColumns,
				m.MembershipID, m.TenantID, m.OrgID, m.UserID)

			var scanErr error
			granted, scanErr = scanMembership(row)
			if errors.Is(scanErr, pgx.ErrNoRows) {
				// The conflict target matched and the update was suppressed:
				// the user is already an active member. The existing row is what
				// the caller gets.
				granted, scanErr = scanMembership(tx.QueryRow(ctx,
					`SELECT `+membershipColumns+` FROM membership WHERE org_id = $1 AND user_id = $2`,
					m.OrgID, m.UserID))
			}
			if scanErr != nil {
				return fmt.Errorf("grant membership: %w", scanErr)
			}
			return nil
		})
	if err != nil {
		return nil, err
	}
	return granted, nil
}

// Revoke withdraws a membership.
//
// The row survives with status 'revoked'. Deleting it would remove the subject
// the administrative record of the grant points at, and ADR-IDENTITY-001 §8.3
// makes revocation a state change for exactly that reason. The status predicate
// makes this safe to call twice.
func (s *MembershipStore) Revoke(ctx context.Context, membershipID string) (*domain.Membership, error) {
	var revoked *domain.Membership
	err := withAdminAudit(ctx, s.pool,
		func() []domain.AdminAct {
			return []domain.AdminAct{{
				Operation: domain.AdminMembershipRevoked,
				Subject: domain.AdminSubject{
					OrgID: revoked.OrgID, TenantID: revoked.TenantID, UserID: revoked.UserID,
				},
				Detail: map[string]any{"membership_id": revoked.MembershipID},
			}}
		},
		func(tx pgx.Tx) error {
			row := tx.QueryRow(ctx, `
				UPDATE membership
				   SET status = 'revoked', row_version = row_version + 1, updated_at = now()
				 WHERE membership_id = $1 AND status = 'active'
				RETURNING `+membershipColumns, membershipID)

			var scanErr error
			revoked, scanErr = scanMembership(row)
			if errors.Is(scanErr, pgx.ErrNoRows) {
				// Either it is gone, it belongs to another tenant and the policy
				// hides it, or it is already revoked. Probing distinguishes the
				// last from the first two — and it cannot distinguish invisible
				// from absent, which is the isolation working.
				existing, getErr := scanMembership(tx.QueryRow(ctx,
					`SELECT `+membershipColumns+` FROM membership WHERE membership_id = $1`,
					membershipID))
				if getErr != nil {
					return domain.ErrNotFound
				}
				revoked = existing
				return domain.ErrConflict
			}
			if scanErr != nil {
				return fmt.Errorf("revoke membership: %w", scanErr)
			}
			return nil
		})
	if err != nil {
		return nil, err
	}
	return revoked, nil
}

// ActiveMember reports whether userID has an ACTIVE membership row in the
// caller's tenant — a public, read-only counterpart to the private
// verifyActiveMember check several other stores each carry their own copy of
// (OnCallScheduleStore.verifyActiveMember, IncidentStore.verifyAssigneeExists),
// exposed here so a consumer outside this package can perform the identical
// check without a fourth copy of the same SQL. Used by internal/escalation's
// page-time active-membership re-check (E5.2a review nit 1, ADR-ONCALL-003
// §4): an on-call user who has since been revoked must not be paged.
func (s *MembershipStore) ActiveMember(ctx context.Context, userID string) (bool, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM membership WHERE user_id = $1 AND status = 'active')`, userID,
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("check active member: %w", err)
	}
	return exists, nil
}

// ListByOrg returns a page of an organisation's memberships, ordered by
// membership_id — a ULID, and therefore ordered by creation. Keyset rather than
// OFFSET so a page does not shift under concurrent grants.
func (s *MembershipStore) ListByOrg(
	ctx context.Context, orgID string, limit int, after string,
) ([]*domain.Membership, error) {
	limit = clampMembershipPage(limit)

	rows, err := s.pool.Query(ctx, `
		SELECT `+membershipColumns+`
		  FROM membership
		 WHERE org_id = $1 AND ($2 = '' OR membership_id > $2)
		 ORDER BY membership_id
		 LIMIT $3`, orgID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list memberships: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.Membership, 0, limit)
	for rows.Next() {
		m, err := scanMembership(rows)
		if err != nil {
			return nil, fmt.Errorf("scan membership: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate memberships: %w", err)
	}
	return out, nil
}

// ListActiveDirectory returns a page of the CALLER'S OWN tenant's directory —
// every ACTIVE member (an active membership row whose app_user account is
// also active), joined to their global profile for display (E-ACT.0,
// ADR-ACT-003).
//
// The join is the same shape and the same safety argument
// OnCallScheduleRepository.OnCall already relies on: membership is
// tenant-owned and carries row-level security, so the set of rows this query
// can even see is confined to the caller's tenant before app_user is ever
// touched; app_user itself is global (no tenant_id, no RLS), so joining it
// onto an already-confined set discloses no other tenant's membership, only
// the public profile shape (user_id/email/display_name) of a person this
// tenant already recognises. No explicit tenant_id predicate appears here,
// for the same reason none appears anywhere else in this store: the
// tenant-scoped appPool connection supplies it.
//
// membership has no "suspended" state of its own (only active/revoked), so
// "ACTIVE member" is defined as an active membership row naming a user whose
// app_user.status has not been suspended or deactivated — 'active' AND
// 'invited' both qualify (ADR-ACT-003 §4, amended 2026-08-06). An invited
// account has not finished onboarding but is a legitimate assignee/roster
// candidate for a tenant that already granted it an active membership: the
// picker must not come back empty while every seeded user is still
// 'invited'. A membership left active while the underlying account was
// suspended or deactivated at the platform level must not populate an
// assignee picker with someone who cannot currently act.
//
// A plain INNER JOIN, not a defensive LEFT JOIN: uq_membership_org_user's
// sibling FK (membership.user_id REFERENCES app_user) guarantees every
// membership row names an app_user row that exists, so there is no orphan
// case to tolerate here the way OnCall's roster-of-participants read does.
//
// Keyset over user_id (a ULID, globally unique) rather than membership_id:
// the caller is paging people, and a user with two memberships in the same
// tenant is impossible (uq_membership_org_user is per org, and app_user's own
// FK confines each membership to one org+user pair), so user_id is already
// the collection's own natural, deterministic order.
func (s *MembershipStore) ListActiveDirectory(
	ctx context.Context, limit int, after string,
) ([]*domain.TenantUserSummary, error) {
	limit = clampMembershipPage(limit)

	rows, err := s.pool.Query(ctx, `
		SELECT m.user_id, u.email, u.display_name
		  FROM membership m
		  JOIN app_user u ON u.user_id = m.user_id
		 WHERE m.status = 'active' AND u.status IN ('active', 'invited')
		   AND ($1 = '' OR m.user_id > $1)
		 ORDER BY m.user_id
		 LIMIT $2`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list tenant user directory: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.TenantUserSummary, 0, limit)
	for rows.Next() {
		var entry domain.TenantUserSummary
		if err := rows.Scan(&entry.UserID, &entry.Email, &entry.DisplayName); err != nil {
			return nil, fmt.Errorf("scan tenant user directory row: %w", err)
		}
		out = append(out, &entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tenant user directory: %w", err)
	}
	return out, nil
}
