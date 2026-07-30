package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
)

const invitationColumns = `invitation_id, tenant_id, org_id, email::text, token_hash, status, expires_at, created_at, redeemed_at`

// defaultInvitationPageSize bounds an unbounded ListByOrg; maxInvitationPageSize
// caps what a caller may ask for. Both mirror the user and organisation
// registries rather than inventing a third paging policy.
const (
	defaultInvitationPageSize = 50
	maxInvitationPageSize     = 500
)

// InvitationStore is the PostgreSQL implementation of
// domain.InvitationRepository.
//
// invitation is TENANT-SCOPED: it carries tenant_id, is registered in
// TenantOwnedTables, and is under forced row-level security
// (ADR-IDENTITY-002 §2.4). Create, Get, ListByOrg and Revoke are therefore
// administrative operations that see one boundary only.
//
// Consume is the deliberate exception and is documented on the method.
type InvitationStore struct {
	pool *pgxpool.Pool
}

// NewInvitationStore builds an invitation repository over the given pool.
func NewInvitationStore(pool *pgxpool.Pool) *InvitationStore {
	return &InvitationStore{pool: pool}
}

var _ domain.InvitationRepository = (*InvitationStore)(nil)

// scanInvitation reads a row.
//
// email is selected as ::text rather than as citext. The citext equality
// operator resolves through search_path, and a session without `public` on it
// silently fails to match — the defect found while building the user registry.
// Reading the column as text removes the dependency entirely; uniqueness and
// case-insensitive matching remain the database's, on the column's real type.
func scanInvitation(s scanner) (*domain.Invitation, error) {
	var (
		i      domain.Invitation
		status string
	)
	if err := s.Scan(
		&i.InvitationID, &i.TenantID, &i.OrgID, &i.Email, &i.TokenHash,
		&status, &i.ExpiresAt, &i.CreatedAt, &i.RedeemedAt,
	); err != nil {
		return nil, err
	}
	i.Status = domain.InvitationStatus(status)
	return &i, nil
}

// Create stores an invitation.
//
// Only the hash is written; the plaintext token never reaches this package.
// A duplicate token_hash is a conflict by constraint, and in practice means the
// entropy source repeated itself — which is a fault worth surfacing rather than
// retrying around.
func (s *InvitationStore) Create(ctx context.Context, i *domain.Invitation) (*domain.Invitation, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}

	var created *domain.Invitation
	err := withAdminAudit(ctx, s.pool,
		func() []domain.AdminAct {
			return []domain.AdminAct{{
				Operation: domain.AdminInvitationCreated,
				Subject:   domain.AdminSubject{OrgID: i.OrgID, TenantID: i.TenantID},
				Detail:    map[string]any{"invitation_id": i.InvitationID, "email": string(i.Email)},
			}}
		},
		func(tx pgx.Tx) error {
			row := tx.QueryRow(ctx, `
				INSERT INTO invitation (invitation_id, tenant_id, org_id, email, token_hash, status, expires_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7)
				RETURNING `+invitationColumns,
				i.InvitationID, i.TenantID, i.OrgID, i.Email, i.TokenHash, string(i.Status), i.ExpiresAt)

			var scanErr error
			created, scanErr = scanInvitation(row)
			if scanErr != nil {
				if isUniqueViolation(scanErr) {
					return domain.ErrConflict
				}
				return fmt.Errorf("insert invitation: %w", scanErr)
			}
			return nil
		})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// Get returns one invitation by identifier, within the caller's tenant.
