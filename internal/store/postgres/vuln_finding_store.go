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
	defaultVulnFindingPageSize = 50
	maxVulnFindingPageSize     = 500
)

const vulnFindingCols = `finding_id, tenant_id, asset_id, vuln_id, title, severity, status,
	first_seen, last_seen, scanner, description, row_version, created_at, updated_at`

// VulnFindingStore administers vuln_finding: batch scan-ingestion (UPSERT/
// dedup) plus lifecycle CRUD (E8.3a) — built EXCLUSIVELY over the
// tenant-scoped pool, mirroring SecurityRuleStore/IOCStore. Unlike those two
// CONFIG-ONLY tables, vuln_finding is a stateful entity with its own
// lifecycle, but the same tenancy shape applies: there is no privileged-pool
// counterpart in this story — prioritization and remediation->Incident
// linkage (E8.3b) are a later, separate story that may need one.
//
// vuln_finding is TENANT-OWNED — it is in TenantOwnedTables and carries
// row-level security — so this store takes no tenant argument anywhere; the
// bound connection already is the boundary (ADR-TENANCY-002).
type VulnFindingStore struct {
	pool *pgxpool.Pool
}

// NewVulnFindingStore builds the store over the given (tenant-scoped) pool.
func NewVulnFindingStore(pool *pgxpool.Pool) *VulnFindingStore {
	return &VulnFindingStore{pool: pool}
}

var _ domain.VulnFindingRepository = (*VulnFindingStore)(nil)

func clampVulnFindingPage(limit int) int {
	if limit <= 0 {
		return defaultVulnFindingPageSize
	}
	if limit > maxVulnFindingPageSize {
		return maxVulnFindingPageSize
	}
	return limit
}

func scanVulnFinding(s scanner) (*domain.VulnFinding, error) {
	var (
		f        domain.VulnFinding
		severity string
		status   string
	)
	if err := s.Scan(
		&f.FindingID, &f.TenantID, &f.AssetID, &f.VulnID, &f.Title, &severity, &status,
		&f.FirstSeen, &f.LastSeen, &f.Scanner, &f.Description, &f.RowVersion, &f.CreatedAt, &f.UpdatedAt,
	); err != nil {
		return nil, err
	}
	f.Severity = domain.VulnFindingSeverity(severity)
	f.Status = domain.VulnFindingStatus(status)
	return &f, nil
}

