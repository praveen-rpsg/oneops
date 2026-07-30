//go:build integration

package postgres

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/rpsg/oneops/internal/domain"
)

// seedAdminHistory writes n administrative acts through the real chokepoint.
func seedAdminHistory(t *testing.T, pool interface{}, n int) []string {
	t.Helper()
	ctx := adminTestCtx()
	users := NewUserStore(testPool(t))
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		u := newTestUser(t)
		if _, err := users.Create(ctx, u); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
		ids = append(ids, u.UserID)
	}
	return ids
}

// The query surface answers §6.1's question and filters on every axis the
// story names: who (actor), what (operation), to whom (the three subject
// columns) and when (a time window).
func TestAdminAuditQuery_FiltersOnEveryAxis(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	q := NewAdminAuditQueryStore(pool)

	users := NewUserStore(pool)
	u := newTestUser(t)
	if _, err := users.Create(ctx, u); err != nil {
		t.Fatalf("create: %v", err)
	}
	orgs := NewOrganizationStore(pool)
	o, err := domain.NewOrganization("Query Probe", strings.ToLower("qp-"+ulid.Make().String()[:10]))
	if err != nil {
		t.Fatalf("new org: %v", err)
	}
	if _, err := orgs.Create(ctx, o); err != nil {
		t.Fatalf("create org: %v", err)
	}

	cases := []struct {
		name   string
		filter domain.AdminAuditFilter
		want   func(*domain.AdminAuditRecord) bool
	}{
		{"by subject user", domain.AdminAuditFilter{SubjectUserID: u.UserID},
			func(r *domain.AdminAuditRecord) bool { return r.SubjectUserID == u.UserID }},
		{"by subject organisation", domain.AdminAuditFilter{SubjectOrgID: o.OrgID},
			func(r *domain.AdminAuditRecord) bool { return r.SubjectOrgID == o.OrgID }},
		{"by subject tenant", domain.AdminAuditFilter{SubjectTenantID: o.TenantID},
			func(r *domain.AdminAuditRecord) bool { return r.SubjectTenantID == o.TenantID }},
		{"by actor", domain.AdminAuditFilter{Actor: "test-platform-admin", Limit: 5},
			func(r *domain.AdminAuditRecord) bool { return r.Actor == "test-platform-admin" }},
		{"by operation", domain.AdminAuditFilter{Operation: domain.AdminOrgCreated, Limit: 5},
			func(r *domain.AdminAuditRecord) bool { return r.Operation == domain.AdminOrgCreated }},
		{"by time window", domain.AdminAuditFilter{
			From: time.Now().UTC().Add(-time.Hour), To: time.Now().UTC().Add(time.Hour), Limit: 5},
			func(r *domain.AdminAuditRecord) bool { return true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := q.QueryAdminAudit(ctx, tc.filter)
			if err != nil {
				t.Fatalf("query: %v", err)
			}
			if len(got) == 0 {
				t.Fatal("filter matched nothing — the assertion would be vacuous")
			}
			for _, r := range got {
				if !tc.want(r) {
					t.Errorf("record %s/%d does not match the filter", r.ChainID, r.Seq)
				}
			}
		})
	}

	// An unknown subject is an empty page, never a 404 — matching the identity
	// registries, and refusing to confirm whether an id exists.
	empty, next, err := q.QueryAdminAudit(ctx, domain.AdminAuditFilter{SubjectUserID: "no-such-user"})
	if err != nil {
		t.Fatalf("unknown subject: %v", err)
	}
	if len(empty) != 0 || next != nil {
		t.Error("an unknown subject must return an empty page with no cursor")
	}
}

