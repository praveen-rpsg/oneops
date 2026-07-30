package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
)

const membershipColumns = `membership_id, tenant_id, org_id, user_id, status, row_version, created_at, updated_at`

// RedemptionStore turns an invitation token into membership, in one
// transaction.
//
// It must be built from the PRIVILEGED pool, and that is not a convenience.
// Redemption necessarily precedes tenant binding: the invitee holds no
// membership, and membership is the fact a tenant would be resolved from
// (ADR-IDENTITY-002 §4.5). On the tenant-scoped pool `app.tenant_id` names some
// other boundary — or none — and every statement below would match no row, so
// redemption would fail closed for everyone. It is the same ordering problem as
// resolving a bearer token to a tenant (ADR-TENANCY-001 §4), and what
// authorises the read is possession of 32 unguessable bytes.
//
// Because this connection is not confined by row-level security, tenant_id is
// taken from the invitation row and from nowhere else. It is never derived,
// defaulted, or accepted from a caller: a membership labelled with the wrong
// tenant is a grant of access to the wrong boundary (ADR-TENANCY-004).
type RedemptionStore struct {
	pool *pgxpool.Pool
}

// NewRedemptionStore builds a redemption repository over the given pool, which
// must be the privileged one.
func NewRedemptionStore(pool *pgxpool.Pool) *RedemptionStore {
	return &RedemptionStore{pool: pool}
}

var _ domain.RedemptionRepository = (*RedemptionStore)(nil)

func scanMembership(s scanner) (*domain.Membership, error) {
	var (
		m      domain.Membership
		status string
	)
	if err := s.Scan(
		&m.MembershipID, &m.TenantID, &m.OrgID, &m.UserID, &status,
		&m.RowVersion, &m.CreatedAt, &m.UpdatedAt,
	); err != nil {
		return nil, err
	}
	m.Status = domain.MembershipStatus(status)
	return &m, nil
}

