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
