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

const riskColumns = `risk_id, tenant_id, title, description, category, likelihood, impact, status,
	treatment, asset_id, row_version, created_at, updated_at`

// defaultRiskPageSize/maxRiskPageSize bound List; defaultRiskRegisterLimit/
// maxRiskRegisterLimit bound GET /admin/risks/register (E8.4a) — the same
// clamp shape VulnFindingStore's page/priority-limit pairs use.
const (
	defaultRiskPageSize = 50
	maxRiskPageSize     = 500

	defaultRiskRegisterLimit = 50
	maxRiskRegisterLimit     = 500
)

// RiskStore administers risk: the tenant-owned risk-register entity, its
// lifecycle, and the computed likelihood x impact scoring projection
// (E8.4a, ADR-SOC-008) — built EXCLUSIVELY over the tenant-scoped pool,
// mirroring VulnFindingStore/IncidentStore. There is no privileged-pool
// counterpart: every write this story needs (including the optional
// asset_id link) is expressible from one tenant's own connection.
//
// risk is TENANT-OWNED — it is in TenantOwnedTables and carries row-level
// security — so this store takes no tenant argument anywhere; the bound
// connection already is the boundary (ADR-TENANCY-002).
type RiskStore struct {
	pool *pgxpool.Pool
}

// NewRiskStore builds the store over the given (tenant-scoped) pool.
func NewRiskStore(pool *pgxpool.Pool) *RiskStore {
	return &RiskStore{pool: pool}
}

var _ domain.RiskRepository = (*RiskStore)(nil)

func clampRiskPage(limit int) int {
	if limit <= 0 {
		return defaultRiskPageSize
	}
	if limit > maxRiskPageSize {
		return maxRiskPageSize
	}
	return limit
}

func clampRiskRegisterLimit(limit int) int {
	if limit <= 0 {
		return defaultRiskRegisterLimit
	}
	if limit > maxRiskRegisterLimit {
		return maxRiskRegisterLimit
	}
	return limit
}

func scanRisk(s scanner) (*domain.Risk, error) {
	var (
		r          domain.Risk
		likelihood string
		impact     string
		status     string
		treatment  *string
	)
	if err := s.Scan(
		&r.RiskID, &r.TenantID, &r.Title, &r.Description, &r.Category, &likelihood, &impact, &status,
		&treatment, &r.AssetID, &r.RowVersion, &r.CreatedAt, &r.UpdatedAt,
	); err != nil {
		return nil, err
	}
	r.Likelihood = domain.RiskLikelihood(likelihood)
	r.Impact = domain.RiskImpact(impact)
	r.Status = domain.RiskStatus(status)
	if treatment != nil {
		t := domain.RiskTreatment(*treatment)
		r.Treatment = &t
	}
	return &r, nil
}

