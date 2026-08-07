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

	// defaultVulnFindingPriorityLimit/maxVulnFindingPriorityLimit bound GET
	// /admin/vuln-findings/prioritized (E8.3b) — a ranked TOP-N projection,
	// clamped exactly like the page-size constants above.
	defaultVulnFindingPriorityLimit = 50
	maxVulnFindingPriorityLimit     = 500
)

const vulnFindingCols = `finding_id, tenant_id, asset_id, vuln_id, title, severity, status,
	first_seen, last_seen, scanner, description, remediation_incident_id, row_version, created_at, updated_at`

// VulnFindingStore administers vuln_finding: batch scan-ingestion (UPSERT/
// dedup), lifecycle CRUD (E8.3a), business-risk prioritization and
// remediation -> Incident linkage (E8.3b, ADR-SOC-007) — built EXCLUSIVELY
// over the tenant-scoped pool, mirroring SecurityRuleStore/IOCStore. Unlike
// those two CONFIG-ONLY tables, vuln_finding is a stateful entity with its
// own lifecycle, but the same tenancy shape applies: there is no
// privileged-pool counterpart — Remediate writes both vuln_finding AND
// incident from this SAME tenant-scoped connection in one transaction, which
// is sufficient because both tables carry FORCE ROW LEVEL SECURITY keyed off
// the identical `app.tenant_id` binding this pool's PrepareConn hook sets
// (postgres.NewTenantScopedPool), so no explicit tenant_id predicate is
// needed on either table (ADR-TENANCY-002).
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

