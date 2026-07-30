//go:build integration

package postgres

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/store/migrate"
)

// itestSchema is this package's private schema. Every integration pool in the
// package must be built from itestDSN so nothing ever writes to `public`.
const itestSchema = "pgstore_itest"

// itestDSN returns TEST_DATABASE_URL pinned to the package's own schema, and
// skips the test when it is unset.
//
// Previously these tests ran in `public` and truncated only four of the
// thirteen tables, so audit chains, webhooks and policy rows accumulated
// permanently in whatever database TEST_DATABASE_URL pointed at. Several audit
// tests corrupt a chain on purpose to prove the verifier detects it; those
// poisoned chains survived the run and made the integrity sweeper — and
// therefore /v1/admin/status — report unhealthy forever after.
//
// This is the single place the search_path is applied: pools built any other
// way silently escape isolation, which is exactly how graphPool kept writing to
// `public` after testPool was fixed.
func itestDSN(tb testing.TB) string {
	tb.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		tb.Skip("TEST_DATABASE_URL not set")
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "options=-c%20search_path%3D" + itestSchema
}

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := itestDSN(t)
	ctx := adminTestCtx()
	pool, err := NewPool(ctx, dsn, 5)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	// Tolerate database startup timing.
	var pingErr error
	for i := 0; i < 60; i++ {
		if pingErr = pool.Ping(ctx); pingErr == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if pingErr != nil {
		t.Fatalf("database not ready: %v", pingErr)
	}
	if err := migrate.Up(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	truncateAll(ctx, t, pool)
	t.Cleanup(pool.Close)
	return pool
}

// truncateAll empties every table the integration suite writes to *except* the
// audit pair. Listing them explicitly (rather than relying on CASCADE from
// configuration_object) is what keeps webhook and policy rows from surviving:
// none of those tables carries a foreign key back to configuration_object, so
// CASCADE never reached them.
//
// audit_event is deliberately absent. Migration 20260723000001 installs an
// append-only trigger that raises on UPDATE, DELETE and TRUNCATE alike — audit
// history cannot be deleted (State Model §8). That guarantee is load-bearing
// and must not be relaxed for tests, so cross-run isolation is achieved by
// dropping the whole test schema in TestMain instead. audit_chain_head is left
// with it so head pointers stay consistent with the events they describe.
func truncateAll(ctx context.Context, t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	const stmt = `TRUNCATE
		configuration_object, configuration_metadata, artifact_version,
		idempotency_key, dependency_edge,
		webhook, webhook_delivery, webhook_cursor, webhook_replay_job,
		policy, policy_execution, policy_cursor
		CASCADE`
	if _, err := pool.Exec(ctx, stmt); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

// TestMain gives the package a schema that exists for exactly one run.
//
// Audit history is append-only by design, so a run's audit rows — including the
// chains that verifier tests corrupt on purpose — cannot be deleted once
// written. Left in a shared schema they accumulated permanently: a development
// database that had served a handful of runs held 56 chains, 5 of them
// deliberately broken, which made the integrity sweeper and /v1/admin/status
// report unhealthy indefinitely and would have masked a genuine chain break.
//
// Dropping the schema is DDL, not a row mutation, so it discards ephemeral test
// fixtures without weakening the immutability guarantee on real audit data.
func TestMain(m *testing.M) {
	if dsn := os.Getenv("TEST_DATABASE_URL"); dsn != "" {
		ctx := adminTestCtx()
		pool, err := NewPool(ctx, dsn, 2)
		if err != nil {
			fmt.Fprintf(os.Stderr, "integration setup: pool: %v\n", err)
			os.Exit(1)
		}
		if _, err := pool.Exec(ctx,
			`DROP SCHEMA IF EXISTS `+itestSchema+` CASCADE; CREATE SCHEMA `+itestSchema); err != nil {
			fmt.Fprintf(os.Stderr, "integration setup: reset schema: %v\n", err)
			os.Exit(1)
		}
		pool.Close()
	}
	os.Exit(m.Run())
}

func sample(artifact, version string) *domain.ConfigObject {
	return &domain.ConfigObject{
		Artifact: artifact, Version: version, Role: domain.RoleGovernance,
		Lifecycle: domain.LifecycleApproved, RetentionClass: domain.RetentionCurrentBaseline,
		Authority: domain.AuthorityActive, RetentionPolicy: "permanent",
		Metadata: map[string]string{"owner": "platform-" + artifact},
	}
}

func TestRepoCreateGet(t *testing.T) {
	repo := NewConfigObjectRepo(testPool(t))
	ctx := adminTestCtx()

	created, err := repo.Create(ctx, sample("A.md", "1.0.0"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.CfgID == "" || created.RowVersion != 1 {
		t.Fatalf("unexpected created: %+v", created)
	}
	got, err := repo.Get(ctx, created.CfgID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Metadata["owner"] != "platform-A.md" {
		t.Errorf("metadata lost: %+v", got.Metadata)
	}

	if _, err := repo.Create(ctx, sample("A.md", "1.0.0")); err != domain.ErrConflict {
		t.Errorf("expected ErrConflict, got %v", err)
	}
	if _, err := repo.Get(ctx, "missing"); err != domain.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestRepoUpdateOptimistic(t *testing.T) {
	repo := NewConfigObjectRepo(testPool(t))
	ctx := adminTestCtx()
	created, _ := repo.Create(ctx, sample("B.md", "1.0.0"))

	// Optimistic locking is exercised with a non-constitutional field: Patch no
	// longer carries a dimension (ADR-CP5).
	rc := "event-driven"
	updated, err := repo.Update(ctx, created.CfgID, 1, &domain.Patch{ReviewCycle: &rc})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.RowVersion != 2 || updated.ReviewCycle != "event-driven" {
		t.Fatalf("unexpected: %+v", updated)
	}
	// The dimensions are untouched by a registry update.
	if updated.Lifecycle != created.Lifecycle || updated.RetentionClass != created.RetentionClass ||
		updated.Authority != created.Authority {
		t.Fatalf("registry update altered a dimension: %+v", updated)
	}
	if _, err := repo.Update(ctx, created.CfgID, 1, &domain.Patch{ReviewCycle: &rc}); err != domain.ErrVersionMismatch {
		t.Errorf("expected ErrVersionMismatch, got %v", err)
	}
	if _, err := repo.Update(ctx, "missing", 1, &domain.Patch{ReviewCycle: &rc}); err != domain.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// Destruction is deliberately absent from this repository's contract: it is a §8
// constitutional operation owned by the Governance Engine (ADR-GOV-002), covered
// by httpapi.TestDestruction_* against the real routes and by
// arch.TestConfigObjectRepository_ExposesNoDestructiveMethod.

// The protected-role prohibition used to be enforced here, by the registry's own
// DELETE — a second door to a destructive constitutional effect that also
// skipped the working-material rule, the dependents check and the audit append.
// Hardening that door was a symptom fix; the door itself is gone
// (ADR-GOV-002). §8 role protection is now enforced in one place, the
// Governance Engine, and covered by governance and httpapi.TestDestruction_*.

func TestRepoBulkAndPagination(t *testing.T) {
	repo := NewConfigObjectRepo(testPool(t))
	ctx := adminTestCtx()

	var objs []*domain.ConfigObject
	for i := 0; i < 5; i++ {
		objs = append(objs, sample(fmt.Sprintf("Doc-%02d.md", i), "1.0.0"))
	}
	if _, err := repo.BulkCreate(ctx, objs); err != nil {
		t.Fatalf("bulk: %v", err)
	}

	seen := map[string]bool{}
	cursor := ""
	pages := 0
	for {
		page, err := repo.List(ctx, domain.ListParams{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		for _, o := range page.Items {
			if seen[o.CfgID] {
				t.Fatalf("duplicate across pages: %s", o.CfgID)
			}
			seen[o.CfgID] = true
		}
		pages++
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
	}
	if len(seen) != 5 {
		t.Fatalf("expected 5 unique items, got %d", len(seen))
	}
}

func TestRepoSearchAndFilter(t *testing.T) {
	repo := NewConfigObjectRepo(testPool(t))
	ctx := adminTestCtx()
	_, _ = repo.Create(ctx, sample("Alpha.md", "1.0.0"))
	beta := sample("Beta.md", "1.0.0")
	beta.Role = domain.RoleEvidence
	_, _ = repo.Create(ctx, beta)

	byRole, _ := repo.List(ctx, domain.ListParams{Limit: 10, Filter: domain.Filter{Role: domain.RoleEvidence}})
	if len(byRole.Items) != 1 || byRole.Items[0].Artifact != "Beta.md" {
		t.Fatalf("role filter: %+v", byRole.Items)
	}
	byQuery, _ := repo.List(ctx, domain.ListParams{Limit: 10, Filter: domain.Filter{Query: "alpha"}})
	if len(byQuery.Items) != 1 || byQuery.Items[0].Artifact != "Alpha.md" {
		t.Fatalf("query filter: %+v", byQuery.Items)
	}
	byMeta, _ := repo.List(ctx, domain.ListParams{Limit: 10, Filter: domain.Filter{Query: "platform-Beta.md"}})
	if len(byMeta.Items) != 1 {
		t.Fatalf("metadata search: %+v", byMeta.Items)
	}
}
