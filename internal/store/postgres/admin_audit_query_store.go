package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
)

// adminAuditQueryColumns is the projection the read path returns. payload_canonical
// rather than payload: it is the byte sequence the chain hash was computed over,
// so a caller can verify a record instead of trusting this API's rendering.
const adminAuditQueryColumns = `chain_id, seq, event_id, operation, actor,
	subject_org_id, subject_tenant_id, subject_user_id,
	payload_canonical, this_hash, occurred_at`

// AdminAuditQueryStore is the sole implementation of the constitutional read
// boundary for administrative audit.
//
// It is built from the PRIVILEGED pool, and that is the story's least-privilege
// decision rather than a shortcut. The alternative — the tenant-scoped pool —
// would require granting SELECT on admin_audit_event to oneops_app, the role
// every request connection assumes. The table is deliberately outside row-level
// security (ADR-AUDIT-007 §6.4), so that grant would put every customer's
// administrative history one statement away from any request-path code, with
// requirePlatformAdmin as the only control. Reading here costs one wiring
// exemption and keeps oneops_app holding no read access at all.
type AdminAuditQueryStore struct {
	pool *pgxpool.Pool
}

// NewAdminAuditQueryStore builds the administrative audit reader.
func NewAdminAuditQueryStore(pool *pgxpool.Pool) *AdminAuditQueryStore {
	return &AdminAuditQueryStore{pool: pool}
}

var _ domain.AdminAuditReader = (*AdminAuditQueryStore)(nil)

// QueryAdminAudit returns one page of administrative history, newest first.
//
// Ordering is (occurred_at, chain_id, seq) descending — a total order, because
// (chain_id, seq) is the primary key and breaks the ties occurred_at cannot.
// The keyset predicate is a row-value comparison over exactly that tuple, so a
// page can neither skip a row nor repeat one, whatever commits between pages.
// ix_admin_audit_page matches the order, so the index is walked rather than the
// table sorted.
func (s *AdminAuditQueryStore) QueryAdminAudit(
	ctx context.Context, f domain.AdminAuditFilter,
) ([]*domain.AdminAuditRecord, *domain.AdminAuditCursor, error) {
	if err := f.Validate(); err != nil {
		return nil, nil, err
	}

	var (
		where []string
		args  []any
	)
	add := func(clause string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}
	if f.Actor != "" {
		add("actor = $%d", f.Actor)
	}
	if f.Operation != "" {
		add("operation = $%d", string(f.Operation))
	}
	if f.SubjectOrgID != "" {
		add("subject_org_id = $%d", f.SubjectOrgID)
	}
	if f.SubjectTenantID != "" {
		add("subject_tenant_id = $%d", f.SubjectTenantID)
	}
	if f.SubjectUserID != "" {
		add("subject_user_id = $%d", f.SubjectUserID)
	}
	if !f.From.IsZero() {
		add("occurred_at >= $%d", f.From.UTC())
	}
	if !f.To.IsZero() {
		add("occurred_at <= $%d", f.To.UTC())
	}
	if f.After.ChainID != "" {
		args = append(args, f.After.OccurredAt.UTC(), f.After.ChainID, f.After.Seq)
		where = append(where, fmt.Sprintf(
			"(occurred_at, chain_id, seq) < ($%d, $%d, $%d)", len(args)-2, len(args)-1, len(args)))
	}

	q := `SELECT ` + adminAuditQueryColumns + ` FROM admin_audit_event`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	// Fetch one beyond the page so the next cursor is only issued when a further
	// row genuinely exists — an empty final page is a bug a caller cannot
	// distinguish from a lost row.
	args = append(args, f.Limit+1)
	q += fmt.Sprintf(" ORDER BY occurred_at DESC, chain_id DESC, seq DESC LIMIT $%d", len(args))

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("query administrative audit: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.AdminAuditRecord, 0, f.Limit)
	for rows.Next() {
		var (
			r                    domain.AdminAuditRecord
			operation            string
			orgID, tenantID, uID *string
		)
		if err := rows.Scan(&r.ChainID, &r.Seq, &r.EventID, &operation, &r.Actor,
			&orgID, &tenantID, &uID, &r.Payload, &r.ThisHash, &r.OccurredAt); err != nil {
			return nil, nil, fmt.Errorf("scan administrative audit row: %w", err)
		}
		r.Operation = domain.AdminOperation(operation)
		if orgID != nil {
			r.SubjectOrgID = *orgID
		}
		if tenantID != nil {
			r.SubjectTenantID = *tenantID
		}
		if uID != nil {
			r.SubjectUserID = *uID
		}
		out = append(out, &r)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate administrative audit: %w", err)
	}

	if len(out) > f.Limit {
		last := out[f.Limit-1]
		out = out[:f.Limit]
		return out, &domain.AdminAuditCursor{
			OccurredAt: last.OccurredAt, ChainID: last.ChainID, Seq: last.Seq,
		}, nil
	}
	return out, nil, nil
}
