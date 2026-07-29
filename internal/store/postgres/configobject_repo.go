package postgres

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
)

const coColumns = `cfg_id, artifact, version, role, lifecycle, retention_class, authority, ` +
	`ratified_by, review_cycle, retention_policy, row_version, created_at, updated_at`

// ConfigObjectRepo is the PostgreSQL implementation of domain.ConfigObjectRepository.
type ConfigObjectRepo struct {
	pool *pgxpool.Pool
}

// NewConfigObjectRepo builds a repository over the given pool.
func NewConfigObjectRepo(pool *pgxpool.Pool) *ConfigObjectRepo {
	return &ConfigObjectRepo{pool: pool}
}

var _ domain.ConfigObjectRepository = (*ConfigObjectRepo)(nil)

type scanner interface {
	Scan(dest ...any) error
}

func scanCO(s scanner) (*domain.ConfigObject, error) {
	var (
		c                    domain.ConfigObject
		role, lc, rc, author string
	)
	if err := s.Scan(
		&c.CfgID, &c.Artifact, &c.Version, &role, &lc, &rc, &author,
		&c.RatifiedBy, &c.ReviewCycle, &c.RetentionPolicy, &c.RowVersion,
		&c.CreatedAt, &c.UpdatedAt,
	); err != nil {
		return nil, err
	}
	c.Role = domain.Role(role)
	c.Lifecycle = domain.Lifecycle(lc)
	c.RetentionClass = domain.RetentionClass(rc)
	c.Authority = domain.Authority(author)
	c.Metadata = map[string]string{}
	return &c, nil
}

// Create inserts a new object with its metadata in one transaction.
func (r *ConfigObjectRepo) Create(ctx context.Context, obj *domain.ConfigObject) (*domain.ConfigObject, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := r.insertTx(ctx, tx, obj); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return obj, nil
}

