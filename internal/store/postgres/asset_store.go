package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
)

const (
	assetColumns = `asset_id, tenant_id, type, name, attributes, status,
		environment, criticality, owner_team_id, owner_user_id,
		source, external_ref, last_seen,
		row_version, created_at, updated_at`
	assetRelColumns    = `relationship_id, tenant_id, from_asset_id, to_asset_id, type, row_version, created_at, updated_at`
	assetChangeColumns = `change_id, tenant_id, asset_id, kind, field, old_value, new_value, actor, row_version, occurred_at`
)

// maxAssetDuplicateRows bounds the read-only duplicate-detection projection
// (Duplicates): a CMDB with an unbounded number of candidates would turn a
// GET into an unbounded scan/result (docs/PLATFORM-BUILD-PLAN.md §3). It is
// a report, not a paginated list — a caller with more candidates than this
// re-runs it after reconciling the first batch (E1.4b owns actual merge).
const maxAssetDuplicateRows = 2000

// defaultAssetPageSize bounds an unbounded List; maxAssetPageSize caps what a
// caller may ask for. Same shape as the other tenant-owned stores.
const (
	defaultAssetPageSize = 50
	maxAssetPageSize     = 500
)

// AssetStore administers the CMDB: assets and the relationships between them.
//
// asset and asset_relationship are TENANT-OWNED — both are in
// TenantOwnedTables and carry row-level security — so this store is built
// from the tenant-scoped pool and takes no tenant argument anywhere: the
// bound connection is the boundary. A caller cannot name another tenant's
// asset, because the policy makes the row invisible rather than because a
// predicate remembered to exclude it (ADR-TENANCY-002).
//
// Mutations here do NOT pass through withAdminAudit — see the doc comment on
// domain.AssetRepository for why: ADR-AUDIT-007 §6.2 scopes that chokepoint to
// five named identity-governance tables, and Asset is not one of them.
type AssetStore struct {
	pool *pgxpool.Pool
}

// NewAssetStore builds the asset repository over the given pool.
func NewAssetStore(pool *pgxpool.Pool) *AssetStore {
	return &AssetStore{pool: pool}
}

var _ domain.AssetRepository = (*AssetStore)(nil)

func clampAssetPage(limit int) int {
	if limit <= 0 {
		return defaultAssetPageSize
	}
	if limit > maxAssetPageSize {
		return maxAssetPageSize
	}
	return limit
}

// getForUpdateTx reads assetID's current row under FOR UPDATE, inside tx, so
// the caller holds the row lock for the rest of its transaction. Used by
// Update and SetStatus to read the pre-image they diff against: serialising
// on the mutated row itself is this table's equivalent of ADR-AUDIT-006's
// obligation to serialise an append on its chain head, since a change-history
// row is written from exactly this read in the same transaction.
func (s *AssetStore) getForUpdateTx(ctx context.Context, tx pgx.Tx, assetID string) (*domain.Asset, error) {
	row := tx.QueryRow(ctx, `SELECT `+assetColumns+` FROM asset WHERE asset_id = $1 FOR UPDATE`, assetID)
	a, err := scanAsset(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get asset for update: %w", err)
	}
	return a, nil
}

func scanAsset(s scanner) (*domain.Asset, error) {
	var (
		a           domain.Asset
		status      string
		environment string
		criticality string
		attrsBytes  []byte
	)
	if err := s.Scan(
		&a.AssetID, &a.TenantID, &a.Type, &a.Name, &attrsBytes, &status,
		&environment, &criticality, &a.OwnerTeamID, &a.OwnerUserID,
		&a.Source, &a.ExternalRef, &a.LastSeen,
		&a.RowVersion, &a.CreatedAt, &a.UpdatedAt,
	); err != nil {
		return nil, err
	}
	a.Status = domain.AssetStatus(status)
	a.Environment = domain.AssetEnvironment(environment)
	a.Criticality = domain.AssetCriticality(criticality)
	a.Attributes = map[string]any{}
	if len(attrsBytes) > 0 {
		if err := json.Unmarshal(attrsBytes, &a.Attributes); err != nil {
			return nil, fmt.Errorf("unmarshal asset attributes: %w", err)
		}
	}
	return &a, nil
}

func scanAssetRelationship(s scanner) (*domain.AssetRelationship, error) {
	var (
		r    domain.AssetRelationship
		kind string
	)
	if err := s.Scan(
		&r.RelationshipID, &r.TenantID, &r.FromAssetID, &r.ToAssetID, &kind,
		&r.RowVersion, &r.CreatedAt, &r.UpdatedAt,
	); err != nil {
		return nil, err
	}
	r.Type = domain.RelationshipType(kind)
	return &r, nil
}

// Create inserts an asset and records the single AssetChangeCreated history
// row in the same transaction. When a.OwnerTeamID/a.OwnerUserID is set, both
// are re-verified against this store's own tenant-scoped connection before
// the INSERT runs — see verifyOwnerTeamExists/verifyOwnerUserExists.
func (s *AssetStore) Create(ctx context.Context, a *domain.Asset) (*domain.Asset, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}
	actor, ok := domain.ActorFrom(ctx)
	if !ok {
		return nil, domain.ErrNoActor
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	created, err := s.insertAssetRowTx(ctx, tx, a)
	if err != nil {
		return nil, err
	}
	if err := s.recordChange(ctx, tx, created.TenantID, created.AssetID,
		domain.AssetChangeCreated, "", nil, nil, actor, created.RowVersion); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return created, nil
}

