package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
)

const (
	assetColumns = `asset_id, tenant_id, type, name, attributes, status,
		environment, criticality, owner_team_id, owner_user_id,
		row_version, created_at, updated_at`
	assetRelColumns    = `relationship_id, tenant_id, from_asset_id, to_asset_id, type, row_version, created_at, updated_at`
	assetChangeColumns = `change_id, tenant_id, asset_id, kind, field, old_value, new_value, actor, row_version, occurred_at`
)

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
	if !a.Status.ValidInitialStatus() {
		return nil, domain.NewValidationError("status",
			"must be one of: planned, active at creation; maintenance and retired are reached by transition")
	}
	actor, ok := domain.ActorFrom(ctx)
	if !ok {
		return nil, domain.ErrNoActor
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

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row := tx.QueryRow(ctx, `
		INSERT INTO asset (asset_id, tenant_id, type, name, attributes, status, environment, criticality, owner_team_id, owner_user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING `+assetColumns,
		a.AssetID, a.TenantID, a.Type, a.Name, attrs, string(a.Status),
		string(a.Environment), string(a.Criticality), a.OwnerTeamID, a.OwnerUserID)

	created, err := scanAsset(row)
	if err != nil {
		switch {
		case isUniqueViolation(err):
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

	if err := s.recordChange(ctx, tx, created.TenantID, created.AssetID,
		domain.AssetChangeCreated, "", nil, nil, actor, created.RowVersion); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return created, nil
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

	row := tx.QueryRow(ctx, `
		UPDATE asset
		   SET name          = COALESCE($3, name),
		       attributes    = COALESCE($4, attributes),
		       environment   = COALESCE($5, environment),
		       criticality   = COALESCE($6, criticality),
		       owner_team_id = CASE WHEN $7 THEN $8  ELSE owner_team_id END,
		       owner_user_id = CASE WHEN $9 THEN $10 ELSE owner_user_id END,
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
