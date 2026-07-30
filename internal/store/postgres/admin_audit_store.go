package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rpsg/oneops/internal/domain"
)

// The administrative audit chain's persistence. This is a second store, not a
// second history mechanism (ADR-AUDIT-007 §6.10): the sealing —
// canonicalisation, the hash input, event-id derivation, the genesis previous
// hash — is internal/audit's, used as functions and not re-derived. What is new
// is only which tables the rows land in and who may see them, which is the
// entire point of the decision.
//
// These are functions rather than methods on a store type because every one of
// them operates on a caller-owned transaction and none needs a pool. A struct
// holding an unused pool would be an abstraction with no subject; OPS-S038's
// reader will need one and can introduce it then.
//
// There is deliberately no ListChainIDs here. The relay, the replay worker and
// the policy consumer all discover work through AuditStore.ListChainIDs, which
// reads audit_chain_head; keeping the administrative heads in their own table
// is what makes administrative events undeliverable by construction rather than
// by a filter someone must remember (§6.5).

// ensureAdminChainHead creates the chain's head row if this is its first act.
// ON CONFLICT DO NOTHING makes a concurrent first act adopt the existing head
// rather than fork the chain.
func ensureAdminChainHead(ctx context.Context, tx pgx.Tx, chainID string, genesis []byte) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO admin_audit_chain_head (chain_id, last_seq, last_hash)
		VALUES ($1, 0, $2)
		ON CONFLICT (chain_id) DO NOTHING`, chainID, genesis); err != nil {
		return fmt.Errorf("ensure admin chain head %s: %w", chainID, err)
	}
	return nil
}

// readAdminChainHeadForUpdate takes the serialisation lock for a chain and
// returns its tip.
//
// The name carries the obligation, exactly as ADR-AUDIT-006 requires of the
// constitutional chain, and there is no non-locking sibling on this store — so
// there is no way to append without holding the lock. A plain read would let
// two concurrent appends compute the same seq and collide on the primary key.
func readAdminChainHeadForUpdate(ctx context.Context, tx pgx.Tx, chainID string) (int64, []byte, error) {
	var lastSeq int64
	var lastHash []byte
	err := tx.QueryRow(ctx, `
		SELECT last_seq, last_hash FROM admin_audit_chain_head
		WHERE chain_id = $1 FOR UPDATE`, chainID).Scan(&lastSeq, &lastHash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil, fmt.Errorf("admin chain head %s: %w", chainID, domain.ErrNotFound)
		}
		return 0, nil, fmt.Errorf("lock admin chain head %s: %w", chainID, err)
	}
	return lastSeq, lastHash, nil
}

// appendAdminAuditEvent writes one sealed administrative record.
func appendAdminAuditEvent(ctx context.Context, tx pgx.Tx, e *adminAuditRow) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO admin_audit_event
			(chain_id, seq, event_id, operation_id, operation, actor,
			 subject_org_id, subject_tenant_id, subject_user_id,
			 payload_canonical, payload, prev_hash, this_hash, occurred_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,convert_from($10,'UTF8')::jsonb,$11,$12,$13)`,
		e.chainID, e.seq, e.eventID, e.operationID, string(e.operation), e.actor,
		nullIfEmpty(e.subject.OrgID), nullIfEmpty(e.subject.TenantID), nullIfEmpty(e.subject.UserID),
		e.payloadCanonical, e.prevHash, e.thisHash, e.occurredAt)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("append admin audit event: %w", domain.ErrConflict)
		}
		return fmt.Errorf("append admin audit event: %w", err)
	}
	return nil
}

// upsertAdminChainHead advances the chain tip.
func upsertAdminChainHead(ctx context.Context, tx pgx.Tx, chainID string, seq int64, hash []byte) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO admin_audit_chain_head (chain_id, last_seq, last_hash, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (chain_id) DO UPDATE
		   SET last_seq = EXCLUDED.last_seq,
		       last_hash = EXCLUDED.last_hash,
		       updated_at = now()`, chainID, seq, hash); err != nil {
		return fmt.Errorf("advance admin chain head %s: %w", chainID, err)
	}
	return nil
}

// adminAuditRow is the sealed record on its way to the database.
type adminAuditRow struct {
	chainID          string
	seq              int64
	eventID          string
	operationID      string
	operation        domain.AdminOperation
	actor            string
	subject          domain.AdminSubject
	payloadCanonical []byte
	prevHash         []byte
	thisHash         []byte
	occurredAt       time.Time
}

// nullIfEmpty maps an absent subject identifier to SQL NULL. The columns are
// nullable because an act on a user has no organisation and an act on the
// tenant registry has no user; writing ” would make "no subject" and "a
// subject whose id is empty" indistinguishable, and would defeat the partial
// index on subject_org_id.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