// insertAssetRowTx performs only the INSERT itself (validation, owner
// re-verification, the row, its error mapping) inside an already-open
// transaction — it writes no history row. It is the shared core of Create
// (which records AssetChangeCreated itself, immediately after, in its own
// body) and Upsert's not-found branch (which does the same in ITS own body).
// Splitting the INSERT out from the recordChange call, instead of sharing a
// single helper that does both, keeps every recordChange call site inside a
// function whose own transaction demonstrably serialises on a lock —
// Create's on nothing (a brand-new asset_id can never collide with a
// concurrent Create, which mints its own asset_id), Upsert's on the
// SELECT ... FOR UPDATE a few lines above it — rather than hiding that
// property behind a third function name (ADR-AUDIT-006; see
// arch.TestEveryAuditAppendPath_SerialisesOnItsChainHead).
func (s *AssetStore) insertAssetRowTx(ctx context.Context, tx pgx.Tx, a *domain.Asset) (*domain.Asset, error) {
	if !a.Status.ValidInitialStatus() {
		return nil, domain.NewValidationError("status",
			"must be one of: planned, active at creation; maintenance and retired are reached by transition")
	}
	if a.OwnerTeamID != nil {
		if err := s.verifyOwnerTeamExists(ctx, *a.OwnerTeamID); err != nil {
			return nil, err
		}
	}
	if a.OwnerUserID != nil {
		if err := s.verifyOwnerUserExists(ctx, *a.OwnerUserID); err != nil {
			return nil, err
		}
	}
	attrs, err := json.Marshal(a.Attributes)
	if err != nil {
		return nil, fmt.Errorf("marshal asset attributes: %w", err)
	}

	// last_seen = now(): a Create — direct or via Upsert's not-found branch —
	// is itself the first confirmation of this CI (E1.5), regardless of
	// anything a.LastSeen carries (there is no request field for it — see
	// Asset.LastSeen's doc comment).
	row := tx.QueryRow(ctx, `
		INSERT INTO asset (asset_id, tenant_id, type, name, attributes, status, environment, criticality, owner_team_id, owner_user_id, source, external_ref, last_seen)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, now())
		RETURNING `+assetColumns,
		a.AssetID, a.TenantID, a.Type, a.Name, attrs, string(a.Status),
		string(a.Environment), string(a.Criticality), a.OwnerTeamID, a.OwnerUserID,
		a.Source, a.ExternalRef)

	created, err := scanAsset(row)
	if err != nil {
		switch {
		case isUniqueViolation(err):
			// Covers both asset_id (never observed in practice — a ULID
			// collision) and a race on ux_asset_tenant_source_external_ref:
			// a concurrent importer inserting the same (tenant, source,
			// external_ref) between Upsert's SELECT ... FOR UPDATE finding
			// nothing and this INSERT running. The database, not application
			// logic that merely checked first, is what makes this refusal
			// certain (E1.4).
			return nil, domain.ErrConflict
		case isForeignKeyViolation(err):
			// Belt-and-suspenders against a TOCTOU between the existence
			// check above and this INSERT (e.g. the owner was deleted or
			// its membership revoked in between) — never observed in
			// practice for team/app_user, which are never hard-deleted, but
			// this is the same defensive branch CreateRelationship already
			// takes for asset_relationship's own foreign keys.
			return nil, domain.ErrNotFound
		default:
			return nil, fmt.Errorf("insert asset: %w", err)
		}
	}
	return created, nil
}

