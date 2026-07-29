//go:build integration

package postgres

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/store/migrate"
)

// Trust Register audit: entry 13 records "concurrent-boot migration race
// (duplicate-key on schema_migrations)" as an eliminated class, closed by
// serialising migration runs on a dedicated advisory lock (ADR-CONCURRENCY-001).
//
// The register lists integration enforcement, but no test exercised the race:
// the migration tests covered pending-detection and schema shape, both
// single-threaded. The claim rested on the lock existing rather than on evidence
// that it works — which is exactly the distinction this programme's evidence law
// draws (EVR-009).
//
// This reproduces the original scenario: several replicas booting at once
// against a database with no schema, which is when every one of them tries to
// create `schema_migrations` and apply the same versions.
func TestMigrationRace_ConcurrentBootsSerialise(t *testing.T) {
	base := itestDSN(t)
	// A dedicated schema so this test owns the migration state it races on and
	// cannot disturb (or be disturbed by) the shared test schema.
	schema := fmt.Sprintf("migrace_%d", time.Now().UnixNano()%1_000_000)
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	dsn := base + sep + "options=-c%20search_path%3D" + schema

	ctx := context.Background()
	admin, err := NewPool(ctx, base, 2)
	if err != nil {
		t.Fatalf("admin pool: %v", err)
	}
	defer admin.Close()
	if _, err := admin.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS `+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	})

	// Several "replicas", each with its own pool, all booting at once.
	const replicas = 6
	pools := make([]*pgxpool.Pool, replicas)
	for i := 0; i < replicas; i++ {
		p, perr := NewPool(ctx, dsn, 2)
		if perr != nil {
			t.Fatalf("replica %d pool: %v", i, perr)
		}
		defer p.Close()
		pools[i] = p
	}

	errs := make([]error, replicas)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < replicas; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = migrate.Up(ctx, pools[i])
		}(i)
	}
	close(start)
	wg.Wait()

	failed := 0
	for i, e := range errs {
		if e != nil {
			failed++
			t.Errorf("replica %d failed to migrate: %v — concurrent boots must serialise on the "+
				"advisory lock, not race on schema_migrations (ADR-CONCURRENCY-001)", i, e)
		}
	}

	// Every migration applied exactly once.
	var versions, distinct int
	if err := pools[0].QueryRow(ctx,
		`SELECT count(*), count(DISTINCT version) FROM schema_migrations`).Scan(&versions, &distinct); err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	t.Logf("%d concurrent boots: %d failed; schema_migrations holds %d rows, %d distinct versions",
		replicas, failed, versions, distinct)

	if versions == 0 {
		t.Fatal("no migrations recorded; the race was not exercised and this test proves nothing")
	}
	if versions != distinct {
		t.Errorf("schema_migrations has %d rows for %d distinct versions — a version was applied "+
			"twice, which is the duplicate-key race this class records", versions, distinct)
	}
}
