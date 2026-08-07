package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
)

const complianceControlColumns = `control_id, tenant_id, framework, control_ref, title, description, status,
	row_version, created_at, updated_at`

const controlEvidenceColumns = `evidence_id, tenant_id, control_id, kind, value, recorded_by, recorded_at`

// defaultComplianceControlPageSize/maxComplianceControlPageSize bound List;
// defaultControlEvidenceLimit/maxControlEvidenceLimit bound Evidence — the
// same clamp shape RiskStore's page/register-limit pairs use.
const (
	defaultComplianceControlPageSize = 50
	maxComplianceControlPageSize     = 500

	defaultControlEvidenceLimit = 200
	maxControlEvidenceLimit     = 500
)

// ComplianceControlStore administers compliance_control and control_evidence
// (E8.4b, ADR-SOC-009) — built EXCLUSIVELY over the tenant-scoped pool,
// mirroring RiskStore/IncidentStore. There is no privileged-pool
// counterpart: every write this story needs is expressible from one
// tenant's own connection.
//
// compliance_control/control_evidence are TENANT-OWNED — both are in
// TenantOwnedTables and carry row-level security — so this store takes no
// tenant argument anywhere; the bound connection already is the boundary
// (ADR-TENANCY-002).
type ComplianceControlStore struct {
	pool *pgxpool.Pool
}

// NewComplianceControlStore builds the store over the given (tenant-scoped) pool.
func NewComplianceControlStore(pool *pgxpool.Pool) *ComplianceControlStore {
	return &ComplianceControlStore{pool: pool}
}

var _ domain.ComplianceControlRepository = (*ComplianceControlStore)(nil)

func clampComplianceControlPage(limit int) int {
	if limit <= 0 {
		return defaultComplianceControlPageSize
	}
	if limit > maxComplianceControlPageSize {
		return maxComplianceControlPageSize
	}
	return limit
}

func clampControlEvidenceLimit(limit int) int {
	if limit <= 0 {
		return defaultControlEvidenceLimit
	}
	if limit > maxControlEvidenceLimit {
		return maxControlEvidenceLimit
	}
	return limit
}

func scanComplianceControl(s scanner) (*domain.ComplianceControl, error) {
	var (
		c      domain.ComplianceControl
		status string
	)
	if err := s.Scan(
		&c.ControlID, &c.TenantID, &c.Framework, &c.ControlRef, &c.Title, &c.Description, &status,
		&c.RowVersion, &c.CreatedAt, &c.UpdatedAt,
	); err != nil {
		return nil, err
	}
	c.Status = domain.ComplianceControlStatus(status)
	return &c, nil
}

func scanControlEvidence(s scanner) (*domain.ControlEvidence, error) {
	var (
		e    domain.ControlEvidence
		kind string
	)
	if err := s.Scan(&e.EvidenceID, &e.TenantID, &e.ControlID, &kind, &e.Value, &e.RecordedBy, &e.RecordedAt); err != nil {
		return nil, err
	}
	e.Kind = domain.ControlEvidenceKind(kind)
	return &e, nil
}

// getForUpdateTx reads controlID's current row under FOR UPDATE, inside tx,
// so Update/SetStatus/AddEvidence serialise on this row for the rest of
// their transaction — mirrors RiskStore.getForUpdateTx/
// IncidentStore.getForUpdateTx exactly. Row-level security alone confines
// this to the caller's own tenant: a controlID belonging to another tenant
// is invisible to this SELECT, so it returns ErrNotFound exactly as if the
// row did not exist.
func (s *ComplianceControlStore) getForUpdateTx(ctx context.Context, tx pgx.Tx, controlID string) (*domain.ComplianceControl, error) {
	row := tx.QueryRow(ctx, `SELECT `+complianceControlColumns+` FROM compliance_control WHERE control_id = $1 FOR UPDATE`, controlID)
	c, err := scanComplianceControl(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get compliance control for update: %w", err)
	}
	return c, nil
}