// Upsert inserts or updates an asset by (tenant, source, external_ref) — see
// domain.AssetRepository.Upsert's doc comment for the full match/replace/
// history contract this implements.
func (s *AssetStore) Upsert(ctx context.Context, a *domain.Asset) (*domain.Asset, bool, error) {
	if err := a.Validate(); err != nil {
		return nil, false, err
	}
	actor, ok := domain.ActorFrom(ctx)
	if !ok {
		return nil, false, domain.ErrNoActor
	}

	// No natural key to match against: this row is created every time it is
	// imported, exactly as a caller of Create already gets.
	if a.ExternalRef == nil {
		created, err := s.Create(ctx, a)
		return created, true, err
	}

	if a.OwnerTeamID != nil {
		if err := s.verifyOwnerTeamExists(ctx, *a.OwnerTeamID); err != nil {
			return nil, false, err
		}
	}
	if a.OwnerUserID != nil {
		if err := s.verifyOwnerUserExists(ctx, *a.OwnerUserID); err != nil {
			return nil, false, err
		}
	}
	attrs, err := json.Marshal(a.Attributes)
	if err != nil {
		return nil, false, fmt.Errorf("marshal asset attributes: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// FOR UPDATE holds the row lock for the rest of this transaction —
	// exactly Update/SetStatus's own reasoning (see getForUpdateTx) — so a
	// second concurrent import of the same (tenant, source, external_ref)
	// serialises on this row rather than racing to decide "create or
	// update" against a value the other has already superseded.
	row := tx.QueryRow(ctx,
		`SELECT `+assetColumns+` FROM asset WHERE source = $1 AND external_ref = $2 FOR UPDATE`,
		a.Source, *a.ExternalRef)
	existing, ferr := scanAsset(row)
	if errors.Is(ferr, pgx.ErrNoRows) {
		created, err := s.insertAssetRowTx(ctx, tx, a)
		if err != nil {
			return nil, false, err
		}
		if err := s.recordChange(ctx, tx, created.TenantID, created.AssetID,
			domain.AssetChangeCreated, "", nil, nil, actor, created.RowVersion); err != nil {
			return nil, false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, false, fmt.Errorf("commit: %w", err)
		}
		return created, true, nil
	}
	if ferr != nil {
		return nil, false, fmt.Errorf("find asset for upsert: %w", ferr)
	}

	// The diff is computed BEFORE any write, against the full-replace
	// semantics domain.AssetRepository.Upsert documents: the import row is
	// the source system's current view of this CI, so every field it
	// carries is compared to the stored value as if it were about to
	// overwrite it. Status and the (source, external_ref) identity itself
	// are deliberately excluded: a bulk import is not a lifecycle authority
	// (E1.3), and the identity is what selected this row in the first place.
	existingAttrsJSON, jerr := json.Marshal(existing.Attributes)
	if jerr != nil {
		return nil, false, fmt.Errorf("marshal existing asset attributes: %w", jerr)
	}
	existingAttrsStr, newAttrsStr := string(existingAttrsJSON), string(attrs)

	var changed []assetFieldChange
	for _, c := range []assetFieldChange{
		{"type", strPtr(existing.Type), strPtr(a.Type)},
		{"name", strPtr(existing.Name), strPtr(a.Name)},
		{"attributes", &existingAttrsStr, &newAttrsStr},
		{"environment", strPtr(string(existing.Environment)), strPtr(string(a.Environment))},
		{"criticality", strPtr(string(existing.Criticality)), strPtr(string(a.Criticality))},
		{"owner_team_id", existing.OwnerTeamID, a.OwnerTeamID},
		{"owner_user_id", existing.OwnerUserID, a.OwnerUserID},
	} {
		if !strPtrEqual(c.old, c.new) {
			changed = append(changed, c)
		}
	}

	// THE idempotency guarantee: re-importing an identical payload finds no
	// SUBSTANTIVE field changed, so no history is written, and neither
	// row_version nor updated_at moves. LastSeen still moves (E1.5): a
	// re-import genuinely re-confirms the CI is still there, so an operator
	// running a health report right after a no-op re-import sweep must not
	// see it as stale — see AssetRepository.Upsert's doc comment for why
	// this does not weaken the guarantee above. The UPDATE below names ONLY
	// last_seen for exactly that reason: it cannot bump row_version, touch
	// updated_at or trigger a history write, because it does not go near any
	// of the columns those depend on.
	if len(changed) == 0 {
		touchedRow := tx.QueryRow(ctx, `
			UPDATE asset SET last_seen = now() WHERE asset_id = $1
			RETURNING `+assetColumns, existing.AssetID)
		touched, terr := scanAsset(touchedRow)
		if terr != nil {
			return nil, false, fmt.Errorf("touch asset last_seen (upsert no-op): %w", terr)
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, false, fmt.Errorf("commit: %w", err)
		}
		return touched, false, nil
	}

	updateRow := tx.QueryRow(ctx, `
		UPDATE asset
		   SET type          = $2,
		       name          = $3,
		       attributes    = $4,
		       environment   = $5,
		       criticality   = $6,
		       owner_team_id = $7,
		       owner_user_id = $8,
		       last_seen     = now(),
		       row_version   = row_version + 1,
		       updated_at    = now()
		 WHERE asset_id = $1
		RETURNING `+assetColumns,
		existing.AssetID, a.Type, a.Name, attrs, string(a.Environment), string(a.Criticality),
		a.OwnerTeamID, a.OwnerUserID)
	updated, uerr := scanAsset(updateRow)
	if uerr != nil {
		if isForeignKeyViolation(uerr) {
			return nil, false, domain.ErrNotFound
		}
		return nil, false, fmt.Errorf("update asset (upsert): %w", uerr)
	}

	for _, c := range changed {
		if err := s.recordChange(ctx, tx, updated.TenantID, updated.AssetID,
			domain.AssetChangeUpdated, c.field, c.old, c.new, actor, updated.RowVersion); err != nil {
			return nil, false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("commit: %w", err)
	}
	return updated, false, nil
}

// recordChange appends one asset_change_history row inside tx, so it commits
// or aborts with the mutation it describes. oldValue/newValue are nil for
// AssetChangeCreated (there is no prior state to diff).
func (s *AssetStore) recordChange(
	ctx context.Context, tx pgx.Tx, tenantID, assetID string,
	kind domain.AssetChangeKind, field string, oldValue, newValue *string,
	actor string, rowVersion int64,
) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO asset_change_history
			(change_id, tenant_id, asset_id, kind, field, old_value, new_value, actor, row_version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		domain.NewID(), tenantID, assetID, string(kind), field, oldValue, newValue, actor, rowVersion)
	if err != nil {
		return fmt.Errorf("record asset change history: %w", err)
	}
	return nil
}

// verifyOwnerTeamExists confirms teamID names a team visible to this store's
// own tenant-scoped, RLS-enforced connection before an asset's
// owner_team_id is written. team.tenant_id is enforced by row-level security
// exactly like asset's is, but PostgreSQL's foreign-key trigger on
// asset.owner_team_id REFERENCES team(team_id) runs with the constraint's
// own privileges and BYPASSES that policy — the same documented PostgreSQL
// behaviour ADR-ASSET-001 §6 records for asset_relationship's endpoints,
// applied here to a second foreign key. A cross-tenant or nonexistent id is
// filtered out by the same policy every other read in this store is, and
// reports ErrNotFound either way — indistinguishable from an id that does
// not exist at all, which is the correct answer either way.
func (s *AssetStore) verifyOwnerTeamExists(ctx context.Context, teamID string) error {
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM team WHERE team_id = $1)`, teamID,
	).Scan(&exists); err != nil {
		return fmt.Errorf("check owner team exists: %w", err)
	}
	if !exists {
		return domain.ErrNotFound
	}
	return nil
}

// verifyOwnerUserExists confirms userID names a person this tenant actually
// knows before an asset's owner_user_id is written. app_user itself carries
// no tenant_id — it is GLOBAL by decision, because a person exists before
// and independently of any membership (ADR-IDENTITY-002 §3.1) — so "visible
// to this tenant" is not a fact app_user's own table can answer at all;
// membership is the tenant-owned, row-level-secured table that answers it
// (an active membership row is proof this tenant knows the user). Without
// this check, naming an arbitrary platform-wide user id as an owner would
// record ownership by a person this tenant has never invited, which the
// owner_user_id -> app_user foreign key alone would happily accept. A user
// with no active membership reports ErrNotFound, indistinguishable from a
// user id that does not exist at all.
func (s *AssetStore) verifyOwnerUserExists(ctx context.Context, userID string) error {
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM membership WHERE user_id = $1 AND status = 'active')`, userID,
	).Scan(&exists); err != nil {
		return fmt.Errorf("check owner user exists: %w", err)
	}
	if !exists {
		return domain.ErrNotFound
	}
	return nil
}

// Get returns an asset by identifier.
func (s *AssetStore) Get(ctx context.Context, assetID string) (*domain.Asset, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+assetColumns+` FROM asset WHERE asset_id = $1`, assetID)
	a, err := scanAsset(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get asset: %w", err)
	}
	return a, nil
}