// BulkCreate inserts many objects atomically.
func (r *ConfigObjectRepo) BulkCreate(ctx context.Context, objs []*domain.ConfigObject) ([]*domain.ConfigObject, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, o := range objs {
		if err := r.insertTx(ctx, tx, o); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return objs, nil
}

func (r *ConfigObjectRepo) insertTx(ctx context.Context, tx pgx.Tx, o *domain.ConfigObject) error {
	if o.CfgID == "" {
		o.CfgID = domain.NewID()
	}
	if o.Authority == "" {
		o.Authority = domain.AuthorityNonNormative
	}
	if o.RetentionPolicy == "" {
		o.RetentionPolicy = "permanent"
	}
	// Ownership is written explicitly rather than left to the column DEFAULT.
	// The default exists so a path that predates tenancy still produces a valid
	// row, but under row-level security a row must be labelled with the tenant
	// bound to the connection: inserting the default while connected as another
	// tenant violates the policy's WITH CHECK and fails the write outright.
	tenantID := domain.TenantIDFrom(ctx)
	err := tx.QueryRow(ctx, `
		INSERT INTO configuration_object
			(cfg_id, artifact, version, role, lifecycle, retention_class, authority,
			 ratified_by, review_cycle, retention_policy, tenant_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		RETURNING row_version, created_at, updated_at`,
		o.CfgID, o.Artifact, o.Version, string(o.Role), string(o.Lifecycle),
		string(o.RetentionClass), string(o.Authority), o.RatifiedBy, o.ReviewCycle, o.RetentionPolicy,
		tenantID,
	).Scan(&o.RowVersion, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrConflict
		}
		return fmt.Errorf("insert: %w", err)
	}
	for k, v := range o.Metadata {
		// The descriptive-metadata channel never carries §9.1 constitutional
		// inputs (ADR-GOV-003). This is the storage chokepoint, not a transport
		// check: every present and future caller writing metadata passes here.
		if domain.IsConstitutionalMetadataKey(k) {
			return domain.NewValidationError("metadata",
				"key "+k+" is a constitutional input (§9.1) and may not be written as descriptive metadata")
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO configuration_metadata (cfg_id, key, value, tenant_id) VALUES ($1,$2,$3,$4)`,
			o.CfgID, k, v, tenantID,
		); err != nil {
			return fmt.Errorf("insert metadata: %w", err)
		}
	}
	return nil
}

// Get returns an object by id, or domain.ErrNotFound.
func (r *ConfigObjectRepo) Get(ctx context.Context, cfgID string) (*domain.ConfigObject, error) {
	obj, err := scanCO(r.pool.QueryRow(ctx,
		`SELECT `+coColumns+` FROM configuration_object WHERE cfg_id = $1`, cfgID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	if err := r.attachMetadata(ctx, []*domain.ConfigObject{obj}); err != nil {
		return nil, err
	}
	return obj, nil
}

// List returns a keyset-paginated page ordered by (created_at, cfg_id).
func (r *ConfigObjectRepo) List(ctx context.Context, params domain.ListParams) (*domain.Page, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = 50
	}
	a := &argset{}
	var conds []string

	if params.Filter.Role != "" {
		conds = append(conds, "role = "+a.add(string(params.Filter.Role)))
	}
	if params.Filter.Lifecycle != "" {
		conds = append(conds, "lifecycle = "+a.add(string(params.Filter.Lifecycle)))
	}
	if params.Filter.Authority != "" {
		conds = append(conds, "authority = "+a.add(string(params.Filter.Authority)))
	}
	if q := strings.TrimSpace(params.Filter.Query); q != "" {
		pat := "%" + strings.ToLower(q) + "%"
		p1 := a.add(pat)
		p2 := a.add(pat)
		conds = append(conds, "(lower(artifact) LIKE "+p1+
			" OR cfg_id IN (SELECT cfg_id FROM configuration_metadata WHERE lower(value) LIKE "+p2+"))")
	}
	if params.Cursor != "" {
		ct, cid, err := decodeCursor(params.Cursor)
		if err != nil {
			return nil, fmt.Errorf("invalid cursor: %w", err)
		}
		conds = append(conds, "(created_at, cfg_id) > ("+a.add(ct)+", "+a.add(cid)+")")
	}

	sql := `SELECT ` + coColumns + ` FROM configuration_object`
	if len(conds) > 0 {
		sql += " WHERE " + strings.Join(conds, " AND ")
	}
	sql += " ORDER BY created_at, cfg_id LIMIT " + a.add(limit+1)

	rows, err := r.pool.Query(ctx, sql, a.vals...)
	if err != nil {
		return nil, fmt.Errorf("list query: %w", err)
	}
	defer rows.Close()

	var items []*domain.ConfigObject
	for rows.Next() {
		obj, err := scanCO(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, obj)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	page := &domain.Page{}
	if len(items) > limit {
		last := items[limit-1]
		page.NextCursor = encodeCursor(last.CreatedAt, last.CfgID)
		items = items[:limit]
	}
	if err := r.attachMetadata(ctx, items); err != nil {
		return nil, err
	}
	page.Items = items
	return page, nil
}

// Update applies a partial update under optimistic locking.
func (r *ConfigObjectRepo) Update(ctx context.Context, cfgID string, expected int64, patch *domain.Patch) (*domain.ConfigObject, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	a := &argset{}
	var sets []string
	if patch.RatifiedBy != nil {
		sets = append(sets, "ratified_by = "+a.add(*patch.RatifiedBy))
	}
	if patch.ReviewCycle != nil {
		sets = append(sets, "review_cycle = "+a.add(*patch.ReviewCycle))
	}
	if patch.RetentionPolicy != nil {
		sets = append(sets, "retention_policy = "+a.add(*patch.RetentionPolicy))
	}
	sets = append(sets, "row_version = row_version + 1", "updated_at = now()")

	sql := `UPDATE configuration_object SET ` + strings.Join(sets, ", ") +
		` WHERE cfg_id = ` + a.add(cfgID) + ` AND row_version = ` + a.add(expected) +
		` RETURNING ` + coColumns

	obj, err := scanCO(tx.QueryRow(ctx, sql, a.vals...))
	if errors.Is(err, pgx.ErrNoRows) {
		var rv int64
		e2 := tx.QueryRow(ctx, `SELECT row_version FROM configuration_object WHERE cfg_id = $1`, cfgID).Scan(&rv)
		if errors.Is(e2, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		if e2 != nil {
			return nil, e2
		}
		return nil, domain.ErrVersionMismatch
	}
	if err != nil {
		return nil, err
	}

	if patch.Metadata != nil {
		// A descriptive patch replaces the descriptive map wholesale, so the
		// delete below must spare the §9.1 constitutional inputs. Refusing to
		// *set* them while allowing the same request to *erase* them is not a
		// guard: proven live, a patch of an unrelated key returned 200 and
		// removed `responsibilities`, which makes allResponsibilities(old) empty
		// and the Replacement Test vacuously satisfied (ADR-GOV-003).
		if _, err := tx.Exec(ctx,
			`DELETE FROM configuration_metadata WHERE cfg_id = $1 AND key <> ALL($2::text[])`,
			cfgID, domain.ConstitutionalMetadataKeys(),
		); err != nil {
			return nil, fmt.Errorf("clear metadata: %w", err)
		}
		for k, v := range patch.Metadata {
			if domain.IsConstitutionalMetadataKey(k) {
				return nil, domain.NewValidationError("metadata",
					"key "+k+" is a constitutional input (§9.1) and may not be written as descriptive metadata")
			}
			if _, err := tx.Exec(ctx,
				`INSERT INTO configuration_metadata (cfg_id, key, value) VALUES ($1,$2,$3)`, cfgID, k, v,
			); err != nil {
				return nil, fmt.Errorf("insert metadata: %w", err)
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	if err := r.attachMetadata(ctx, []*domain.ConfigObject{obj}); err != nil {
		return nil, err
	}
	return obj, nil
}

// Delete removes an object, or returns domain.ErrNotFound.
//
// The §8-protected roles are excluded in the statement itself, so this registry
// path cannot destroy a Constitution/Governance/Validation/Evidence/Audit object
// even though it does not go through the governance engine. The exclusion is
// part of the DELETE rather than a preceding read, so no window exists between
// the check and the delete. Role is immutable, so the set cannot be evaded.
// (Deliberately no Delete method here.)
//
// Destroying a Configuration Object is a §8 constitutional operation, not a
// persistence detail. It is owned by the Governance Engine, which enforces role
// protection, working-material-only, the dependents check, and the atomic audit
// append in one transaction (ADR-AUDIT-005), and reaches storage through
// GovernanceStore.RemoveObject.
//
// This repository used to expose a `Delete` that issued a bare
// `DELETE FROM configuration_object` guarded by the protected-role rule alone.
// It was wired straight to DELETE /v1/artifacts/{id}, and it was a second,
// unguarded door to a destructive constitutional effect: proven live, a
// ratified current_baseline object the engine refuses to delete was destroyed
// through it with no audit event and with its dependency edges cascaded away.
//
// The method is gone rather than hardened, so the class cannot return through a
// new caller: an unguarded destructive path that does not exist cannot be wired
// to anything (ADR-GOV-002).

func (r *ConfigObjectRepo) attachMetadata(ctx context.Context, objs []*domain.ConfigObject) error {
	if len(objs) == 0 {
		return nil
	}
	ids := make([]string, len(objs))
	index := make(map[string]*domain.ConfigObject, len(objs))
	for i, o := range objs {
		ids[i] = o.CfgID
		if o.Metadata == nil {
			o.Metadata = map[string]string{}
		}
		index[o.CfgID] = o
	}
	rows, err := r.pool.Query(ctx,
		`SELECT cfg_id, key, value FROM configuration_metadata WHERE cfg_id = ANY($1)`, ids)
	if err != nil {
		return fmt.Errorf("load metadata: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, k, v string
		if err := rows.Scan(&id, &k, &v); err != nil {
			return err
		}
		if o, ok := index[id]; ok {
			o.Metadata[k] = v
		}
	}
	return rows.Err()
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// argset builds ordered positional placeholders for dynamic SQL.
type argset struct {
	vals []any
}

func (a *argset) add(v any) string {
	a.vals = append(a.vals, v)
	return "$" + strconv.Itoa(len(a.vals))
}

func encodeCursor(t time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d|%s", t.UnixMicro(), id)))
}

func decodeCursor(s string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, "", err
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, "", fmt.Errorf("malformed cursor")
	}
	n, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return time.Time{}, "", err
	}
	return time.UnixMicro(n).UTC(), parts[1], nil
}