// Cursor pagination must be a total order: walking every page must visit each
// record exactly once, with no skips and no repeats, even though occurred_at
// ties are guaranteed by §6.8's multi-chain shape.
func TestAdminAuditQuery_CursorPaginationIsStable(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	q := NewAdminAuditQueryStore(pool)

	// Ties are the whole point. occurred_at is not unique — §6.8's multi-chain
	// shape puts sibling acts of one administrative act on the same instant —
	// so a page of records that all share a timestamp is the case a cursor over
	// occurred_at alone silently mis-handles. Seeding through the writer would
	// produce distinct microsecond stamps and prove nothing, so the block is
	// written directly, as the owner.
	tie := time.Now().UTC().Truncate(time.Microsecond)
	for i := 1; i <= 12; i++ {
		chain := fmt.Sprintf("chain:tie:%d:%s", i, ulid.Make().String())
		for seq := 1; seq <= 2; seq++ {
			if _, err := pool.Exec(ctx, `
				INSERT INTO admin_audit_event
					(chain_id, seq, event_id, operation_id, operation, actor,
					 subject_user_id, payload_canonical, payload, prev_hash, this_hash, occurred_at)
				VALUES ($1,$2,$3,$4,'user.created','tie-actor',$5,
					'{"subject_user_id":"tie"}','{"subject_user_id":"tie"}'::jsonb,$6,$7,$8)`,
				chain, seq, ulid.Make().String(), ulid.Make().String(), "tie",
				make([]byte, 32), append([]byte{byte(i), byte(seq)}, make([]byte, 30)...), tie); err != nil {
				t.Fatalf("seed tie %d/%d: %v", i, seq, err)
			}
		}
	}

	seen := map[string]int{}
	var cursor domain.AdminAuditCursor
	pages, total := 0, 0
	for {
		got, next, err := q.QueryAdminAudit(ctx,
			domain.AdminAuditFilter{SubjectUserID: "tie", Limit: 4, After: cursor})
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		for _, r := range got {
			key := fmt.Sprintf("%s/%d", r.ChainID, r.Seq)
			seen[key]++
			total++
		}
		pages++
		if next == nil {
			break
		}
		cursor = *next
		if pages > 200 {
			t.Fatal("pagination did not terminate — the cursor is not advancing")
		}
	}
	if pages < 2 {
		t.Fatalf("only %d page(s) — pagination was not exercised", pages)
	}
	for key, n := range seen {
		if n != 1 {
			t.Errorf("record %s appeared %d times across pages; the cursor is not a total order", key, n)
		}
	}

	// The whole set, read in one page, must equal what paging produced.
	all, _, err := q.QueryAdminAudit(ctx,
		domain.AdminAuditFilter{SubjectUserID: "tie", Limit: domain.MaxAdminAuditPageSize})
	if err != nil {
		t.Fatalf("single page: %v", err)
	}
	if len(all) != total {
		t.Errorf("paging visited %d records but a single page holds %d — rows were skipped or repeated",
			total, len(all))
	}

	// Ordering is strictly descending on the full tuple.
	for i := 1; i < len(all); i++ {
		p, c := all[i-1], all[i]
		if p.OccurredAt.Before(c.OccurredAt) {
			t.Errorf("ordering is not descending at %d", i)
		}
		if p.OccurredAt.Equal(c.OccurredAt) && p.ChainID == c.ChainID && p.Seq <= c.Seq {
			t.Errorf("tie at %d is not broken by (chain_id, seq)", i)
		}
	}
}

// A page is bounded whatever the caller asks for: this table is never pruned,
// so an unbounded query is unbounded for the life of the platform.
func TestAdminAuditQuery_PagesAreBounded(t *testing.T) {
	pool := testPool(t)
	f := domain.AdminAuditFilter{Limit: 100000}
	if err := f.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if f.Limit != domain.MaxAdminAuditPageSize {
		t.Errorf("limit = %d, want it capped at %d", f.Limit, domain.MaxAdminAuditPageSize)
	}
	zero := domain.AdminAuditFilter{}
	if err := zero.Validate(); err != nil || zero.Limit != domain.DefaultAdminAuditPageSize {
		t.Errorf("an unspecified limit must default to %d, got %d", domain.DefaultAdminAuditPageSize, zero.Limit)
	}
	if bad := (&domain.AdminAuditFilter{Operation: "not.real"}); bad.Validate() == nil {
		t.Error("an operation outside the vocabulary must be refused")
	}
	_ = NewAdminAuditQueryStore(pool)
}