// List returns a page of the caller's assets, keyset-paginated over
// asset_id — a ULID, and therefore ordered by creation.
//
// status is the E1.3 soft-retire filter. The zero value excludes
// AssetRetired (a retired CI is soft-deleted from the default view, per
// ix_asset_tenant_status_not_retired) but includes every other status; a
// caller that explicitly wants retired assets — or wants only one other
// status — passes it. A retired asset is always individually reachable
// through Get regardless of this filter.
func (s *AssetStore) List(ctx context.Context, limit int, after string, status domain.AssetStatus) ([]*domain.Asset, error) {
	limit = clampAssetPage(limit)
	if status != "" && !status.Valid() {
		return nil, domain.NewValidationError("status", "must be one of: planned, active, maintenance, retired")
	}

	var (
		rows pgx.Rows
		err  error
	)
	if status == "" {
		rows, err = s.pool.Query(ctx, `
			SELECT `+assetColumns+`
			  FROM asset
			 WHERE status <> 'retired' AND ($1 = '' OR asset_id > $1)
			 ORDER BY asset_id
			 LIMIT $2`, after, limit)
	} else {
		rows, err = s.pool.Query(ctx, `
			SELECT `+assetColumns+`
			  FROM asset
			 WHERE status = $1 AND ($2 = '' OR asset_id > $2)
			 ORDER BY asset_id
			 LIMIT $3`, string(status), after, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("list assets: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.Asset, 0, limit)
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			return nil, fmt.Errorf("scan asset: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate assets: %w", err)
	}
	return out, nil
}

// Update changes one or more non-lifecycle fields of patch under optimistic
// locking, recording one AssetChangeUpdated history row per field actually
// changed. A nil pointer/map leaves that field unchanged — COALESCE against a
// NULL bind parameter, the same way a caller signals "not supplied" rather
// than "clear it". OwnerTeamID/OwnerUserID are tri-state (see
// domain.AssetPatch's doc comment): untouched, cleared to NULL, or set to a
// new id — a new id is re-verified against this store's own tenant-scoped
// connection exactly as Create re-verifies one, before the UPDATE runs.
//
// Status is deliberately not a field here (E1.3): see SetStatus.
//
// The old values needed to diff are read under FOR UPDATE inside the same
// transaction as the write — see getForUpdateTx's doc comment for why.
func (s *AssetStore) Update(
	ctx context.Context, assetID string, rowVersion int64, patch domain.AssetPatch,
) (*domain.Asset, error) {
	actor, ok := domain.ActorFrom(ctx)
	if !ok {
		return nil, domain.ErrNoActor
	}

	var namePtr *string
	if patch.Name != nil {
		trimmed := strings.TrimSpace(*patch.Name)
		if trimmed == "" {
			return nil, domain.NewValidationError("name", "must not be empty")
		}
		if len(trimmed) > domain.MaxAssetNameLength {
			return nil, domain.NewValidationError("name", "must be at most 200 characters")
		}
		namePtr = &trimmed
	}

	var envPtr *string
	if patch.Environment != nil {
		if !patch.Environment.Valid() {
			return nil, domain.NewValidationError("environment", "must be one of: production, staging, development, unknown")
		}
		v := string(*patch.Environment)
		envPtr = &v
	}

	var critPtr *string
	if patch.Criticality != nil {
		if !patch.Criticality.Valid() {
			return nil, domain.NewValidationError("criticality", "must be one of: critical, high, medium, low, unknown")
		}
		v := string(*patch.Criticality)
		critPtr = &v
	}

	var attrsJSON string
	haveAttrs := patch.Attributes != nil
	if haveAttrs {
		b, err := json.Marshal(patch.Attributes)
		if err != nil {
			return nil, fmt.Errorf("marshal asset attributes: %w", err)
		}
		attrsJSON = string(b)
	}

	// touchOwnerTeam/touchOwnerUser is false when the patch field is nil
	// ("leave unchanged"); true with a nil value clears the owner; true with
	// a non-nil value sets it, re-verified first.
	touchOwnerTeam := patch.OwnerTeamID != nil
	var ownerTeamValue *string
	if touchOwnerTeam {
		if trimmed := strings.TrimSpace(*patch.OwnerTeamID); trimmed != "" {
			if err := s.verifyOwnerTeamExists(ctx, trimmed); err != nil {
				return nil, err
			}
			ownerTeamValue = &trimmed
		}
	}
	touchOwnerUser := patch.OwnerUserID != nil
	var ownerUserValue *string
	if touchOwnerUser {
		if trimmed := strings.TrimSpace(*patch.OwnerUserID); trimmed != "" {
			if err := s.verifyOwnerUserExists(ctx, trimmed); err != nil {
				return nil, err
			}
			ownerUserValue = &trimmed
		}
	}

	var attrsParam []byte
	if haveAttrs {
		attrsParam = []byte(attrsJSON)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The pre-image is read under FOR UPDATE, inside this transaction, not by
	// a separate s.Get before it begins. This is the same obligation
	// ADR-AUDIT-006 imposes on audit_event/admin_audit_event's chain head,
	// applied to this table's own equivalent of one: two concurrent PATCHes
	// against the same asset now serialise on its row instead of racing to
	// compute a diff against a value the other has already superseded, and
	// the guarded UPDATE below is therefore certain to match once the
	// version check passes — nothing else can touch this row until commit.
	current, err := s.getForUpdateTx(ctx, tx, assetID)
	if err != nil {
		return nil, err
	}
	if current.RowVersion != rowVersion {
		return nil, domain.ErrVersionMismatch
	}

	// last_seen = now(): an operator editing a CI by hand is themselves
	// confirming it (E1.5) — set unconditionally, the same as updated_at,
	// never through recordChange (see AssetRepository.Update's doc comment).
	row := tx.QueryRow(ctx, `
		UPDATE asset
		   SET name          = COALESCE($3, name),
		       attributes    = COALESCE($4, attributes),
		       environment   = COALESCE($5, environment),
		       criticality   = COALESCE($6, criticality),
		       owner_team_id = CASE WHEN $7 THEN $8  ELSE owner_team_id END,
		       owner_user_id = CASE WHEN $9 THEN $10 ELSE owner_user_id END,
		       last_seen     = now(),
		       row_version   = row_version + 1,
		       updated_at    = now()
		 WHERE asset_id = $1 AND row_version = $2
		RETURNING `+assetColumns,
		assetID, rowVersion, namePtr, attrsParam, envPtr, critPtr,
		touchOwnerTeam, ownerTeamValue, touchOwnerUser, ownerUserValue)

	updated, err := scanAsset(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, s.explainMissedUpdate(ctx, assetID)
	}
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("update asset: %w", err)
	}

	// changes lists only the fields the caller actually supplied — name/
	// environment/criticality are added only when their patch pointer was
	// non-nil, and the owner fields only when touched. This matters because
	// nil means two different things for these groups: for name/environment/
	// criticality nil means "not supplied" (skip); for an owner field that
	// WAS touched, nil means "clear it" (a real, recordable change). Gating
	// membership in the slice on "was this field supplied at all" — rather
	// than on whether the resulting pointer is nil — keeps both meanings
	// correct without special-casing the owner fields in the loop below.
	var changes []assetFieldChange
	if patch.Name != nil {
		changes = append(changes, assetFieldChange{"name", strPtr(current.Name), namePtr})
	}
	if patch.Environment != nil {
		changes = append(changes, assetFieldChange{"environment", strPtr(string(current.Environment)), envPtr})
	}
	if patch.Criticality != nil {
		changes = append(changes, assetFieldChange{"criticality", strPtr(string(current.Criticality)), critPtr})
	}
	if haveAttrs {
		currentAttrsJSON, err := json.Marshal(current.Attributes)
		if err != nil {
			return nil, fmt.Errorf("marshal current asset attributes: %w", err)
		}
		changes = append(changes, assetFieldChange{"attributes", strPtr(string(currentAttrsJSON)), &attrsJSON})
	}
	if touchOwnerTeam {
		changes = append(changes, assetFieldChange{"owner_team_id", current.OwnerTeamID, ownerTeamValue})
	}
	if touchOwnerUser {
		changes = append(changes, assetFieldChange{"owner_user_id", current.OwnerUserID, ownerUserValue})
	}
	for _, c := range changes {
		if strPtrEqual(c.old, c.new) {
			continue
		}
		if err := s.recordChange(ctx, tx, updated.TenantID, assetID,
			domain.AssetChangeUpdated, c.field, c.old, c.new, actor, updated.RowVersion); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return updated, nil
}

// assetFieldChange is one candidate history row Update may write: a field
// name and its old/new value, both as *string so a nil owner reference and
// an absent field are representable uniformly.
type assetFieldChange struct {
	field    string
	old, new *string
}

// strPtr returns a pointer to s. A small helper so assetFieldChange can
// treat every field uniformly as *string, including ones the domain models
// as a distinct type (AssetEnvironment, AssetCriticality).
func strPtr(s string) *string { return &s }

// strPtrEqual reports whether two optional strings hold the same value, nil
// included — used to skip writing a history row for a field the caller
// "changed" to the value it already held.
func strPtrEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// SetStatus performs a lifecycle transition under optimistic locking,
// guarded by AssetStatus.CanTransitionTo — the single authority for status
// changes (mirrors UserStore.SetStatus). The transition is checked against
// the state the database currently holds, not one the caller asserts: a
// caller that read the asset, then had it retired underneath them, must not
// be able to move it by supplying the old state — the row-version guard
// makes that read-then-write safe.
func (s *AssetStore) SetStatus(
	ctx context.Context, assetID string, rowVersion int64, status domain.AssetStatus,
) (*domain.Asset, error) {
	if !status.Valid() {
		return nil, domain.NewValidationError("status", "must be one of: planned, active, maintenance, retired")
	}
	actor, ok := domain.ActorFrom(ctx)
	if !ok {
		return nil, domain.ErrNoActor
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Read under FOR UPDATE inside this transaction — see Update's identical
	// comment on why: this is this table's equivalent of ADR-AUDIT-006's
	// chain-head serialisation for a concurrent append.
	current, err := s.getForUpdateTx(ctx, tx, assetID)
	if err != nil {
		return nil, err
	}
	if current.RowVersion != rowVersion {
		return nil, domain.ErrVersionMismatch
	}
	if !current.Status.CanTransitionTo(status) {
		return nil, domain.NewAssetTransitionError(current.Status, status)
	}

	row := tx.QueryRow(ctx, `
		UPDATE asset
		   SET status = $3, row_version = row_version + 1, updated_at = now()
		 WHERE asset_id = $1 AND row_version = $2
		RETURNING `+assetColumns,
		assetID, rowVersion, string(status))

	updated, err := scanAsset(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, s.explainMissedUpdate(ctx, assetID)
	}
	if err != nil {
		return nil, fmt.Errorf("set asset status: %w", err)
	}

	oldVal, newVal := string(current.Status), string(status)
	if err := s.recordChange(ctx, tx, updated.TenantID, assetID,
		domain.AssetChangeTransitioned, "status", &oldVal, &newVal, actor, updated.RowVersion); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return updated, nil
}

// Delete removes an asset. Its relationships go with it (ON DELETE CASCADE).
// Its change history does NOT: asset_change_history carries no foreign key
// to asset, precisely so a hard Delete does not erase the forensic record of
// how the asset got to the state it was deleted in (see the migration).
func (s *AssetStore) Delete(ctx context.Context, assetID string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM asset WHERE asset_id = $1`, assetID)
	if err != nil {
		return fmt.Errorf("delete asset: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// History returns a page of assetID's append-only change history,
// keyset-paginated over change_id — a ULID, and therefore chronological
// ascending, the same shape List's asset_id pagination uses. Isolation is
// enforced by row-level security on the tenant-scoped connection, exactly as
// every other read in this store is; no explicit tenant_id predicate is
// needed here for the same reason List has none.
func (s *AssetStore) History(
	ctx context.Context, assetID string, limit int, after string,
) ([]*domain.AssetChangeEntry, error) {
	limit = clampAssetPage(limit)

	rows, err := s.pool.Query(ctx, `
		SELECT `+assetChangeColumns+`
		  FROM asset_change_history
		 WHERE asset_id = $1 AND ($2 = '' OR change_id > $2)
		 ORDER BY change_id
		 LIMIT $3`, assetID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list asset change history: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.AssetChangeEntry, 0, limit)
	for rows.Next() {
		e, err := scanAssetChangeEntry(rows)
		if err != nil {
			return nil, fmt.Errorf("scan asset change entry: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate asset change history: %w", err)
	}
	return out, nil
}

// Export returns a page of EVERY asset regardless of status, INCLUDING
// retired — unlike List, nothing here is soft-retire-filtered, because an
// export exists to reconstruct the CMDB exactly as it stands (backup/
// migration, E1.4), not as an operator browses it. Keyset-paginated over
// asset_id, the same shape List uses.
func (s *AssetStore) Export(ctx context.Context, limit int, after string) ([]*domain.Asset, error) {
	limit = clampAssetPage(limit)

	rows, err := s.pool.Query(ctx, `
		SELECT `+assetColumns+`
		  FROM asset
		 WHERE ($1 = '' OR asset_id > $1)
		 ORDER BY asset_id
		 LIMIT $2`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("export assets: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.Asset, 0, limit)
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			return nil, fmt.Errorf("scan asset: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate assets: %w", err)
	}
	return out, nil
}

// Duplicates returns groups of assets likely representing the same
// real-world Configuration Item (E1.4) — see domain.AssetRepository.
// Duplicates' doc comment for the two matching rules. Read-only: no row is
// touched. Bounded by maxAssetDuplicateRows per rule, per
// docs/PLATFORM-BUILD-PLAN.md §3's "no unbounded scans" rule — this is a
// report, not a paginated list.
func (s *AssetStore) Duplicates(ctx context.Context) ([]*domain.AssetDuplicateGroup, error) {
	byKey := map[string]*domain.AssetDuplicateGroup{}
	var order []string
	record := func(reason domain.AssetDuplicateReason, key string, m domain.AssetDuplicateMember) {
		k := string(reason) + "\x00" + key
		g, ok := byKey[k]
		if !ok {
			g = &domain.AssetDuplicateGroup{Reason: reason, Key: key}
			byKey[k] = g
			order = append(order, k)
		}
		g.Members = append(g.Members, m)
	}

	// Rule 1: the same external_ref reported by two or more DIFFERENT
	// sources. ux_asset_tenant_source_external_ref already forbids the same
	// source reporting one external_ref twice, so every group produced here
	// spans at least two sources by construction.
	extRows, err := s.pool.Query(ctx, `
		SELECT asset_id, type, name, source, external_ref, status
		  FROM asset
		 WHERE external_ref IN (
		         SELECT external_ref FROM asset
		          WHERE external_ref IS NOT NULL
		          GROUP BY external_ref
		         HAVING COUNT(DISTINCT source) > 1)
		 ORDER BY external_ref, asset_id
		 LIMIT $1`, maxAssetDuplicateRows)
	if err != nil {
		return nil, fmt.Errorf("query external_ref duplicates: %w", err)
	}
	for extRows.Next() {
		m, status, err := scanAssetDuplicateMember(extRows)
		if err != nil {
			extRows.Close()
			return nil, fmt.Errorf("scan external_ref duplicate: %w", err)
		}
		m.Status = status
		record(domain.AssetDuplicateSameExternalRef, *m.ExternalRef, m)
	}
	if err := extRows.Err(); err != nil {
		extRows.Close()
		return nil, fmt.Errorf("iterate external_ref duplicates: %w", err)
	}
	extRows.Close()

	// Rule 2: the same type and the same name after normalization (trimmed,
	// case-folded) — the manual-entry case, where two operators registered
	// the same box under names differing only in case or whitespace.
	nameRows, err := s.pool.Query(ctx, `
		SELECT asset_id, type, name, source, external_ref, status
		  FROM asset
		 WHERE (type, lower(trim(name))) IN (
		         SELECT type, lower(trim(name)) FROM asset
		          GROUP BY type, lower(trim(name))
		         HAVING COUNT(*) > 1)
		 ORDER BY type, lower(trim(name)), asset_id
		 LIMIT $1`, maxAssetDuplicateRows)
	if err != nil {
		return nil, fmt.Errorf("query type/name duplicates: %w", err)
	}
	defer nameRows.Close()
	for nameRows.Next() {
		m, status, err := scanAssetDuplicateMember(nameRows)
		if err != nil {
			return nil, fmt.Errorf("scan type/name duplicate: %w", err)
		}
		m.Status = status
		key := m.Type + ":" + strings.ToLower(strings.TrimSpace(m.Name))
		record(domain.AssetDuplicateSameTypeName, key, m)
	}
	if err := nameRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate type/name duplicates: %w", err)
	}

	out := make([]*domain.AssetDuplicateGroup, 0, len(order))
	for _, k := range order {
		out = append(out, byKey[k])
	}
	return out, nil
}

// maxAssetHealthSampleRows bounds each health-report category's sample —
// mirrors maxAssetDuplicateRows' "no unbounded scan returning every CI" rule
// (docs/PLATFORM-BUILD-PLAN.md §3), scoped smaller because Health reports
// FOUR categories in one call rather than one. Count is never capped by this;
// only Samples is.
const maxAssetHealthSampleRows = 50

// healthSampleColumns is the projection every health-report sample query
// selects — an asset_id/type/name is enough for an operator to act on
// without a second round-trip through Get.
const healthSampleColumns = `asset_id, type, name`

func scanAssetHealthSample(s scanner) (domain.AssetHealthSample, error) {
	var m domain.AssetHealthSample
	err := s.Scan(&m.AssetID, &m.Type, &m.Name)
	return m, err
}

// Health computes the CMDB data-quality/freshness report (E1.5) — see
// domain.AssetHealthReport's doc comment for what each category means. Every
// query below is scoped to the caller's tenant by row-level security on this
// store's connection, exactly like every other read here; none takes an
// explicit tenant_id predicate for the same reason List has none.
func (s *AssetStore) Health(ctx context.Context, staleAfter time.Duration) (*domain.AssetHealthReport, error) {
	report := &domain.AssetHealthReport{StaleAfter: staleAfter}

	var err error
	if report.Stale, err = s.staleAssets(ctx, staleAfter); err != nil {
		return nil, err
	}
	if report.OrphanedAssets, err = s.orphanedAssets(ctx); err != nil {
		return nil, err
	}
	if report.OrphanedBusinessServices, err = s.orphanedBusinessServices(ctx); err != nil {
		return nil, err
	}
	if report.Incomplete, err = s.incompleteAssets(ctx); err != nil {
		return nil, err
	}
	return report, nil
}

// staleAssets: active/maintenance CIs whose last_seen is older than
// staleAfter, OR NULL (never confirmed by any source at all — treated as at
// least as stale as an old timestamp, not as fresh by omission). Retired and
// planned CIs are excluded: retired is not in service to go stale, and a
// planned CI has never been claimed to be confirmed by anything yet, so
// absence of confirmation is not a defect for it the way it is for
// something already live. Index-backed by ix_asset_tenant_status_last_seen
// (20260820000001).
func (s *AssetStore) staleAssets(ctx context.Context, staleAfter time.Duration) (domain.AssetHealthCategory, error) {
	cutoff := time.Now().UTC().Add(-staleAfter)
	const whereSQL = `status IN ('active', 'maintenance') AND (last_seen IS NULL OR last_seen < $1)`
	return s.assetHealthCategory(ctx,
		`SELECT COUNT(*) FROM asset WHERE `+whereSQL,
		`SELECT `+healthSampleColumns+` FROM asset WHERE `+whereSQL+` ORDER BY asset_id LIMIT $2`,
		cutoff)
}

// orphanedAssets: CIs (excluding retired) with NO relationship at all,
// neither outgoing nor incoming — unreachable from anything else in the CMDB
// graph. Both NOT EXISTS subqueries use the existing per-direction indexes
// (ix_asset_relationship_from/ix_asset_relationship_to, 20260816000001); no
// new index is needed for this category.
func (s *AssetStore) orphanedAssets(ctx context.Context) (domain.AssetHealthCategory, error) {
	const whereSQL = `
		status <> 'retired'
		AND NOT EXISTS (SELECT 1 FROM asset_relationship r WHERE r.from_asset_id = asset.asset_id)
		AND NOT EXISTS (SELECT 1 FROM asset_relationship r WHERE r.to_asset_id = asset.asset_id)`
	return s.assetHealthCategory(ctx,
		`SELECT COUNT(*) FROM asset WHERE `+whereSQL,
		`SELECT `+healthSampleColumns+` FROM asset WHERE `+whereSQL+` ORDER BY asset_id LIMIT $1`)
}

// assetTypeBusinessService is the seeded Asset.Type value orphanedBusinessServices
// applies to — mirrors httpapi's own assetTypeBusinessService (E1.2). Asset.Type
// is open-but-validated, not a closed enum (ADR-ASSET-001 §1), so this is a
// convention this query enforces, not a database constraint; duplicated
// rather than imported because httpapi depends on postgres, not the reverse
// (ADR-ARCH-001 layering).
const assetTypeBusinessService = "business_service"

// orphanedBusinessServices: business_service CIs (excluding retired) with no
// supporting CIs — no outgoing depends_on/runs_on edge, the composition
// GET .../service-map would otherwise report empty. connected_to and
// member_of are deliberately excluded, mirroring getAssetServiceMap's own
// choice of edge types: neither composes a service.
func (s *AssetStore) orphanedBusinessServices(ctx context.Context) (domain.AssetHealthCategory, error) {
	const whereSQL = `
		type = '` + assetTypeBusinessService + `' AND status <> 'retired'
		AND NOT EXISTS (
			SELECT 1 FROM asset_relationship r
			 WHERE r.from_asset_id = asset.asset_id AND r.type IN ('depends_on', 'runs_on'))`
	return s.assetHealthCategory(ctx,
		`SELECT COUNT(*) FROM asset WHERE `+whereSQL,
		`SELECT `+healthSampleColumns+` FROM asset WHERE `+whereSQL+` ORDER BY asset_id LIMIT $1`)
}

// incompleteAssets: CIs (excluding retired) missing basic classification —
// unowned (both owner_team_id and owner_user_id NULL), or criticality/
// environment left at "unknown". Index-backed by the PARTIAL index
// ix_asset_tenant_incomplete (20260820000001), whose predicate is this exact
// WHERE clause.
func (s *AssetStore) incompleteAssets(ctx context.Context) (domain.AssetHealthCategory, error) {
	const whereSQL = `
		status <> 'retired'
		AND ((owner_team_id IS NULL AND owner_user_id IS NULL)
		     OR criticality = 'unknown'
		     OR environment = 'unknown')`
	return s.assetHealthCategory(ctx,
		`SELECT COUNT(*) FROM asset WHERE `+whereSQL,
		`SELECT `+healthSampleColumns+` FROM asset WHERE `+whereSQL+` ORDER BY asset_id LIMIT $1`)
}

// assetHealthCategory runs countSQL (returning one COUNT(*) value) and, only
// if that count is non-zero, sampleSQL (whose final placeholder — $2 for
// staleAssets' cutoff-parameterised queries, $1 for every other category — is
// maxAssetHealthSampleRows) to build one domain.AssetHealthCategory. Skipping
// the sample query when count is zero avoids a second round-trip for the
// overwhelmingly common case (most categories are usually empty).
func (s *AssetStore) assetHealthCategory(ctx context.Context, countSQL, sampleSQL string, args ...any) (domain.AssetHealthCategory, error) {
	var cat domain.AssetHealthCategory
	if err := s.pool.QueryRow(ctx, countSQL, args...).Scan(&cat.Count); err != nil {
		return cat, fmt.Errorf("count asset health category: %w", err)
	}
	if cat.Count == 0 {
		return cat, nil
	}

	sampleArgs := append(append([]any{}, args...), maxAssetHealthSampleRows)
	rows, err := s.pool.Query(ctx, sampleSQL, sampleArgs...)
	if err != nil {
		return cat, fmt.Errorf("sample asset health category: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		m, err := scanAssetHealthSample(rows)
		if err != nil {
			return cat, fmt.Errorf("scan asset health sample: %w", err)
		}
		cat.Samples = append(cat.Samples, m)
	}
	if err := rows.Err(); err != nil {
		return cat, fmt.Errorf("iterate asset health sample: %w", err)
	}
	return cat, nil
}

// scanAssetDuplicateMember scans one row shared by both Duplicates queries:
// asset_id, type, name, source, external_ref, status, in that order.
func scanAssetDuplicateMember(s scanner) (domain.AssetDuplicateMember, domain.AssetStatus, error) {
	var (
		m      domain.AssetDuplicateMember
		status string
	)
	if err := s.Scan(&m.AssetID, &m.Type, &m.Name, &m.Source, &m.ExternalRef, &status); err != nil {
		return domain.AssetDuplicateMember{}, "", err
	}
	return m, domain.AssetStatus(status), nil
}

func scanAssetChangeEntry(s scanner) (*domain.AssetChangeEntry, error) {
	var (
		e    domain.AssetChangeEntry
		kind string
	)
	if err := s.Scan(
		&e.ChangeID, &e.TenantID, &e.AssetID, &kind, &e.Field,
		&e.OldValue, &e.NewValue, &e.Actor, &e.RowVersion, &e.OccurredAt,
	); err != nil {
		return nil, err
	}
	e.Kind = domain.AssetChangeKind(kind)
	return &e, nil
}

// explainMissedUpdate distinguishes "gone" from "stale version" after an
// UPDATE matched no row, so the caller gets 404 or 409 rather than one
// ambiguous failure. This mirrors TeamStore.explainMissedUpdate.
func (s *AssetStore) explainMissedUpdate(ctx context.Context, assetID string) error {
	if _, err := s.Get(ctx, assetID); err != nil {
		return err
	}
	return domain.ErrVersionMismatch
}

// CreateRelationship inserts a relationship between two assets.
//
// Both endpoints are confirmed to exist on THIS store's own tenant-scoped,
// RLS-enforced connection before the INSERT runs. That confirmation is load
// bearing, not defensive: PostgreSQL's foreign-key triggers run with the
// constraint's own privileges and BYPASS row-level security on the
// referenced table (a documented PostgreSQL behaviour, not a bug), so the
// `REFERENCES asset (asset_id)` constraint alone would let a caller name
// another tenant's asset_id and have the FK accept it — creating a
// cross-tenant edge the CMDB graph would then traverse. The existence check
// below runs the same policy the rest of this store's reads do, so a
// cross-tenant id is indistinguishable from one that does not exist at all,
// and both report ErrNotFound (ADR-ASSET-001 §6).
func (s *AssetStore) CreateRelationship(ctx context.Context, r *domain.AssetRelationship) (*domain.AssetRelationship, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	for _, assetID := range []string{r.FromAssetID, r.ToAssetID} {
		var exists bool
		if err := s.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM asset WHERE asset_id = $1)`, assetID,
		).Scan(&exists); err != nil {
			return nil, fmt.Errorf("check asset exists: %w", err)
		}
		if !exists {
			return nil, domain.ErrNotFound
		}
	}

	row := s.pool.QueryRow(ctx, `
		INSERT INTO asset_relationship (relationship_id, tenant_id, from_asset_id, to_asset_id, type)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+assetRelColumns,
		r.RelationshipID, r.TenantID, r.FromAssetID, r.ToAssetID, string(r.Type))

	created, err := scanAssetRelationship(row)
	if err != nil {
		switch {
		case isUniqueViolation(err):
			return nil, domain.ErrConflict
		case isForeignKeyViolation(err):
			return nil, domain.ErrNotFound
		default:
			return nil, fmt.Errorf("insert asset relationship: %w", err)
		}
	}
	return created, nil
}

// DeleteRelationship removes a relationship by id, or returns ErrNotFound.
func (s *AssetStore) DeleteRelationship(ctx context.Context, relationshipID string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM asset_relationship WHERE relationship_id = $1`, relationshipID)
	if err != nil {
		return fmt.Errorf("delete asset relationship: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// RelationshipsFrom returns the direct out-edges of assetID (no traversal).
func (s *AssetStore) RelationshipsFrom(ctx context.Context, assetID string) ([]*domain.AssetRelationship, error) {
	return s.queryRelationships(ctx,
		`SELECT `+assetRelColumns+` FROM asset_relationship WHERE from_asset_id = $1 ORDER BY created_at, relationship_id`,
		assetID)
}

// RelationshipsTo returns the direct in-edges of assetID (no traversal).
func (s *AssetStore) RelationshipsTo(ctx context.Context, assetID string) ([]*domain.AssetRelationship, error) {
	return s.queryRelationships(ctx,
		`SELECT `+assetRelColumns+` FROM asset_relationship WHERE to_asset_id = $1 ORDER BY created_at, relationship_id`,
		assetID)
}

func (s *AssetStore) queryRelationships(ctx context.Context, sql, arg string) ([]*domain.AssetRelationship, error) {
	rows, err := s.pool.Query(ctx, sql, arg)
	if err != nil {
		return nil, fmt.Errorf("query asset relationships: %w", err)
	}
	defer rows.Close()

	var out []*domain.AssetRelationship
	for rows.Next() {
		r, err := scanAssetRelationship(rows)
		if err != nil {
			return nil, fmt.Errorf("scan asset relationship: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate asset relationships: %w", err)
	}
	return out, nil
}