// verifyAssetExists confirms assetID names a Configuration Item visible to
// this store's own tenant-scoped, RLS-enforced connection before
// risk.asset_id is written — mirrors IncidentStore.verifyAssetExists exactly,
// applied here to Risk's own optional CI link.
func (s *RiskStore) verifyAssetExists(ctx context.Context, assetID string) error {
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM asset WHERE asset_id = $1)`, assetID,
	).Scan(&exists); err != nil {
		return fmt.Errorf("check linked asset exists: %w", err)
	}
	if !exists {
		return domain.ErrNotFound
	}
	return nil
}

// getForUpdateTx reads riskID's current row under FOR UPDATE, inside tx, so
// Update/SetStatus serialise on this row for the rest of their transaction —
// mirrors AssetStore.getForUpdateTx/IncidentStore.getForUpdateTx exactly.
func (s *RiskStore) getForUpdateTx(ctx context.Context, tx pgx.Tx, riskID string) (*domain.Risk, error) {
	row := tx.QueryRow(ctx, `SELECT `+riskColumns+` FROM risk WHERE risk_id = $1 FOR UPDATE`, riskID)
	r, err := scanRisk(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get risk for update: %w", err)
	}
	return r, nil
}

// Create inserts a risk. When r.AssetID is set, it is re-verified against
// this store's own tenant-scoped connection before the INSERT runs.
func (s *RiskStore) Create(ctx context.Context, r *domain.Risk) (*domain.Risk, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	if r.AssetID != nil {
		if err := s.verifyAssetExists(ctx, *r.AssetID); err != nil {
			return nil, err
		}
	}

	var treatment *string
	if r.Treatment != nil {
		v := string(*r.Treatment)
		treatment = &v
	}

	row := s.pool.QueryRow(ctx, `
		INSERT INTO risk
			(risk_id, tenant_id, title, description, category, likelihood, impact, status, treatment, asset_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING `+riskColumns,
		r.RiskID, r.TenantID, r.Title, r.Description, r.Category,
		string(r.Likelihood), string(r.Impact), string(r.Status), treatment, r.AssetID)

	created, err := scanRisk(row)
	if err != nil {
		if isForeignKeyViolation(err) {
			// Belt-and-suspenders against a TOCTOU between verifyAssetExists
			// and this INSERT — the same defensive branch
			// IncidentStore.Create takes for its own optional asset link.
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("insert risk: %w", err)
	}
	return created, nil
}

// Get returns a risk by identifier, or domain.ErrNotFound.
func (s *RiskStore) Get(ctx context.Context, riskID string) (*domain.Risk, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+riskColumns+` FROM risk WHERE risk_id = $1`, riskID)
	r, err := scanRisk(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get risk: %w", err)
	}
	return r, nil
}

// List returns a page of the caller's risks, keyset-paginated over risk_id,
// filtered by status/category when non-empty — mirrors
// VulnFindingRepository.List's filter shape.
func (s *RiskStore) List(
	ctx context.Context, limit int, after string, status domain.RiskStatus, category string,
) ([]*domain.Risk, error) {
	limit = clampRiskPage(limit)
	rows, err := s.pool.Query(ctx, `
		SELECT `+riskColumns+`
		  FROM risk
		 WHERE ($1 = '' OR risk_id > $1)
		   AND ($2 = '' OR status = $2)
		   AND ($3 = '' OR category = $3)
		 ORDER BY risk_id
		 LIMIT $4`,
		after, string(status), category, limit)
	if err != nil {
		return nil, fmt.Errorf("list risks: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.Risk, 0, limit)
	for rows.Next() {
		r, err := scanRisk(rows)
		if err != nil {
			return nil, fmt.Errorf("scan risk: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate risks: %w", err)
	}
	return out, nil
}

// Update changes one or more non-lifecycle fields of patch under optimistic
// locking. Unlike Asset/Incident's Update (which COALESCE/CASE each column
// individually in SQL), this store merges patch onto a copy of the current
// row in Go and calls domain.Risk.Validate() on the WHOLE merged entity
// before writing it back — the same "merge then Validate the whole entity"
// shape SecurityRuleStore.Update uses, chosen here specifically because
// Category's format rule (domain.riskCategoryPattern) lives inside Validate
// and is not otherwise reachable from this package: re-deriving it here
// would be a second, driftable copy of the same rule.
//
// Category/Treatment/AssetID each follow the tri-state rule
// domain.RiskPatch's own doc comment states: nil leaves the field unchanged
// (copied from current before Validate runs); "" clears it; a non-empty
// value sets it (AssetID re-verified against this store's own tenant-scoped
// connection BEFORE the transaction begins, mirroring Asset/Incident's
// identical ordering).
//
// The pre-image is read under FOR UPDATE inside the same transaction as the
// write, so two concurrent PATCHes against the same risk serialise on its
// row instead of racing to compute a merge against a value the other has
// already superseded.
func (s *RiskStore) Update(
	ctx context.Context, riskID string, rowVersion int64, patch domain.RiskPatch,
) (*domain.Risk, error) {
	touchAsset := patch.AssetID != nil
	var verifiedAssetID *string
	if touchAsset {
		if trimmed := strings.TrimSpace(*patch.AssetID); trimmed != "" {
			if err := s.verifyAssetExists(ctx, trimmed); err != nil {
				return nil, err
			}
			verifiedAssetID = &trimmed
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	current, err := s.getForUpdateTx(ctx, tx, riskID)
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
	if patch.Category != nil {
		current.Category = strings.TrimSpace(*patch.Category)
	}
	if patch.Likelihood != nil {
		current.Likelihood = *patch.Likelihood
	}
	if patch.Impact != nil {
		current.Impact = *patch.Impact
	}
	if patch.Treatment != nil {
		if trimmed := strings.TrimSpace(*patch.Treatment); trimmed == "" {
			current.Treatment = nil
		} else {
			t := domain.RiskTreatment(trimmed)
			current.Treatment = &t
		}
	}
	if touchAsset {
		current.AssetID = verifiedAssetID
	}
	if err := current.Validate(); err != nil {
		return nil, err
	}

	var treatmentValue *string
	if current.Treatment != nil {
		v := string(*current.Treatment)
		treatmentValue = &v
	}

	row := tx.QueryRow(ctx, `
		UPDATE risk
		   SET title       = $3,
		       description = $4,
		       category    = $5,
		       likelihood  = $6,
		       impact      = $7,
		       treatment   = $8,
		       asset_id    = $9,
		       row_version = row_version + 1,
		       updated_at  = now()
		 WHERE risk_id = $1 AND row_version = $2
		RETURNING `+riskColumns,
		riskID, rowVersion, current.Title, current.Description, current.Category,
		string(current.Likelihood), string(current.Impact), treatmentValue, current.AssetID)

	updated, err := scanRisk(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, s.explainMissedUpdate(ctx, riskID)
	}
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("update risk: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return updated, nil
}

// explainMissedUpdate distinguishes "gone" from "stale version" after an
// UPDATE matched no row — mirrors VulnFindingStore.explainMissedUpdate.
func (s *RiskStore) explainMissedUpdate(ctx context.Context, riskID string) error {
	if _, err := s.Get(ctx, riskID); err != nil {
		return err
	}
	return domain.ErrVersionMismatch
}

// SetStatus performs a lifecycle transition under optimistic locking, guarded
// by domain.RiskStatus.CanTransitionTo — the single authority for status
// changes (mirrors VulnFindingStore.SetStatus/AssetStore.SetStatus). An
// illegal move returns *domain.RiskTransitionError (ErrInvalidTransition).
func (s *RiskStore) SetStatus(
	ctx context.Context, riskID string, rowVersion int64, status domain.RiskStatus,
) (*domain.Risk, error) {
	if !status.Valid() {
		return nil, domain.NewValidationError("status", "must be one of: open, mitigating, accepted, closed")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	current, err := s.getForUpdateTx(ctx, tx, riskID)
	if err != nil {
		return nil, err
	}
	if current.RowVersion != rowVersion {
		return nil, domain.ErrVersionMismatch
	}
	if !current.Status.CanTransitionTo(status) {
		return nil, domain.NewRiskTransitionError(current.Status, status)
	}

	row := tx.QueryRow(ctx, `
		UPDATE risk
		   SET status = $3, row_version = row_version + 1, updated_at = now()
		 WHERE risk_id = $1 AND row_version = $2
		RETURNING `+riskColumns,
		riskID, rowVersion, string(status))

	updated, err := scanRisk(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrVersionMismatch // unreachable under the FOR UPDATE lock; defensive only.
	}
	if err != nil {
		return nil, fmt.Errorf("set risk status: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return updated, nil
}

// riskLikelihoodRankSQL/riskImpactRankSQL are the SQL CASE expressions
// implementing the two 1-based rank tables domain.RiskRepository.Register's
// doc comment states, verbatim — this is the ONE place either rank table is
// expressed as SQL; Go only ever consumes the resulting integer, mirroring
// vulnFindingSeverityRankSQL/assetCriticalityRankSQL exactly.
const (
	riskLikelihoodRankSQL = `(CASE likelihood
		WHEN 'rare' THEN 1 WHEN 'unlikely' THEN 2 WHEN 'possible' THEN 3 WHEN 'likely' THEN 4 WHEN 'almost_certain' THEN 5
		ELSE 1 END)`
	riskImpactRankSQL = `(CASE impact
		WHEN 'negligible' THEN 1 WHEN 'minor' THEN 2 WHEN 'moderate' THEN 3 WHEN 'major' THEN 4 WHEN 'severe' THEN 5
		ELSE 1 END)`
)

// Register implements domain.RiskRepository.Register (E8.4a, ADR-SOC-008):
// the caller's OPEN (non-closed) risks ranked by the score
// domain.RiskRepository.Register's doc comment defines, computed entirely in
// this one query — never in Go, and never stored. Row-level security alone
// confines this to the caller's own tenant (no explicit tenant_id predicate),
// exactly like VulnFindingStore.Prioritized; the ORDER BY/LIMIT keep this a
// bounded TOP-N read regardless of how many risks the tenant has
// accumulated.
func (s *RiskStore) Register(ctx context.Context, limit int) ([]domain.RiskRegisterEntry, error) {
	limit = clampRiskRegisterLimit(limit)

	rows, err := s.pool.Query(ctx, `
		SELECT `+riskColumns+`, `+riskLikelihoodRankSQL+` * `+riskImpactRankSQL+` AS risk_score
		  FROM risk
		 WHERE status <> 'closed'
		 ORDER BY risk_score DESC, updated_at DESC, risk_id
		 LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list risk register: %w", err)
	}
	defer rows.Close()

	out := make([]domain.RiskRegisterEntry, 0, limit)
	for rows.Next() {
		var (
			r          domain.Risk
			likelihood string
			impact     string
			status     string
			treatment  *string
			score      int
		)
		if err := rows.Scan(
			&r.RiskID, &r.TenantID, &r.Title, &r.Description, &r.Category, &likelihood, &impact, &status,
			&treatment, &r.AssetID, &r.RowVersion, &r.CreatedAt, &r.UpdatedAt, &score,
		); err != nil {
			return nil, fmt.Errorf("scan risk register entry: %w", err)
		}
		r.Likelihood = domain.RiskLikelihood(likelihood)
		r.Impact = domain.RiskImpact(impact)
		r.Status = domain.RiskStatus(status)
		if treatment != nil {
			t := domain.RiskTreatment(*treatment)
			r.Treatment = &t
		}
		out = append(out, domain.RiskRegisterEntry{
			Risk:  r,
			Score: score,
			Band:  domain.RiskSeverityBandOf(score),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate risk register: %w", err)
	}
	return out, nil
}
