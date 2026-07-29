package arch

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Completeness sweep for the queue-correctness classes.
//
// Trust Register entries 14 (atomic claim) and 18 (claim fencing) were recorded
// as eliminated *classes* but enforced by tests that named the two queues then
// known. `webhook_replay_job` was a third claimed resource with neither: proven
// live, two concurrent workers each received all 8 pending jobs, and a worker
// that no longer owned a job overwrote the owner's `completed` outcome.
//
// So the guard no longer names queues. It derives them from the schema: any
// table with a pending-defaulted `status` column is work someone claims, and
// must carry a fencing token. A fourth queue cannot be added without one
// (ADR-CONCURRENCY-007).
func TestEveryWorkQueue_HasAFencingToken(t *testing.T) {
	sql := readMigrations(t)

	// A work queue: a table whose status column defaults to 'pending'.
	tableRe := regexp.MustCompile(`(?s)CREATE TABLE IF NOT EXISTS (\w+) \((.*?)\n\);`)
	queues := map[string]bool{}
	for _, m := range tableRe.FindAllStringSubmatch(sql, -1) {
		name, body := m[1], m[2]
		if strings.Contains(body, "status") && strings.Contains(body, "'pending'") {
			queues[name] = true
		}
	}
	if len(queues) == 0 {
		t.Fatal("no work-queue tables detected; the sweep would be vacuous")
	}

	for q := range queues {
		// claimed_at may be added by a later ALTER, so search the whole corpus.
		added := strings.Contains(sql, "ALTER TABLE "+q+" ADD COLUMN IF NOT EXISTS claimed_at") ||
			regexp.MustCompile(`(?s)CREATE TABLE IF NOT EXISTS `+q+` \(.*?claimed_at.*?\n\);`).MatchString(sql)
		if !added {
			t.Errorf("%s is a work queue (status defaults to 'pending') but has no claimed_at "+
				"column — it can be claimed by two workers at once and its outcome cannot be "+
				"fenced, which is exactly what entries 14/18 record as eliminated "+
				"(ADR-CONCURRENCY-007)", q)
		}
	}
	t.Logf("work queues swept: %d", len(queues))
}

// Every claim must be an atomic compare-and-set, and every outcome write fenced
// on the token. Derived from the queue set rather than a list of method names.
//
// Analysis is per function body, not per SQL literal: a query is routinely built
// from several string fragments (`... `+prefixCols(cols, "d")+` ...`), so
// splitting on literals cut one statement into pieces and reported a claim as
// missing the FOR UPDATE it actually had. A guard that cries wolf gets switched
// off, which is worse than no guard.
func TestEveryQueueClaim_IsAtomicAndFenced(t *testing.T) {
	queues := []string{"webhook_delivery", "policy_execution", "webhook_replay_job"}
	bodies := storeFuncBodies(t)
	if len(bodies) == 0 {
		t.Fatal("no store functions found; the sweep would be vacuous")
	}

	claimed := map[string]bool{}
	for _, body := range bodies {
		for _, q := range queues {
			if !strings.Contains(body, q) {
				continue
			}
			// A claim moves the row INTO the working state. Releasing a claim
			// mentions that state in its WHERE but sets 'pending', so match the
			// SET clause — matching the state anywhere flagged ReleaseClaim as an
			// unfenced claim.
			isClaim := strings.Contains(body, "UPDATE "+q) &&
				(strings.Contains(body, "SET status = 'inflight'") ||
					strings.Contains(body, "SET status = 'running'"))
			if isClaim {
				claimed[q] = true
				if !strings.Contains(body, "claimed_at") {
					t.Errorf("%s's claim does not stamp claimed_at, so its outcome write cannot "+
						"be fenced (ADR-CONCURRENCY-007)", q)
				}
				// `FOR UPDATE OF <alias> SKIP LOCKED` is required when the claim
				// joins another table, so the two parts are matched separately.
				if !strings.Contains(body, "FOR UPDATE") || !strings.Contains(body, "SKIP LOCKED") {
					t.Errorf("%s's claim is not exclusive (no FOR UPDATE SKIP LOCKED) — two "+
						"workers can hold the same unit of work (ADR-CONCURRENCY-002/007)", q)
				}
				continue
			}
			// A read that hands out pending work without claiming it is the defect
			// ADR-CONCURRENCY-002 eliminated. A single-row lookup by id is not.
			if !strings.Contains(body, "UPDATE") &&
				strings.Contains(body, "status = 'pending'") &&
				!strings.Contains(body, "WHERE id=$1") {
				t.Errorf("%s is read with a plain SELECT of pending rows — two workers in the "+
					"leadership-overlap window both receive it (ADR-CONCURRENCY-002/007)", q)
			}
		}
	}
	for _, q := range queues {
		if !claimed[q] {
			t.Errorf("%s has no atomic claim in the store — it is either unclaimed or claimed "+
				"outside the store (ADR-CONCURRENCY-007)", q)
		}
	}
	t.Logf("queues with an atomic, fenced claim: %d", len(claimed))
}

// storeFuncBodies returns each top-level function's source in the store package.
func storeFuncBodies(t *testing.T) []string {
	t.Helper()
	src := readStoreSources(t)
	parts := strings.Split(src, "\nfunc ")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return out
}

// The cursor classes (entry 17) get the same treatment: every cursor write must
// be monotonic, derived from the schema rather than from a list of two.
func TestEveryCursor_IsWrittenMonotonically(t *testing.T) {
	sql := readMigrations(t)
	cursorRe := regexp.MustCompile(`CREATE TABLE IF NOT EXISTS (\w*cursor\w*) \(`)
	cursors := map[string]bool{}
	for _, m := range cursorRe.FindAllStringSubmatch(sql, -1) {
		cursors[m[1]] = true
	}
	if len(cursors) == 0 {
		t.Fatal("no cursor tables detected; the sweep would be vacuous")
	}

	stores := readStoreSources(t)
	for c := range cursors {
		write := regexp.MustCompile(`INSERT INTO ` + c + `[^;]*ON CONFLICT[^;]*`)
		got := write.FindString(stores)
		if got == "" {
			t.Errorf("%s has no upsert in the store layer — its writer cannot be checked for "+
				"monotonicity (ADR-CONCURRENCY-004)", c)
			continue
		}
		if !strings.Contains(got, "GREATEST(") {
			t.Errorf("%s is written without GREATEST(...) — a stale or overlapping writer can "+
				"rewind the watermark and events are silently skipped (ADR-CONCURRENCY-004)", c)
		}
	}
	t.Logf("cursors swept: %d", len(cursors))
}

func readMigrations(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	dir := "../store/migrate/sql"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		raw, rerr := os.ReadFile(filepath.Join(dir, e.Name()))
		if rerr != nil {
			t.Fatalf("read %s: %v", e.Name(), rerr)
		}
		b.Write(raw)
		b.WriteByte('\n')
	}
	if b.Len() == 0 {
		t.Fatal("no migrations read; every sweep over them would be vacuous")
	}
	return b.String()
}

func readStoreSources(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	dir := "../store/postgres"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		raw, rerr := os.ReadFile(filepath.Join(dir, e.Name()))
		if rerr != nil {
			t.Fatalf("read %s: %v", e.Name(), rerr)
		}
		b.Write(raw)
		b.WriteByte('\n')
	}
	if b.Len() == 0 {
		t.Fatal("no store sources read; every sweep over them would be vacuous")
	}
	return b.String()
}
