package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/governance"
)

const approvalColumns = `approval_id, tenant_id, governance_id, approver_user_id, created_at`

// ApprovalStore is both the Governance Engine's transaction-scoped quorum
// tracker (governance.ApprovalRecorder) and the read-only administration
// repository (domain.ApprovalRepository) for GET .../approvals — the same
// split GovernanceStore/ConfigObjectRepo have for configuration_object: one
// table, a transactional write port and a plain-pool read port.
//
// approval_record is TENANT-OWNED — it is in TenantOwnedTables and carries
// row-level security — so ListApprovers takes no tenant argument: the bound
// connection is the boundary (ADR-TENANCY-002). Record and CountDistinct
// instead take tenantID explicitly, exactly as TeamStore.Create and
// SettingStore.Upsert take TenantID on their domain structs: the column
// needs an actual value to write, and RLS's WITH CHECK is defence in depth
// against writing it wrong, not a substitute for supplying it.
type ApprovalStore struct {
	pool *pgxpool.Pool
}

// NewApprovalStore builds the store over the given pool.
func NewApprovalStore(pool *pgxpool.Pool) *ApprovalStore {
	return &ApprovalStore{pool: pool}
}

var (
	_ governance.ApprovalRecorder = (*ApprovalStore)(nil)
	_ domain.ApprovalRepository   = (*ApprovalStore)(nil)
)

func scanApproval(s scanner) (*domain.ApprovalRecord, error) {
	var a domain.ApprovalRecord
	if err := s.Scan(&a.ApprovalID, &a.TenantID, &a.GovernanceID, &a.ApproverUserID, &a.CreatedAt); err != nil {
		return nil, err
	}
	return &a, nil
}

// Record inserts one distinct approval inside the engine's own transaction.
// uq_approval_tenant_governance_approver is what makes a second approval from
// the same approver impossible to record, rather than merely discouraged by
// application logic a bug or a second code path could bypass; a violation of
// it is surfaced as governance.ErrAlreadyApproved rather than a raw conflict.
func (s *ApprovalStore) Record(ctx context.Context, tx pgx.Tx, tenantID, governanceID, approverUserID string) error {
	rec, err := domain.NewApprovalRecord(tenantID, governanceID, approverUserID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO approval_record (approval_id, tenant_id, governance_id, approver_user_id)
		VALUES ($1, $2, $3, $4)`,
		rec.ApprovalID, rec.TenantID, rec.GovernanceID, rec.ApproverUserID)
	switch {
	case err == nil:
		return nil
	case isUniqueViolation(err):
		return governance.ErrAlreadyApproved
	case isForeignKeyViolation(err):
		return domain.ErrNotFound
	default:
		return fmt.Errorf("record approval: %w", err)
	}
}

// CountDistinct returns the number of distinct approvers recorded for
// governanceID, inside the same transaction as Record — so the count that
// decides whether quorum is met reflects exactly what this transaction has
// (and has not) committed.
func (s *ApprovalStore) CountDistinct(ctx context.Context, tx pgx.Tx, governanceID string) (int, error) {
	var n int
	if err := tx.QueryRow(ctx,
		`SELECT count(DISTINCT approver_user_id) FROM approval_record WHERE governance_id = $1`,
		governanceID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count distinct approvals: %w", err)
	}
	return n, nil
}

// ListApprovers returns every distinct approval recorded for governanceID,
// ordered by CreatedAt (and ApprovalID as a tiebreak, since a ULID is itself
// time-ordered) so the sequence a client sees is deterministic.
func (s *ApprovalStore) ListApprovers(ctx context.Context, governanceID string) ([]*domain.ApprovalRecord, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+approvalColumns+`
		  FROM approval_record
		 WHERE governance_id = $1
		 ORDER BY created_at, approval_id`, governanceID)
	if err != nil {
		return nil, fmt.Errorf("list approvers: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.ApprovalRecord, 0)
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, fmt.Errorf("scan approval: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate approvals: %w", err)
	}
	return out, nil
}
