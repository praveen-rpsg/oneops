package arch

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ADR-AUDIT-006 requires every audit append to serialise on its chain head.
// The obligation was previously enforced by matching a method name, which meant
// it reached one append path and not the other, and could not see the SQL at
// all: deleting `FOR UPDATE` from the statement left the method name intact and
// the sweep silent. This guard derives both halves instead.
//
// Nothing here is written down. The append-only tables come from the migration
// corpus, the append paths from the SQL that writes them, and the obligation
// from the transaction owner's own call graph. A new audit table, a new append
// path, or a renamed function all enter the sweep with no edit to this file.

var (
	// An append-only table is one the schema guards with a row-level trigger
	// against UPDATE and DELETE. That is the structural signature of audit
	// history in this codebase, and it is visible in the migrations without
	// naming any table.
	appendOnlyTriggerRe = regexp.MustCompile(`(?is)BEFORE\s+UPDATE\s+OR\s+DELETE\s+ON\s+([a-z_][a-z0-9_]*)`)

	// The obligation, expressed as the SQL that actually serialises rather than
	// as the name of a method believed to contain it.
	forUpdateRe = regexp.MustCompile(`(?is)FOR\s+UPDATE`)

	insertIntoRe = regexp.MustCompile(`(?is)INSERT\s+INTO\s+([a-z_][a-z0-9_]*)`)
)

// appendOnlyAuditTables derives the tables whose history ADR-AUDIT-006 protects.
func appendOnlyAuditTables(t *testing.T) []string {
	t.Helper()
	dir := "../store/migrate/sql"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for _, m := range appendOnlyTriggerRe.FindAllStringSubmatch(string(raw), -1) {
			seen[strings.ToLower(m[1])] = true
		}
	}
	out := make([]string, 0, len(seen))
	for tbl := range seen {
		out = append(out, tbl)
	}
	sort.Strings(out)
	if len(out) == 0 {
		t.Fatal("derived no append-only audit table from the migrations — the derivation is " +
			"broken and every assertion below would be vacuous")
	}
	return out
}

// reachesForUpdate reports whether fn, or anything it calls within its own
// package, issues a locking read.
//
// The walk is package-scoped deliberately. Resolving callees by name across the
// whole module lets an unrelated function with a colliding name satisfy the
// obligation, which was observed while this guard was being designed: a
// tree-wide walk reported the administrative path as compliant while its
// `FOR UPDATE` was deleted.
func reachesForUpdate(funcs []funcBody, pkgDir, start string) bool {
	byName := map[string][]funcBody{}
	for _, f := range funcs {
		if f.pkgDir == pkgDir {
			byName[f.name] = append(byName[f.name], f)
		}
	}
	seen := map[string]bool{}
	stack := []string{start}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[cur] {
			continue
		}
		seen[cur] = true
		for _, f := range byName[cur] {
			if forUpdateRe.MatchString(f.src) {
				return true
			}
			for callee := range f.calls {
				if !seen[callee] {
					stack = append(stack, callee)
				}
			}
		}
	}
	return false
}

// Every audit append path serialises on its chain head.
//
// Per-path, never in aggregate: the previous sweep counted append paths and
// passed at one of two. Each derived path is resolved to its transaction owner
// and checked independently, and a path with no owner is a failure rather than
// a silent skip — an append nothing calls cannot be shown to take the lock.
func TestEveryAuditAppendPath_SerialisesOnItsChainHead(t *testing.T) {
	tables := appendOnlyAuditTables(t)
	funcs := allFuncs(t)

	// Every function that writes append-only audit history.
	type path struct{ pkgDir, name, table string }
	var paths []path
	for _, f := range funcs {
		for _, m := range insertIntoRe.FindAllStringSubmatch(f.src, -1) {
			tbl := strings.ToLower(m[1])
			for _, want := range tables {
				if tbl == want {
					paths = append(paths, path{f.pkgDir, f.name, tbl})
				}
			}
		}
	}
	if len(paths) == 0 {
		t.Fatalf("derived no audit append path for %v — the derivation is broken and this sweep "+
			"would pass forever", tables)
	}

	// Each derived table must have at least one append path, so a table that
	// silently stops being written cannot shrink the subject set unnoticed.
	covered := map[string]bool{}
	for _, p := range paths {
		covered[p.table] = true
	}
	for _, tbl := range tables {
		if !covered[tbl] {
			t.Errorf("append-only table %q has no append path in the tree; either the derivation "+
				"missed it or history is being written by something this guard cannot see", tbl)
		}
	}

	checked := 0
	for _, p := range paths {
		// The transaction owner is whoever calls the append. It owns the
		// transaction the append runs in, so it is where the lock must be
		// taken — the append statement itself never locks.
		var owners []string
		for _, f := range funcs {
			if f.pkgDir == p.pkgDir && f.name != p.name && f.calls[p.name] {
				owners = append(owners, f.name)
			}
		}
		if len(owners) == 0 {
			t.Errorf("%s.%s writes %s but nothing calls it, so no transaction owner can be shown "+
				"to serialise on the chain head (ADR-AUDIT-006). An unreachable append path is "+
				"not an exempt one.", p.pkgDir, p.name, p.table)
			continue
		}
		sort.Strings(owners)
		for _, owner := range owners {
			checked++
			if reachesForUpdate(funcs, p.pkgDir, owner) {
				continue
			}
			t.Errorf("%s.%s appends to %s, but its caller %s never reads the chain head under "+
				"FOR UPDATE. Without that lock concurrent appends compute the same sequence "+
				"number and all but one fail: measured, twelve concurrent appends became three "+
				"(ADR-AUDIT-006).", p.pkgDir, p.name, p.table, owner)
		}
	}
	if checked == 0 {
		t.Fatal("no append path was checked against an owner — the sweep is vacuous")
	}
	t.Logf("append-only tables %v; append paths %d; owner checks %d", tables, len(paths), checked)
}
