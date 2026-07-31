package kg

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The extractors describe the schema the architecture guards protect.
//
// Nine guards under internal/arch walk every non-test .go file below internal/
// and read the SQL in it. They cannot tell an extractor's example apart from a
// real statement, so a table name written into Go source here is read as a real
// mutation of that table and fails the build — and it fails inside internal/arch,
// where it looks like a guard defect rather than a naming collision in this
// package.
//
// These two rules make the collision unrepresentable (specification §0.3). They
// are enforced here, next to the code they bind, so the failure names the file
// that caused it.
//
// Both detectors are proven to fire against a known-bad sample held in
// testdata; a harness of two negative assertions over a package that is
// currently one doc comment would otherwise pass whether or not it works.

const (
	migrationDir = "../store/migrate/sql"
	fixtureDir   = "../../testdata/kg/fixtures"
	badSample    = fixtureDir + "/forbidden_sample.go.txt"
)

// createTableRe finds the tables the migration corpus creates. The forbidden
// set is derived from the tree rather than listed, so a table added tomorrow is
// covered without editing this file (Constitution Law 14.1). Deriving it also
// makes the rule stricter than §0.3 requires — no table name at all, not merely
// the §6.2 and audit ones — which costs nothing, because the design already
// requires every table name to be discovered at run time.
var createTableRe = regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z_][a-z0-9_]*)`)

// forbiddenTables reads the migration corpus and returns every table it creates.
func forbiddenTables(t *testing.T) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(migrationDir)
	if err != nil {
		t.Fatalf("read %s: %v", migrationDir, err)
	}
	out := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		raw, rerr := os.ReadFile(filepath.Join(migrationDir, e.Name()))
		if rerr != nil {
			t.Fatalf("read %s: %v", e.Name(), rerr)
		}
		for _, m := range createTableRe.FindAllStringSubmatch(string(raw), -1) {
			out[strings.ToLower(m[1])] = true
		}
	}
	// Anti-vacuity on the derivation itself: an empty or shrunken set would let
	// every assertion below pass over a package full of table names.
	if len(out) == 0 {
		t.Fatal("derived no table from the migration corpus — the derivation is broken and both " +
			"sweeps below would be vacuous")
	}
	for _, required := range []string{"app_user", "organization", "membership", "invitation",
		"tenant", "audit_event", "admin_audit_event", "admin_audit_chain_head"} {
		if !out[required] {
			t.Errorf("the derived forbidden set is missing %q — specification §0.3 names it "+
				"explicitly, so the derivation has stopped covering what it must", required)
		}
	}
	return out
}

// goSourceUnder returns every non-test .go file in this package tree, mirroring
// goFilesUnder in internal/arch so the two sweeps see the same files.
func goSourceUnder(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		out[path] = string(raw)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

// stringLiteralsIn returns the decoded value of every string literal in a file.
//
// Only literals are examined. §0.3's rule is about literals, and a comment
// explaining which table an extractor derives is not a literal — the guards in
// internal/arch strip comments before reading SQL for the same reason. Reading
// literals rather than raw text also keeps an identifier such as tenantID from
// being mistaken for the table it is named after.
func stringLiteralsIn(t *testing.T, path, src string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if v, uerr := strconv.Unquote(lit.Value); uerr == nil {
			out = append(out, v)
		}
		return true
	})
	return out
}

// tablesNamedIn reports which of the forbidden names appear in a file's string
// literals, matched on a word boundary so app_user_view is not read as app_user.
func tablesNamedIn(t *testing.T, path, src string, forbidden map[string]bool) []string {
	t.Helper()
	var hit []string
	lits := stringLiteralsIn(t, path, src)
	for table := range forbidden {
		re := regexp.MustCompile(`\b` + regexp.QuoteMeta(table) + `\b`)
		for _, lit := range lits {
			if re.MatchString(lit) {
				hit = append(hit, table)
				break
			}
		}
	}
	sort.Strings(hit)
	return hit
}

// runLoopsIn returns the receivers declaring Run(ctx context.Context) error,
// counting interface declarations of the same signature as well as methods:
// the guard in internal/arch matches on the written signature, so an interface
// that merely promises one is enough to put a type in its subject set.
func runLoopsIn(t *testing.T, path, src string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	isWorkerSig := func(ft *ast.FuncType) bool {
		if ft.Params == nil || len(ft.Params.List) != 1 {
			return false
		}
		sel, ok := ft.Params.List[0].Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Context" {
			return false
		}
		if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "context" {
			return false
		}
		if ft.Results == nil || len(ft.Results.List) != 1 {
			return false
		}
		res, ok := ft.Results.List[0].Type.(*ast.Ident)
		return ok && res.Name == "error"
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.FuncDecl:
			if v.Recv != nil && v.Name.Name == "Run" && isWorkerSig(v.Type) {
				out = append(out, v.Name.Name)
			}
		case *ast.InterfaceType:
			if v.Methods == nil {
				return true
			}
			for _, m := range v.Methods.List {
				ft, ok := m.Type.(*ast.FuncType)
				if !ok {
					continue
				}
				for _, nm := range m.Names {
					if nm.Name == "Run" && isWorkerSig(ft) {
						out = append(out, nm.Name)
					}
				}
			}
		}
		return true
	})
	return out
}

// RULE ONE — no table name may appear as a literal in this package's Go source.
func TestNoTableNameIsALiteralInGoSource(t *testing.T) {
	forbidden := forbiddenTables(t)
	files := goSourceUnder(t, ".")
	if len(files) == 0 {
		t.Fatal("swept no Go source under internal/kg — the walk is broken and this assertion " +
			"would pass over any violation")
	}
	for path, src := range files {
		if hit := tablesNamedIn(t, path, src, forbidden); len(hit) > 0 {
			t.Errorf("%s names %s in a string literal. internal/arch reads that as real SQL against "+
				"a real table and fails the build there, not here. Derive table names from the "+
				"migration corpus at run time, and keep example schema in %s as .sql data "+
				"(specification §0.3).", path, strings.Join(hit, ", "), fixtureDir)
		}
	}
	t.Logf("Go source swept: %d; forbidden table names derived: %d", len(files), len(forbidden))
}

// RULE TWO — no type here may declare the worker signature.
func TestNoTypeDeclaresTheWorkerRunSignature(t *testing.T) {
	files := goSourceUnder(t, ".")
	if len(files) == 0 {
		t.Fatal("swept no Go source under internal/kg — the walk is broken and this assertion " +
			"would pass over any violation")
	}
	for path, src := range files {
		if len(runLoopsIn(t, path, src)) > 0 {
			t.Errorf("%s declares Run(ctx context.Context) error. That signature marks a background "+
				"worker: TestEveryWorkerRunLoop_ObservesCancellation would adopt this type and "+
				"require it to observe ctx.Done(). Derivation is not a worker — use Build(ctx) "+
				"(specification §0.3).", path)
		}
	}
	t.Logf("Go source swept for the worker signature: %d", len(files))
}

// Both detectors must fire against a known-bad sample.
//
// Anti-vacuity, per subject (Constitution Law 14.2). The two rules above are
// negative assertions over a package that today holds a single doc comment;
// they would pass unchanged if either detector were blind. The sample is held
// as data so it is never compiled and never swept.
func TestGuardSafetyDetectorsFire(t *testing.T) {
	raw, err := os.ReadFile(badSample)
	if err != nil {
		t.Fatalf("read %s: %v — the known-bad sample is what proves these detectors work", badSample, err)
	}
	src := string(raw)

	hit := tablesNamedIn(t, badSample, src, forbiddenTables(t))
	want := []string{"admin_audit_event", "app_user"}
	if strings.Join(hit, ",") != strings.Join(want, ",") {
		t.Errorf("literal detector found %v in the known-bad sample, want %v — it is blind or "+
			"over-reaching, and rule one proves nothing", hit, want)
	}

	if got := runLoopsIn(t, badSample, src); len(got) != 1 {
		t.Errorf("worker-signature detector found %d Run loops in the known-bad sample, want 1 — "+
			"rule two proves nothing", len(got))
	}
}

// The fixture channel must stay usable.
//
// §0.3 sends example schema to testdata as .sql data files. That is only a real
// answer if such a file can hold the names Go source may not, so this asserts
// the fixture exists and does hold them.
func TestFixturesMayHoldWhatGoSourceMayNot(t *testing.T) {
	const fixture = fixtureDir + "/schema_sample.sql"
	raw, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("read %s: %v", fixture, err)
	}
	found := map[string]bool{}
	for _, m := range createTableRe.FindAllStringSubmatch(string(raw), -1) {
		found[strings.ToLower(m[1])] = true
	}
	for _, want := range []string{"app_user", "organization", "membership", "admin_audit_event"} {
		if !found[want] {
			t.Errorf("%s does not create %q — the fixture channel is meant to carry exactly the "+
				"names Go source may not, and it has stopped doing so", fixture, want)
		}
	}
}
