package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/alerting"
	"github.com/rpsg/oneops/internal/domain"
)

const (
	defaultAlertRulePageSize = 50
	maxAlertRulePageSize     = 500
)

const alertRuleCols = `rule_id, tenant_id, asset_id, metric, comparator, threshold, for_duration_seconds,
	severity, enabled, last_state, last_transition_at, row_version, created_at, updated_at`

// AlertRuleStore administers alert_rule: the admin CRUD API's tenant-scoped
// reads/writes (domain.AlertRuleRepository) AND, when built over the
// PRIVILEGED pool, the evaluator's due-scan/transition-recording surface
// (alerting.Store) — one struct, two roles depending on which pool
// constructs it, exactly mirroring CollectorCheckStore/collector.Store.
//
// alert_rule is TENANT-OWNED — it is in TenantOwnedTables and carries
// row-level security — so the tenant-scoped instance takes no tenant
// argument anywhere; the bound connection already is the boundary
// (ADR-TENANCY-002). The privileged instance instead serves every tenant's
// enabled rules from one process, the same category of cross-tenant
// background worker the telemetry rollup worker and collector scheduler
// already are.
type AlertRuleStore struct {
	pool *pgxpool.Pool
}

// NewAlertRuleStore builds the store over the given pool.
func NewAlertRuleStore(pool *pgxpool.Pool) *AlertRuleStore { return &AlertRuleStore{pool: pool} }

var (
	_ domain.AlertRuleRepository = (*AlertRuleStore)(nil)
	_ alerting.Store             = (*AlertRuleStore)(nil)
)

func clampAlertRulePage(limit int) int {
	if limit <= 0 {
		return defaultAlertRulePageSize
	}
	if limit > maxAlertRulePageSize {
		return maxAlertRulePageSize
	}
	return limit
}

func scanAlertRule(sc scanner) (*domain.AlertRule, error) {
	var (
		r                domain.AlertRule
		comparator       string
		severity         string
		lastState        string
		lastTransitionAt *time.Time
	)
	if err := sc.Scan(
		&r.RuleID, &r.TenantID, &r.AssetID, &r.Metric, &comparator, &r.Threshold, &r.ForDuration,
		&severity, &r.Enabled, &lastState, &lastTransitionAt, &r.RowVersion, &r.CreatedAt, &r.UpdatedAt,
	); err != nil {
		return nil, err
	}
	r.Comparator = domain.AlertComparator(comparator)
	r.Severity = domain.AlertSeverity(severity)
	r.LastState = domain.AlertRuleState(lastState)
	if lastTransitionAt != nil {
		r.LastTransitionAt = *lastTransitionAt
	}
	return &r, nil
}

