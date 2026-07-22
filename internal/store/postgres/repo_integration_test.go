//go:build integration

package postgres

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/store/migrate"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
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
	if _, err := pool.Exec(ctx,
		`TRUNCATE configuration_object, configuration_metadata, artifact_version, idempotency_key CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
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
	ctx := context.Background()

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
	ctx := context.Background()
	created, _ := repo.Create(ctx, sample("B.md", "1.0.0"))

	lc := domain.LifecycleComplete
	updated, err := repo.Update(ctx, created.CfgID, 1, &domain.Patch{Lifecycle: &lc})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.RowVersion != 2 || updated.Lifecycle != domain.LifecycleComplete {
		t.Fatalf("unexpected: %+v", updated)
	}
	if _, err := repo.Update(ctx, created.CfgID, 1, &domain.Patch{Lifecycle: &lc}); err != domain.ErrVersionMismatch {
		t.Errorf("expected ErrVersionMismatch, got %v", err)
	}
	if _, err := repo.Update(ctx, "missing", 1, &domain.Patch{Lifecycle: &lc}); err != domain.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestRepoDelete(t *testing.T) {
	repo := NewConfigObjectRepo(testPool(t))
	ctx := context.Background()
	created, _ := repo.Create(ctx, sample("C.md", "1.0.0"))
	if err := repo.Delete(ctx, created.CfgID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := repo.Delete(ctx, created.CfgID); err != domain.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestRepoBulkAndPagination(t *testing.T) {
	repo := NewConfigObjectRepo(testPool(t))
	ctx := context.Background()

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
	ctx := context.Background()
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