// The cursor is opaque and must round-trip exactly; a forged one is refused
// rather than silently reinterpreted.
func TestAdminAuditQuery_CursorRoundTripsAndRefusesForgery(t *testing.T) {
	c := domain.AdminAuditCursor{
		OccurredAt: time.Now().UTC().Truncate(time.Microsecond),
		ChainID:    "chain:platform", Seq: 42,
	}
	back, err := domain.DecodeAdminAuditCursor(c.Encode())
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if !back.OccurredAt.Equal(c.OccurredAt) || back.ChainID != c.ChainID || back.Seq != c.Seq {
		t.Errorf("cursor did not round-trip: %+v vs %+v", back, c)
	}
	for _, bad := range []string{"not-base64!!", "YWJj", "", "///"} {
		if _, err := domain.DecodeAdminAuditCursor(bad); bad != "" && err == nil {
			t.Errorf("forged cursor %q was accepted", bad)
		}
	}
}

// PERFORMANCE: every filter the story names must use an index. A sequential
// scan here is a table scan over history that is never pruned.
func TestAdminAuditQuery_UsesIndexesNotSequentialScans(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	seedAdminHistory(t, pool, 40)
	if _, err := pool.Exec(ctx, `ANALYZE admin_audit_event`); err != nil {
		t.Fatalf("analyze: %v", err)
	}
	// The planner prefers a sequential scan on a tiny table whatever the
	// indexes say, so the choice is forced off for the assertion to mean
	// anything about production-sized data. SET LOCAL is per-connection and
	// per-transaction, so the EXPLAIN must run on the same transaction — a
	// pooled Exec followed by a pooled Query would take two connections and the
	// setting would silently not apply.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatalf("disable seqscan: %v", err)
	}

	// Each case names the column the plan must satisfy from an index condition.
	// Asserting merely that "Index" appears is not enough: with sequential
	// scans disabled the planner will happily walk an unrelated index and apply
	// the predicate as a Filter, which is the table scan this test exists to
	// forbid, wearing an index's name.
	plans := []struct{ name, sql, cond string }{
		{"page order", `SELECT 1 FROM admin_audit_event ORDER BY occurred_at DESC, chain_id DESC, seq DESC LIMIT 10`, ""},
		{"by actor", `SELECT 1 FROM admin_audit_event WHERE actor = 'x'`, "actor"},
		{"by operation", `SELECT 1 FROM admin_audit_event WHERE operation = 'user.created'`, "operation"},
		{"by subject org", `SELECT 1 FROM admin_audit_event WHERE subject_org_id = 'x'`, "subject_org_id"},
		{"by subject usr", `SELECT 1 FROM admin_audit_event WHERE subject_user_id = 'x'`, "subject_user_id"},
		{"by subject ten", `SELECT 1 FROM admin_audit_event WHERE subject_tenant_id = 'x'`, "subject_tenant_id"},
	}
	for _, tc := range plans {
		name, sql, cond := tc.name, tc.sql, tc.cond
		t.Run(name, func(t *testing.T) {
			var plan strings.Builder
			rows, err := tx.Query(ctx, "EXPLAIN "+sql)
			if err != nil {
				t.Fatalf("explain: %v", err)
			}
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					t.Fatalf("scan plan: %v", err)
				}
				plan.WriteString(line + "\n")
			}
			rows.Close()
			if strings.Contains(plan.String(), "Seq Scan") {
				t.Errorf("%s falls back to a sequential scan over never-pruned history:\n%s", name, plan.String())
			}
			if !strings.Contains(plan.String(), "Index") {
				t.Errorf("%s uses no index:\n%s", name, plan.String())
			}
			if cond != "" && !strings.Contains(plan.String(), "Index Cond: ("+cond) {
				t.Errorf("%s does not satisfy %q from an index condition — the predicate is being "+
					"applied as a filter over rows an index should have excluded:\n%s",
					name, cond, plan.String())
			}
		})
	}
}