// Create inserts a rule, re-verifying AssetID against this store's own
// tenant-scoped connection first — see domain.AlertRuleRepository.Create's
// doc comment. RLS on asset filters the existence check to the caller's own
// tenant already: an id belonging to another tenant simply does not come
// back, indistinguishable from one that does not exist at all — the same
// shape CollectorCheckStore.Create uses.
func (s *AlertRuleStore) Create(ctx context.Context, r *domain.AlertRule) (*domain.AlertRule, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM asset WHERE asset_id = $1)`, r.AssetID,
	).Scan(&exists); err != nil {
		return nil, fmt.Errorf("check alert rule asset id: %w", err)
	}
	if !exists {
		return nil, domain.ErrNotFound
	}

	row := s.pool.QueryRow(ctx, `
		INSERT INTO alert_rule
			(rule_id, tenant_id, asset_id, metric, comparator, threshold, for_duration_seconds,
			 severity, enabled, last_state, last_transition_at, row_version, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NULL,1,now(),now())
		RETURNING `+alertRuleCols,
		r.RuleID, r.TenantID, r.AssetID, r.Metric, string(r.Comparator), r.Threshold, r.ForDuration,
		string(r.Severity), r.Enabled, string(r.LastState))

	created, err := scanAlertRule(row)
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("insert alert rule: %w", err)
	}
	return created, nil
}

// Get returns a rule by identifier, or domain.ErrNotFound.
func (s *AlertRuleStore) Get(ctx context.Context, ruleID string) (*domain.AlertRule, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+alertRuleCols+` FROM alert_rule WHERE rule_id = $1`, ruleID)
	r, err := scanAlertRule(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get alert rule: %w", err)
	}
	return r, nil
}

// List returns a page of the caller's rules, keyset-paginated over rule_id.
func (s *AlertRuleStore) List(ctx context.Context, limit int, after string) ([]*domain.AlertRule, error) {
	limit = clampAlertRulePage(limit)
	rows, err := s.pool.Query(ctx, `
		SELECT `+alertRuleCols+`
		  FROM alert_rule
		 WHERE ($1 = '' OR rule_id > $1)
		 ORDER BY rule_id
		 LIMIT $2`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list alert rules: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.AlertRule, 0, limit)
	for rows.Next() {
		r, err := scanAlertRule(rows)
		if err != nil {
			return nil, fmt.Errorf("scan alert rule: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate alert rules: %w", err)
	}
	return out, nil
}

// Update changes one or more fields under optimistic locking. AssetID and
// Metric cannot be changed here — see domain.AlertRulePatch's doc comment —
// nor can LastState/LastTransitionAt, which RecordTransition owns
// exclusively.
func (s *AlertRuleStore) Update(
	ctx context.Context, ruleID string, rowVersion int64, patch domain.AlertRulePatch,
) (*domain.AlertRule, error) {
	current, err := s.Get(ctx, ruleID)
	if err != nil {
		return nil, err
	}
	if patch.Comparator != nil {
		current.Comparator = *patch.Comparator
	}
	if patch.Threshold != nil {
		current.Threshold = *patch.Threshold
	}
	if patch.ForDuration != nil {
		current.ForDuration = *patch.ForDuration
	}
	if patch.Severity != nil {
		current.Severity = *patch.Severity
	}
	if patch.Enabled != nil {
		current.Enabled = *patch.Enabled
	}
	if err := current.Validate(); err != nil {
		return nil, err
	}

	row := s.pool.QueryRow(ctx, `
		UPDATE alert_rule
		   SET comparator = $3, threshold = $4, for_duration_seconds = $5, severity = $6, enabled = $7,
		       row_version = row_version + 1, updated_at = now()
		 WHERE rule_id = $1 AND row_version = $2
		RETURNING `+alertRuleCols,
		ruleID, rowVersion, string(current.Comparator), current.Threshold, current.ForDuration,
		string(current.Severity), current.Enabled)

	updated, err := scanAlertRule(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, s.explainMissedUpdate(ctx, ruleID)
	}
	if err != nil {
		return nil, fmt.Errorf("update alert rule: %w", err)
	}
	return updated, nil
}

// explainMissedUpdate distinguishes "gone" from "stale version" after an
// UPDATE matched no row — mirrors CollectorCheckStore.explainMissedUpdate.
func (s *AlertRuleStore) explainMissedUpdate(ctx context.Context, ruleID string) error {
	if _, err := s.Get(ctx, ruleID); err != nil {
		return err
	}
	return domain.ErrVersionMismatch
}

// Delete removes a rule, or returns domain.ErrNotFound.
func (s *AlertRuleStore) Delete(ctx context.Context, ruleID string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM alert_rule WHERE rule_id = $1`, ruleID)
	if err != nil {
		return fmt.Errorf("delete alert rule: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// EnabledRules implements alerting.Store: enabled rules, across every tenant
// when built over the privileged pool, keyset-paginated over rule_id — the
// evaluator's due-scan equivalent (there is no "interval" to be due against;
// every enabled rule is a candidate every tick).
func (s *AlertRuleStore) EnabledRules(ctx context.Context, limit int, after string) ([]*domain.AlertRule, error) {
	limit = clampAlertRulePage(limit)
	rows, err := s.pool.Query(ctx, `
		SELECT `+alertRuleCols+`
		  FROM alert_rule
		 WHERE enabled = true AND ($1 = '' OR rule_id > $1)
		 ORDER BY rule_id
		 LIMIT $2`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list enabled alert rules: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.AlertRule, 0, limit)
	for rows.Next() {
		r, err := scanAlertRule(rows)
		if err != nil {
			return nil, fmt.Errorf("scan alert rule: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate enabled alert rules: %w", err)
	}
	return out, nil
}

// RecordTransition implements alerting.Store: a fenced, single-row CAS update
// of last_state/last_transition_at, keyed on the rule's own primary key —
// the same single-row-by-id shape NotificationStore.MarkSent and
// CollectorCheckStore.RecordRun already use from the privileged pool, so
// ADR-TENANCY-009's owning-predicate rule (which targets a caller-supplied
// *id set*, e.g. `id = ANY($)`) has nothing to re-derive here: this is one
// row, named by the id the evaluator's own prior read just returned.
func (s *AlertRuleStore) RecordTransition(
	ctx context.Context, ruleID string, rowVersion int64, state domain.AlertRuleState, at time.Time,
) (*domain.AlertRule, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE alert_rule
		   SET last_state = $3, last_transition_at = $4, row_version = row_version + 1, updated_at = now()
		 WHERE rule_id = $1 AND row_version = $2
		RETURNING `+alertRuleCols,
		ruleID, rowVersion, string(state), at)

	updated, err := scanAlertRule(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, s.explainMissedUpdate(ctx, ruleID)
	}
	if err != nil {
		return nil, fmt.Errorf("record alert rule transition: %w", err)
	}
	return updated, nil
}