func (s *InvitationStore) Get(ctx context.Context, invitationID string) (*domain.Invitation, error) {
	i, err := scanInvitation(s.pool.QueryRow(ctx,
		`SELECT `+invitationColumns+` FROM invitation WHERE invitation_id = $1`, invitationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get invitation: %w", err)
	}
	return i, nil
}

// ListByOrg returns a page of an organisation's invitations.
//
// Ordering is by invitation_id, a ULID and therefore ordered by creation, so
// keyset paging is stable under concurrent inserts. Nothing here selects
// token_hash into a caller-facing position it would not already occupy: the
// hash is not the token, and withholding it would not make the token any less
// unrecoverable.
func (s *InvitationStore) ListByOrg(
	ctx context.Context, orgID string, limit int, after string,
) ([]*domain.Invitation, error) {
	if limit <= 0 {
		limit = defaultInvitationPageSize
	}
	if limit > maxInvitationPageSize {
		limit = maxInvitationPageSize
	}

	rows, err := s.pool.Query(ctx, `
		SELECT `+invitationColumns+`
		  FROM invitation
		 WHERE org_id = $1
		   AND ($2 = '' OR invitation_id > $2)
		 ORDER BY invitation_id
		 LIMIT $3`, orgID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list invitations: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.Invitation, 0, limit)
	for rows.Next() {
		i, err := scanInvitation(rows)
		if err != nil {
			return nil, fmt.Errorf("scan invitation: %w", err)
		}
		out = append(out, i)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate invitations: %w", err)
	}
	return out, nil
}

// Consume atomically redeems a pending, unexpired invitation.
//
// The whole rule is in one conditional UPDATE, and that is what makes
// redemption single-use. A read-then-write would let two requests carrying the
// same token both observe `pending` and both proceed; here the second finds no
// row matching `status = 'pending'` because the first has already changed it,
// and PostgreSQL serialises the two on the row lock. Expiry is compared against
// the database clock in the same statement, so a token cannot be redeemed in
// the window between a Go-side check and the write.
//
// Every failure returns the same error. A token that never existed, one already
// redeemed, one revoked and one expired are indistinguishable to the caller —
// otherwise the endpoint reports whether a guessed token was real.
//
// This method runs OUTSIDE row-level security by necessity: an invitee holds no
// membership, so there is no tenant to bind before the invitation is found. It
// is the same ordering problem as resolving a bearer token to a tenant
// (ADR-TENANCY-001 §4), and possession of the token is what authorises the
// read. It must therefore be given the privileged pool; the tenant-scoped pool
// would match no row and every redemption would fail closed.
func (s *InvitationStore) Consume(ctx context.Context, token string) (*domain.Invitation, error) {
	if token == "" {
		return nil, domain.ErrTokenNotRedeemable
	}

	// This path is authorised by possession of the token, not by a session, so
	// there are no claims and ActorFrom would refuse the act. The bearer is
	// recorded as a synthetic principal: the act is real and must be audited,
	// and the subject columns carry which invitation and which organisation.
	// Naming the bearer explicitly is honest — inventing a human actor here
	// would attribute the redemption to someone who did not perform it.
	var redeemed *domain.Invitation
	err := withAdminAudit(domain.WithActor(ctx, domain.InvitationBearerActor), s.pool,
		func() []domain.AdminAct {
			return []domain.AdminAct{{
				Operation: domain.AdminInvitationRedeemed,
				Subject:   domain.AdminSubject{OrgID: redeemed.OrgID, TenantID: redeemed.TenantID},
				Detail:    map[string]any{"invitation_id": redeemed.InvitationID},
			}}
		},
		func(tx pgx.Tx) error {
			row := tx.QueryRow(ctx, `
				UPDATE invitation
				   SET status = 'redeemed', redeemed_at = now()
				 WHERE token_hash = $1
				   AND status = 'pending'
				   AND expires_at > now()
				RETURNING `+invitationColumns, domain.HashInvitationToken(token))

			var scanErr error
			redeemed, scanErr = scanInvitation(row)
			if errors.Is(scanErr, pgx.ErrNoRows) {
				return domain.ErrTokenNotRedeemable
			}
			if scanErr != nil {
				return fmt.Errorf("consume invitation: %w", scanErr)
			}
			return nil
		})
	if err != nil {
		return nil, err
	}
	return redeemed, nil
}

// Revoke withdraws a pending invitation.
//
// The status predicate is what makes this safe to call twice: a revoked or
// already-redeemed invitation matches nothing, so revocation cannot silently
// undo a redemption that has already happened.
func (s *InvitationStore) Revoke(ctx context.Context, invitationID string) (*domain.Invitation, error) {
	var revoked *domain.Invitation
	err := withAdminAudit(ctx, s.pool,
		func() []domain.AdminAct {
			return []domain.AdminAct{{
				Operation: domain.AdminInvitationRevoked,
				Subject:   domain.AdminSubject{OrgID: revoked.OrgID, TenantID: revoked.TenantID},
				Detail:    map[string]any{"invitation_id": invitationID},
			}}
		},
		func(tx pgx.Tx) error {
			row := tx.QueryRow(ctx, `
				UPDATE invitation
				   SET status = 'revoked'
				 WHERE invitation_id = $1
				   AND status = 'pending'
				RETURNING `+invitationColumns, invitationID)

			var scanErr error
			revoked, scanErr = scanInvitation(row)
			if errors.Is(scanErr, pgx.ErrNoRows) {
				// Either it is gone or it is no longer pending. Probing
				// distinguishes them so the caller gets 404 or 409 rather than
				// one ambiguous failure, the same shape TenantStore and
				// OrganizationStore use.
				if _, getErr := s.Get(ctx, invitationID); getErr != nil {
					return getErr
				}
				return domain.ErrConflict
			}
			if scanErr != nil {
				return fmt.Errorf("revoke invitation: %w", scanErr)
			}
			return nil
		})
	if err != nil {
		return nil, err
	}
	return revoked, nil
}
