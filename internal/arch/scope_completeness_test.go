package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Completeness sweep for the platform/tenant authorization boundary (entry 2).
//
// ADR-AUTHZ-001's guard is AST-based but its *subject set* is a hand-maintained
// list of three handler names. That is enumerated enforcement: it proves the
// three routes someone remembered are gated, and says nothing about a fourth.
// Every audit so far has found sibling instances behind exactly that shape.
//
// The subject set is derived from the router instead: the tenant registry lives
// under /admin/tenants, so *every* route on that prefix must be registered
// through requirePlatformAdmin. A new tenant route is covered the moment it is
// added, without anyone remembering to update a list.
func TestEveryTenantRegistryRoute_RequiresPlatformAdmin(t *testing.T) {
	const routerPath = "../httpapi/server.go"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, routerPath, nil, 0)
	if err != nil {
		t.Fatalf("parse router: %v", err)
	}

	// The registry's route prefix. Anything mounted here administers tenants
	// themselves, which is a platform operation regardless of which handler it
	// happens to call.
	const registryPrefix = "/admin/tenants"

	found := 0
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		// A route registration: <recv>.<Verb>("/path", handler)
		verb, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch verb.Sel.Name {
		case "Get", "Post", "Patch", "Put", "Delete", "Method", "Handle":
		default:
			return true
		}
		path := ""
		for _, a := range call.Args {
			if lit, ok := a.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if v := strings.Trim(lit.Value, `"`); strings.HasPrefix(v, registryPrefix) {
					path = v
				}
			}
		}
		if path == "" {
			return true
		}
		found++

		// The guard is whatever this route was registered With(...).
		guard := ""
		if with, ok := verb.X.(*ast.CallExpr); ok {
			if withSel, ok := with.Fun.(*ast.SelectorExpr); ok && withSel.Sel.Name == "With" && len(with.Args) > 0 {
				guard = exprTextOf(with.Args[0])
			}
		}
		if !strings.Contains(guard, "requirePlatformAdmin") {
			t.Errorf("route %s is registered with %q — every tenant-registry route must go "+
				"through requirePlatformAdmin, because the registry is exempt from row-level "+
				"security and that middleware is the only control between a caller and every "+
				"customer's record (ADR-AUTHZ-001)", path, guard)
		}
		return true
	})

	if found == 0 {
		t.Fatal("no tenant-registry routes found; the sweep would be vacuous — if the prefix " +
			"moved, this guard must move with it")
	}
	t.Logf("tenant-registry routes swept: %d", found)
}

// Completeness sweep for producer identity (entry 15).
//
// ADR-CONCURRENCY-003 replaced random row ids with content-derived ones so a
// re-produced delivery collides rather than duplicating. Its guard names three
// files. This one finds every construction of a queued row anywhere in the tree
// and requires its identity to come from the derivation helper.
func TestEveryQueuedRowProducer_UsesDerivedIdentity(t *testing.T) {
	// The helper must be matched on a word boundary. `newDeliveryID(` contains
	// the substring `DeliveryID(`, so a plain Contains check passed a producer
	// that had been switched back to a random id — a sweep that silently passes
	// is worse than no sweep, because it is believed.
	type spec struct {
		lit    string
		helper *regexp.Regexp
		name   string
	}
	specs := []spec{
		{"Delivery{", regexp.MustCompile(`\bDeliveryID\(`), "DeliveryID("},
		{"Execution{", regexp.MustCompile(`\bExecutionID\(`), "ExecutionID("},
	}

	files := goFilesUnder(t, "..")
	checked := 0
	for _, f := range files {
		// The helpers themselves, and the stores that read rows back, are not
		// producers.
		if strings.Contains(f.path, "/store/") || strings.Contains(f.path, "/identity.go") {
			continue
		}
		src := stripComments(f.src)
		for _, sp := range specs {
			idx := strings.Index(src, sp.lit)
			for idx >= 0 {
				// A composite literal that sets ID: is a producer of a queued row.
				window := src[idx:min(idx+400, len(src))]
				if strings.Contains(window, "ID:") {
					checked++
					if !sp.helper.MatchString(window) {
						t.Errorf("%s constructs a %s with an identity that is not derived by %s — "+
							"a re-produced row would get a new id and become a duplicate the "+
							"receiver cannot dedup (ADR-CONCURRENCY-003)", f.path, sp.lit, sp.name)
					}
				}
				next := strings.Index(src[idx+1:], sp.lit)
				if next < 0 {
					break
				}
				idx = idx + 1 + next
			}
		}
	}
	if checked == 0 {
		t.Fatal("no queued-row producers found; the sweep would be vacuous")
	}
	t.Logf("queued-row producers swept: %d", checked)
}

func exprTextOf(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.SelectorExpr:
		return exprTextOf(v.X) + "." + v.Sel.Name
	case *ast.Ident:
		return v.Name
	case *ast.CallExpr:
		return exprTextOf(v.Fun) + "(...)"
	}
	return "?"
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

type goFile struct {
	path string
	src  string
}

// goFilesUnder returns every non-test .go file below root, so a sweep covers the
// tree rather than a remembered subset.
func goFilesUnder(t *testing.T, root string) []goFile {
	t.Helper()
	var out []goFile
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "node_modules" || d.Name() == "testdata" {
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
		out = append(out, goFile{path: path, src: string(raw)})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if len(out) == 0 {
		t.Fatalf("no Go files found under %s; every sweep would be vacuous", root)
	}
	return out
}