func clampVulnFindingPriorityLimit(limit int) int {
	if limit <= 0 {
		return defaultVulnFindingPriorityLimit
	}
	if limit > maxVulnFindingPriorityLimit {
		return maxVulnFindingPriorityLimit
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
		&f.FirstSeen, &f.LastSeen, &f.Scanner, &f.Description, &f.RemediationIncidentID,
		&f.RowVersion, &f.CreatedAt, &f.UpdatedAt,
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

// vulnFindingJoinCols is vulnFindingCols qualified with the `vf` alias
// Prioritized's join uses, so its SELECT list can sit alongside a.criticality
// and the computed score without a naming collision.
const vulnFindingJoinCols = `vf.finding_id, vf.tenant_id, vf.asset_id, vf.vuln_id, vf.title, vf.severity, vf.status,
	vf.first_seen, vf.last_seen, vf.scanner, vf.description, vf.remediation_incident_id, vf.row_version,
	vf.created_at, vf.updated_at`

// vulnFindingSeverityRankSQL/assetCriticalityRankSQL are the SQL CASE
// expressions implementing the two 1-based rank tables
// domain.VulnFindingRepository.Prioritized's doc comment states, verbatim —
// this is the ONE place either rank table is expressed as SQL; Go only ever
// consumes the resulting integer.
const (
	vulnFindingSeverityRankSQL = `(CASE vf.severity
		WHEN 'none' THEN 1 WHEN 'low' THEN 2 WHEN 'medium' THEN 3 WHEN 'high' THEN 4 WHEN 'critical' THEN 5
		ELSE 1 END)`
	assetCriticalityRankSQL = `(CASE a.criticality
		WHEN 'unknown' THEN 1 WHEN 'low' THEN 2 WHEN 'medium' THEN 3 WHEN 'high' THEN 4 WHEN 'critical' THEN 5
		ELSE 1 END)`
)

// Prioritized implements domain.VulnFindingRepository.Prioritized (E8.3b,
// ADR-SOC-007): OPEN findings joined with their own asset's criticality,
// ranked by the score domain.VulnFindingRepository.Prioritized's doc comment
// defines, computed entirely in this one query — never in Go, and never
// stored. RLS on BOTH vuln_finding and asset supplies the tenant filter (no
// explicit tenant_id predicate, exactly like every other appPool-built read
// in this package); the ORDER BY/LIMIT keep this a bounded TOP-N read
// regardless of how many open findings the tenant has accumulated.
func (s *VulnFindingStore) Prioritized(ctx context.Context, limit int) ([]domain.PrioritizedVulnFinding, error) {
	limit = clampVulnFindingPriorityLimit(limit)

	rows, err := s.pool.Query(ctx, `
		SELECT `+vulnFindingJoinCols+`, a.criticality, `+vulnFindingSeverityRankSQL+` * `+assetCriticalityRankSQL+` AS priority_score
		  FROM vuln_finding vf
		  JOIN asset a ON a.asset_id = vf.asset_id
		 WHERE vf.status = 'open'
		 ORDER BY priority_score DESC, vf.last_seen DESC, vf.finding_id
		 LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list prioritized vuln findings: %w", err)
	}
	defer rows.Close()

	out := make([]domain.PrioritizedVulnFinding, 0, limit)
	for rows.Next() {
		var (
			f           domain.VulnFinding
			severity    string
			status      string
			criticality string
			score       int
		)
		if err := rows.Scan(
			&f.FindingID, &f.TenantID, &f.AssetID, &f.VulnID, &f.Title, &severity, &status,
			&f.FirstSeen, &f.LastSeen, &f.Scanner, &f.Description, &f.RemediationIncidentID, &f.RowVersion,
			&f.CreatedAt, &f.UpdatedAt, &criticality, &score,
		); err != nil {
			return nil, fmt.Errorf("scan prioritized vuln finding: %w", err)
		}
		f.Severity = domain.VulnFindingSeverity(severity)
		f.Status = domain.VulnFindingStatus(status)
		out = append(out, domain.PrioritizedVulnFinding{
			Finding:     f,
			Criticality: domain.AssetCriticality(criticality),
			Score:       score,
			Priority:    domain.VulnFindingPriorityOf(score),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate prioritized vuln findings: %w", err)
	}
	return out, nil
}

// vulnFindingSeverityToIncidentSeverity maps the CVSS-derived
// domain.VulnFindingSeverity onto domain.IncidentSeverity for the Incident
// Remediate opens — the two are DIFFERENT closed vocabularies (see
// domain.VulnFindingSeverity's own doc comment for why), so this is an
// explicit, one-directional, lossy translation, not a shared type. none has
// no IncidentSeverity counterpart (an Incident is opened because something is
// known to be wrong; see domain.IncidentSeverity's own doc comment for why it
// carries no "none" member) — it maps to IncidentSeverityLow, the least
// severe defined tier, rather than silently defaulting to a MORE severe one.
func vulnFindingSeverityToIncidentSeverity(sev domain.VulnFindingSeverity) domain.IncidentSeverity {
	switch sev {
	case domain.VulnFindingSeverityCritical:
		return domain.IncidentSeverityCritical
	case domain.VulnFindingSeverityHigh:
		return domain.IncidentSeverityHigh
	case domain.VulnFindingSeverityMedium:
		return domain.IncidentSeverityMedium
	default: // low, none
		return domain.IncidentSeverityLow
	}
}

// remediationIncidentTitle/remediationIncidentDescription build the Incident
// Remediate opens from the finding it tracks — bounded by
// domain.MaxIncidentTitleLength/MaxIncidentDescriptionLength via
// domain.NewVulnRemediationIncident's own Validate call, exactly like every
// other constructor in this codebase.
func remediationIncidentTitle(f *domain.VulnFinding) string {
	return fmt.Sprintf("Remediate %s (%s) on asset %s", f.VulnID, f.Severity, f.AssetID)
}

func remediationIncidentDescription(f *domain.VulnFinding) string {
	return fmt.Sprintf(
		"Tracked remediation for vulnerability finding %s: vuln_id=%s severity=%s scanner=%s asset_id=%s",
		f.FindingID, f.VulnID, f.Severity, f.Scanner, f.AssetID)
}

// Remediate implements domain.VulnFindingRepository.Remediate (E8.3b,
// ADR-SOC-007) — see that method's doc comment for the full contract
// (idempotency, the row_version guard and exactly how concurrent calls are
// serialized).
func (s *VulnFindingStore) Remediate(
	ctx context.Context, findingID string, rowVersion int64,
) (*domain.VulnFinding, *domain.Incident, error) {
	actor, ok := domain.ActorFrom(ctx)
	if !ok {
		return nil, nil, domain.ErrNoActor
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Holding this lock for the rest of the transaction is what makes two
	// concurrent Remediate calls on the SAME finding fully serialize — see
	// the interface doc comment's "Concurrency guard" paragraph.
	row := tx.QueryRow(ctx, `SELECT `+vulnFindingCols+` FROM vuln_finding WHERE finding_id = $1 FOR UPDATE`, findingID)
	current, err := scanVulnFinding(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("get vuln finding for update: %w", err)
	}
	if current.RowVersion != rowVersion {
		// Fails BEFORE any Incident is even considered: a racing caller that
		// lost the lock above now sees a stale version and stops here, never
		// reaching the create branch below — this, not a database constraint,
		// is what prevents two concurrent callers from both creating an
		// Incident (see the interface doc comment).
		return nil, nil, domain.ErrVersionMismatch
	}
	if current.Status != domain.VulnFindingOpen {
		return nil, nil, domain.ErrVulnFindingNotOpen
	}

	if current.RemediationIncidentID != nil {
		existingRow := tx.QueryRow(ctx, `SELECT `+incidentColumns+` FROM incident WHERE incident_id = $1 FOR UPDATE`,
			*current.RemediationIncidentID)
		existing, existingErr := scanIncident(existingRow)
		switch {
		case existingErr == nil && existing.Open():
			// Idempotent: nothing to write. Returning here lets the deferred
			// Rollback discard the FOR UPDATE locks; there is nothing to
			// commit.
			return current, existing, nil
		case existingErr != nil && !errors.Is(existingErr, pgx.ErrNoRows):
			return nil, nil, fmt.Errorf("get linked remediation incident: %w", existingErr)
		}
		// Otherwise: the linked incident has since closed (or, defensively,
		// no longer exists) — fall through and open a fresh one, exactly as
		// "no OPEN remediation linked" would.
	}

	// current.AssetID needs no fresh existence probe here (unlike a
	// caller-supplied asset_id elsewhere in this codebase): vuln_finding.
	// asset_id is `ON DELETE CASCADE` (20260909000001_vuln_finding.sql), so
	// this finding row cannot still exist while naming a hard-deleted asset
	// — if the asset were gone, so would current itself, and the SELECT ...
	// FOR UPDATE above would have returned ErrNotFound already. The INSERT's
	// own isForeignKeyViolation fallback below remains as belt-and-suspenders.
	inc, err := domain.NewVulnRemediationIncident(current.TenantID,
		remediationIncidentTitle(current), remediationIncidentDescription(current),
		vulnFindingSeverityToIncidentSeverity(current.Severity), current.AssetID)
	if err != nil {
		return nil, nil, err
	}

	insertRow := tx.QueryRow(ctx, `
		INSERT INTO incident (incident_id, tenant_id, title, description, severity, status, source, asset_id)
		VALUES ($1, $2, $3, $4, $5, $6, 'vuln', $7)
		RETURNING `+incidentColumns,
		inc.IncidentID, inc.TenantID, inc.Title, inc.Description, string(inc.Severity), string(inc.Status), inc.AssetID)
	created, err := scanIncident(insertRow)
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, nil, domain.ErrNotFound
		}
		return nil, nil, fmt.Errorf("insert remediation incident: %w", err)
	}

	// Reuses IncidentStore's own recordEvent rather than a second, duplicate
	// INSERT INTO incident_event: one append path for the table, not two —
	// see recordEvent's own doc comment and ADR-AUDIT-006 (this same
	// transaction's `FOR UPDATE` above is what serialises it on the chain
	// head; arch.TestEveryAuditAppendPath_SerialisesOnItsChainHead verifies
	// every caller of recordEvent, including this one, reaches it).
	if err := NewIncidentStore(s.pool).recordEvent(ctx, tx, created.TenantID, created.IncidentID,
		domain.IncidentEventCreated, "", nil, nil, actor, created.RowVersion); err != nil {
		return nil, nil, err
	}

	updatedRow := tx.QueryRow(ctx, `
		UPDATE vuln_finding
		   SET remediation_incident_id = $3, row_version = row_version + 1, updated_at = now()
		 WHERE finding_id = $1 AND row_version = $2
		RETURNING `+vulnFindingCols,
		findingID, rowVersion, created.IncidentID)
	updatedFinding, err := scanVulnFinding(updatedRow)
	if errors.Is(err, pgx.ErrNoRows) {
		// Unreachable under the FOR UPDATE lock held since this transaction's
		// first statement — nobody else could have moved row_version between
		// the check above and here. Defensive only, mirroring
		// explainMissedUpdate's own belt-and-suspenders elsewhere in this
		// file.
		return nil, nil, domain.ErrVersionMismatch
	}
	if err != nil {
		return nil, nil, fmt.Errorf("link remediation incident: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, fmt.Errorf("commit: %w", err)
	}
	return updatedFinding, created, nil
}