// Upsert validates every finding, re-verifies every referenced asset_id
// against this store's own tenant-scoped connection in one query (mirroring
// SecurityObservationStore.WriteObservations exactly), then writes the
// survivors with one INSERT ... ON CONFLICT DO UPDATE statement per finding
// that implements the exact rule domain.VulnFindingRepository.Upsert's doc
// comment states: a fresh row on first sight; last_seen refreshed and status
// forced back to open on an open/remediated/accepted_risk row; ONLY
// last_seen touched on a false_positive row. The CASE expressions read
// vuln_finding's OWN pre-update column values (PostgreSQL evaluates an
// UPDATE's SET list against the row being replaced), so the false-positive
// branch is enforced atomically against whatever the row's real status is at
// write time — not against a value read in an earlier, separate query that
// could have gone stale under concurrent ingestion.
func (s *VulnFindingStore) Upsert(ctx context.Context, findings []domain.VulnFinding) ([]domain.VulnFindingUpsertResult, error) {
	results := make([]domain.VulnFindingUpsertResult, len(findings))
	if len(findings) == 0 {
		return results, nil
	}

	assetIDSet := map[string]bool{}
	for i := range findings {
		if err := findings[i].Validate(); err != nil {
			results[i] = domain.VulnFindingUpsertResult{Reason: err.Error()}
			continue
		}
		assetIDSet[findings[i].AssetID] = true
	}
	if len(assetIDSet) == 0 {
		// Every finding failed validation; there is nothing left to check or
		// write.
		return results, nil
	}
	ids := make([]string, 0, len(assetIDSet))
	for id := range assetIDSet {
		ids = append(ids, id)
	}

	// RLS on asset filters this to the caller's own tenant already: an id
	// belonging to another tenant simply does not come back, indistinguishable
	// from one that does not exist at all — the correct answer either way.
	rows, err := s.pool.Query(ctx, `SELECT asset_id FROM asset WHERE asset_id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("check vuln finding asset ids: %w", err)
	}
	visible := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan vuln finding asset id: %w", err)
		}
		visible[id] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate vuln finding asset ids: %w", err)
	}
	rows.Close()

	batch := &pgx.Batch{}
	var queuedAt []int
	for i := range findings {
		if results[i].Reason != "" {
			continue // already rejected by validation, above
		}
		if !visible[findings[i].AssetID] {
			results[i] = domain.VulnFindingUpsertResult{Reason: "asset_id does not name an asset visible to this tenant"}
			continue
		}
		// finding_id ($1) is used only on the INSERT branch (a fresh row);
		// on a dedup conflict it is not touched by the UPDATE, so the
		// pre-existing row's own finding_id is what RETURNING reports back —
		// see VulnFindingUpsertResult.FindingID's doc comment. first_seen is
		// likewise absent from the UPDATE's SET list, so it survives
		// untouched across every reappearance.
		batch.Queue(`
			INSERT INTO vuln_finding
				(finding_id, tenant_id, asset_id, vuln_id, title, severity, status,
				 first_seen, last_seen, scanner, description, row_version, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,'open',now(),now(),$7,$8,1,now(),now())
			ON CONFLICT (tenant_id, asset_id, vuln_id) DO UPDATE SET
				last_seen   = now(),
				title       = CASE WHEN vuln_finding.status = 'false_positive' THEN vuln_finding.title       ELSE EXCLUDED.title       END,
				severity    = CASE WHEN vuln_finding.status = 'false_positive' THEN vuln_finding.severity    ELSE EXCLUDED.severity    END,
				scanner     = CASE WHEN vuln_finding.status = 'false_positive' THEN vuln_finding.scanner     ELSE EXCLUDED.scanner     END,
				description = CASE WHEN vuln_finding.status = 'false_positive' THEN vuln_finding.description ELSE EXCLUDED.description END,
				status      = CASE WHEN vuln_finding.status = 'false_positive' THEN 'false_positive'         ELSE 'open'              END,
				row_version = vuln_finding.row_version + 1,
				updated_at  = now()
			RETURNING `+vulnFindingCols,
			findings[i].FindingID, findings[i].TenantID, findings[i].AssetID, findings[i].VulnID,
			findings[i].Title, string(findings[i].Severity), findings[i].Scanner, findings[i].Description)
		queuedAt = append(queuedAt, i)
	}
	if len(queuedAt) == 0 {
		return results, nil
	}

	br := s.pool.SendBatch(ctx, batch)
	defer func() { _ = br.Close() }()
	for _, i := range queuedAt {
		f, err := scanVulnFinding(br.QueryRow())
		if err != nil {
			results[i] = domain.VulnFindingUpsertResult{Reason: fmt.Sprintf("write vuln finding: %v", err)}
			continue
		}
		results[i] = domain.VulnFindingUpsertResult{Accepted: true, FindingID: f.FindingID, Status: f.Status}
	}
	return results, nil
}

// Get returns a finding by identifier, or domain.ErrNotFound.
func (s *VulnFindingStore) Get(ctx context.Context, findingID string) (*domain.VulnFinding, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+vulnFindingCols+` FROM vuln_finding WHERE finding_id = $1`, findingID)
	f, err := scanVulnFinding(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get vuln finding: %w", err)
	}
	return f, nil
}

// List returns a page of the caller's findings, keyset-paginated over
// finding_id, filtered by assetID/status/severity when non-empty — mirrors
// IncidentRepository.List's filter shape.
func (s *VulnFindingStore) List(
	ctx context.Context, limit int, after, assetID string, status domain.VulnFindingStatus, severity domain.VulnFindingSeverity,
) ([]*domain.VulnFinding, error) {
	limit = clampVulnFindingPage(limit)
	rows, err := s.pool.Query(ctx, `
		SELECT `+vulnFindingCols+`
		  FROM vuln_finding
		 WHERE ($1 = '' OR finding_id > $1)
		   AND ($2 = '' OR asset_id = $2)
		   AND ($3 = '' OR status = $3)
		   AND ($4 = '' OR severity = $4)
		 ORDER BY finding_id
		 LIMIT $5`,
		after, assetID, string(status), string(severity), limit)
	if err != nil {
		return nil, fmt.Errorf("list vuln findings: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.VulnFinding, 0, limit)
	for rows.Next() {
		f, err := scanVulnFinding(rows)
		if err != nil {
			return nil, fmt.Errorf("scan vuln finding: %w", err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate vuln findings: %w", err)
	}
	return out, nil
}

// SetStatus performs a lifecycle transition under optimistic locking, guarded
// by domain.VulnFindingStatus.CanTransitionTo — the single authority for
// status changes, mirroring SecurityRuleStore.Update's row_version shape and
// IncidentStore.SetStatus's transition guard. This is the ONLY path that can
// move a finding out of false_positive — Upsert never does (see its own doc
// comment).
func (s *VulnFindingStore) SetStatus(
	ctx context.Context, findingID string, rowVersion int64, status domain.VulnFindingStatus,
) (*domain.VulnFinding, error) {
	if !status.Valid() {
		return nil, domain.NewValidationError("status", "must be one of: open, remediated, accepted_risk, false_positive")
	}
	current, err := s.Get(ctx, findingID)
	if err != nil {
		return nil, err
	}
	if current.RowVersion != rowVersion {
		return nil, domain.ErrVersionMismatch
	}
	if !current.Status.CanTransitionTo(status) {
		return nil, domain.NewVulnFindingTransitionError(current.Status, status)
	}

	row := s.pool.QueryRow(ctx, `
		UPDATE vuln_finding
		   SET status = $3, row_version = row_version + 1, updated_at = now()
		 WHERE finding_id = $1 AND row_version = $2
		RETURNING `+vulnFindingCols,
		findingID, rowVersion, string(status))

	updated, err := scanVulnFinding(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, s.explainMissedUpdate(ctx, findingID)
	}
	if err != nil {
		return nil, fmt.Errorf("set vuln finding status: %w", err)
	}
	return updated, nil
}

// explainMissedUpdate distinguishes "gone" from "stale version" after an
// UPDATE matched no row — mirrors SecurityRuleStore.explainMissedUpdate.
func (s *VulnFindingStore) explainMissedUpdate(ctx context.Context, findingID string) error {
	if _, err := s.Get(ctx, findingID); err != nil {
		return err
	}
	return domain.ErrVersionMismatch
}