// Redeem consumes an invitation and produces the account and membership it
// promised.
//
// The whole operation is one transaction, and that is the story's substance.
// Consuming the token in one transaction and creating the membership in another
// has two failure modes and both are real: a crash between them burns the
// token and leaves the invitee with no access and no way to retry, or — if the
// order is reversed — grants membership that a failed consume would leave
// re-claimable. Neither is recoverable by the invitee, who has one token and no
// way to tell what happened.
//
// Every refusal returns domain.ErrTokenNotRedeemable, and every refusal rolls
// back. A rejected redemption consumes nothing.
func (s *RedemptionStore) Redeem(ctx context.Context, token, displayName string) (*domain.Redemption, error) {
	if token == "" {
		return nil, domain.ErrTokenNotRedeemable
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin redemption: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1. Consume. One conditional UPDATE, so the token is claimed exactly once
	//    even under concurrent redemption: the second transaction blocks on the
	//    row lock and then matches nothing, because status is no longer
	//    'pending'. Expiry is compared against the database clock in the same
	//    statement, leaving no window between a check and the write.
	inv, err := scanInvitation(tx.QueryRow(ctx, `
		UPDATE invitation
		   SET status = 'redeemed', redeemed_at = now()
		 WHERE token_hash = $1
		   AND status = 'pending'
		   AND expires_at > now()
		RETURNING `+invitationColumns, domain.HashInvitationToken(token)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrTokenNotRedeemable
	}
	if err != nil {
		return nil, fmt.Errorf("consume invitation: %w", err)
	}

	// 2. Find or create the account. The address is the identity: app_user is
	//    unique on it platform-wide, so an invitation to an address someone
	//    already uses must attach to that account rather than mint a second one
	//    (ADR-IDENTITY-001 §8.2).
	//
	//    The lookup folds case in the query rather than relying on citext's
	//    equality operator, which resolves through search_path and silently
	//    fails to match when `public` is absent — the defect found while
	//    building the user registry.
	user, err := scanUser(tx.QueryRow(ctx,
		`SELECT `+userColumns+` FROM app_user WHERE lower(email::text) = $1`,
		domain.NormalizeEmail(inv.Email)))

	var userCreated bool
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Construction and validation are the aggregate's; repeating them here
		// would be a second answer to what a valid user is.
		fresh, nerr := domain.NewUser(inv.Email, displayName)
		if nerr != nil {
			return nil, nerr
		}
		// The account is created already active. It exists because someone
		// accepted an invitation, which is precisely the evidence `invited`
		// waits for — leaving it invited would create an account that is
		// immediately correct to activate and that nothing would ever activate.
		fresh.Status = domain.UserActive

		user, err = scanUser(tx.QueryRow(ctx, `
			INSERT INTO app_user (user_id, email, display_name, status)
			VALUES ($1, $2, $3, $4)
			RETURNING `+userColumns,
			fresh.UserID, fresh.Email, fresh.DisplayName, string(fresh.Status)))
		if err != nil {
			if isUniqueViolation(err) {
				// Another redemption for the same address committed between the
				// SELECT and here. The token is genuinely unusable now, and
				// rolling back leaves the winner's work intact.
				return nil, domain.ErrTokenNotRedeemable
			}
			return nil, fmt.Errorf("create user for redemption: %w", err)
		}
		userCreated = true

	case err != nil:
		return nil, fmt.Errorf("resolve invitee: %w", err)

	default:
		// The account exists. Whether it may accept is the domain's rule, not a
		// predicate repeated in SQL.
		if !user.Status.MayAcceptInvitation() {
			// Uniform with every other refusal: revealing that the address
			// belongs to a suspended account would confirm the account exists.
			return nil, domain.ErrTokenNotRedeemable
		}
		if user.Status == domain.UserInvited {
			// invited -> active, the transition the lifecycle already defines.
			// Checked rather than assumed, so this stays correct if the machine
			// changes (ADR-IDENTITY-001 §8.3).
			if !user.Status.CanTransitionTo(domain.UserActive) {
				return nil, domain.NewTransitionError(user.Status, domain.UserActive)
			}
			user, err = scanUser(tx.QueryRow(ctx, `
				UPDATE app_user
				   SET status = 'active', row_version = row_version + 1, updated_at = now()
				 WHERE user_id = $1
				RETURNING `+userColumns, user.UserID))
			if err != nil {
				return nil, fmt.Errorf("activate invitee: %w", err)
			}
		}
	}

	// 3. Grant membership. tenant_id comes from the invitation, which is the
	//    only record that already knows which boundary this grant belongs to.
	fresh, err := domain.NewMembership(inv.OrgID, inv.TenantID, user.UserID)
	if err != nil {
		return nil, err
	}

	// uq_membership_org_user makes a duplicate impossible rather than unlikely.
	// A previously revoked membership is reactivated — that is what re-inviting
	// someone means — and one already active is left exactly as it is, so a
	// redemption does not consume a row version that a concurrent administrator
	// is holding.
	membership, err := scanMembership(tx.QueryRow(ctx, `
		INSERT INTO membership (membership_id, tenant_id, org_id, user_id, status)
		VALUES ($1, $2, $3, $4, 'active')
		ON CONFLICT (org_id, user_id) DO UPDATE
		   SET status = 'active',
		       row_version = membership.row_version + 1,
		       updated_at = now()
		 WHERE membership.status <> 'active'
		RETURNING `+membershipColumns,
		fresh.MembershipID, fresh.TenantID, fresh.OrgID, fresh.UserID))

	membershipCreated := true
	if errors.Is(err, pgx.ErrNoRows) {
		// The conflict target matched and the WHERE suppressed the update: the
		// user is already an active member. The invitation is still consumed —
		// it was used — and the existing row is what the caller gets.
		membershipCreated = false
		membership, err = scanMembership(tx.QueryRow(ctx,
			`SELECT `+membershipColumns+` FROM membership WHERE org_id = $1 AND user_id = $2`,
			inv.OrgID, user.UserID))
	}
	if err != nil {
		return nil, fmt.Errorf("grant membership: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit redemption: %w", err)
	}
	return &domain.Redemption{
		Invitation:        inv,
		User:              user,
		Membership:        membership,
		UserCreated:       userCreated,
		MembershipCreated: membershipCreated,
	}, nil
}