// Create inserts a control.
func (s *ComplianceControlStore) Create(ctx context.Context, c *domain.ComplianceControl) (*domain.ComplianceControl, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}

	row := s.pool.QueryRow(ctx, `
		INSERT INTO compliance_control
			(control_id, tenant_id, framework, control_ref, title, description, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+complianceControlColumns,
		c.ControlID, c.TenantID, c.Framework, c.ControlRef, c.Title, c.Description, string(c.Status))

	created, err := scanComplianceControl(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, domain.ErrConflict
		}
		return nil, fmt.Errorf("insert compliance control: %w", err)
	}
	return created, nil
}

// Get returns a control by identifier, or domain.ErrNotFound.
func (s *ComplianceControlStore) Get(ctx context.Context, controlID string) (*domain.ComplianceControl, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+complianceControlColumns+` FROM compliance_control WHERE control_id = $1`, controlID)
	c, err := scanComplianceControl(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get compliance control: %w", err)
	}
	return c, nil
}

// List returns a page of the caller's controls, keyset-paginated over
// control_id, filtered by framework/status when non-empty — mirrors
// RiskRepository.List's filter shape.
func (s *ComplianceControlStore) List(
	ctx context.Context, limit int, after string, framework string, status domain.ComplianceControlStatus,
) ([]*domain.ComplianceControl, error) {
	limit = clampComplianceControlPage(limit)
	rows, err := s.pool.Query(ctx, `
		SELECT `+complianceControlColumns+`
		  FROM compliance_control
		 WHERE ($1 = '' OR control_id > $1)
		   AND ($2 = '' OR framework = $2)
		   AND ($3 = '' OR status = $3)
		 ORDER BY control_id
		 LIMIT $4`,
		after, framework, string(status), limit)
	if err != nil {
		return nil, fmt.Errorf("list compliance controls: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.ComplianceControl, 0, limit)
	for rows.Next() {
		c, err := scanComplianceControl(rows)
		if err != nil {
			return nil, fmt.Errorf("scan compliance control: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate compliance controls: %w", err)
	}
	return out, nil
}

// Update changes one or more non-lifecycle, non-identity fields of patch
// under optimistic locking. Mirrors RiskStore.Update's "merge onto a copy of
// the current row, then Validate the whole entity" shape, chosen here for
// the identical reason: Framework/ControlRef's format rules live inside
// Validate and are not otherwise reachable from this package.
//
// The pre-image is read under FOR UPDATE inside the same transaction as the
// write, so two concurrent PATCHes against the same control serialise on its
// row instead of racing to compute a merge against a value the other has
// already superseded.
func (s *ComplianceControlStore) Update(
	ctx context.Context, controlID string, rowVersion int64, patch domain.ComplianceControlPatch,
) (*domain.ComplianceControl, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	current, err := s.getForUpdateTx(ctx, tx, controlID)
	if err != nil {
		return nil, err
	}
	if current.RowVersion != rowVersion {
		return nil, domain.ErrVersionMismatch
	}

	if patch.Title != nil {
		current.Title = strings.TrimSpace(*patch.Title)
	}
	if patch.Description != nil {
		current.Description = *patch.Description
	}
	if err := current.Validate(); err != nil {
		return nil, err
	}

	row := tx.QueryRow(ctx, `
		UPDATE compliance_control
		   SET title       = $3,
		       description = $4,
		       row_version = row_version + 1,
		       updated_at  = now()
		 WHERE control_id = $1 AND row_version = $2
		RETURNING `+complianceControlColumns,
		controlID, rowVersion, current.Title, current.Description)

	updated, err := scanComplianceControl(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, s.explainMissedUpdate(ctx, controlID)
	}
	if err != nil {
		return nil, fmt.Errorf("update compliance control: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return updated, nil
}

// explainMissedUpdate distinguishes "gone" from "stale version" after an
// UPDATE matched no row — mirrors RiskStore.explainMissedUpdate.
func (s *ComplianceControlStore) explainMissedUpdate(ctx context.Context, controlID string) error {
	if _, err := s.Get(ctx, controlID); err != nil {
		return err
	}
	return domain.ErrVersionMismatch
}

// SetStatus performs a lifecycle transition under optimistic locking,
// guarded by domain.ComplianceControlStatus.CanTransitionTo — the single
// authority for status changes (mirrors RiskStore.SetStatus/
// VulnFindingStore.SetStatus). An illegal move returns
// *domain.ComplianceControlTransitionError (ErrInvalidTransition).
func (s *ComplianceControlStore) SetStatus(
	ctx context.Context, controlID string, rowVersion int64, status domain.ComplianceControlStatus,
) (*domain.ComplianceControl, error) {
	if !status.Valid() {
		return nil, domain.NewValidationError("status", "must be one of: not_implemented, in_progress, implemented, not_applicable")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	current, err := s.getForUpdateTx(ctx, tx, controlID)
	if err != nil {
		return nil, err
	}
	if current.RowVersion != rowVersion {
		return nil, domain.ErrVersionMismatch
	}
	if !current.Status.CanTransitionTo(status) {
		return nil, domain.NewComplianceControlTransitionError(current.Status, status)
	}

	row := tx.QueryRow(ctx, `
		UPDATE compliance_control
		   SET status = $3, row_version = row_version + 1, updated_at = now()
		 WHERE control_id = $1 AND row_version = $2
		RETURNING `+complianceControlColumns,
		controlID, rowVersion, string(status))

	updated, err := scanComplianceControl(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrVersionMismatch // unreachable under the FOR UPDATE lock; defensive only.
	}
	if err != nil {
		return nil, fmt.Errorf("set compliance control status: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return updated, nil
}

// recordEvidence inserts one control_evidence row inside tx, so it commits
// or aborts with the mutation it records — mirrors
// IncidentStore.recordEvent's identical same-transaction discipline. Called
// ONLY by AddEvidence, which reads the parent control under FOR UPDATE
// first (ADR-AUDIT-006's chain-head-lock discipline, generalised by
// arch.TestEveryAuditAppendPath_SerialisesOnItsChainHead to every
// append-only-guarded table, not only the audit hash-chain: control_evidence
// carries no chain sequence number of its own — see domain.ControlEvidence's
// doc comment — so the lock serialises the tenant re-verification read, not
// a seq assignment, but it is still the transaction owner's locking read
// the guard requires).
func (s *ComplianceControlStore) recordEvidence(ctx context.Context, tx pgx.Tx, ev *domain.ControlEvidence) (*domain.ControlEvidence, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO control_evidence (evidence_id, tenant_id, control_id, kind, value, recorded_by)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+controlEvidenceColumns,
		ev.EvidenceID, ev.TenantID, ev.ControlID, string(ev.Kind), ev.Value, ev.RecordedBy)

	created, err := scanControlEvidence(row)
	if err != nil {
		if isForeignKeyViolation(err) {
			// Belt-and-suspenders against a TOCTOU between AddEvidence's own
			// FOR UPDATE read and this INSERT — the same defensive branch
			// RiskStore.Create takes for its own optional asset link.
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("insert control evidence: %w", err)
	}
	return created, nil
}

// AddEvidence appends one control_evidence row inside a transaction that
// first re-reads controlID under FOR UPDATE on this store's own
// tenant-scoped connection — proving controlID belongs to the caller's
// tenant (row-level security makes a cross-tenant id invisible to this
// SELECT, returning ErrNotFound) BEFORE the evidence row is written, since
// the foreign key alone bypasses RLS on compliance_control (ADR-ASSET-001
// §6's reasoning).
func (s *ComplianceControlStore) AddEvidence(
	ctx context.Context, controlID string, kind domain.ControlEvidenceKind, value string,
) (*domain.ControlEvidence, error) {
	actor, ok := domain.ActorFrom(ctx)
	if !ok {
		return nil, domain.ErrNoActor
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	control, err := s.getForUpdateTx(ctx, tx, controlID)
	if err != nil {
		return nil, err
	}

	ev, err := domain.NewControlEvidence(control.TenantID, control.ControlID, kind, value, actor)
	if err != nil {
		return nil, err
	}

	created, err := s.recordEvidence(ctx, tx, ev)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return created, nil
}

// Evidence returns controlID's append-only evidence trail, oldest first,
// bounded to at most limit rows. Row-level security alone confines this to
// rows the caller's tenant actually wrote — mirrors
// IncidentStore.Timeline's identical "no explicit tenant predicate" shape.
func (s *ComplianceControlStore) Evidence(ctx context.Context, controlID string, limit int) ([]*domain.ControlEvidence, error) {
	limit = clampControlEvidenceLimit(limit)

	rows, err := s.pool.Query(ctx, `
		SELECT `+controlEvidenceColumns+`
		  FROM control_evidence
		 WHERE control_id = $1
		 ORDER BY evidence_id
		 LIMIT $2`, controlID, limit)
	if err != nil {
		return nil, fmt.Errorf("list control evidence: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.ControlEvidence, 0, limit)
	for rows.Next() {
		e, err := scanControlEvidence(rows)
		if err != nil {
			return nil, fmt.Errorf("scan control evidence: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate control evidence: %w", err)
	}
	return out, nil
}
