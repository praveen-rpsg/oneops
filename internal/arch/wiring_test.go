package arch_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Isolation is a property of wiring, not of schema.
//
// Row-level security only protects queries that run on a connection subject to
// it. A correct policy on a connection holding BYPASSRLS is worth nothing, and
// nothing in the type system distinguishes the two pools — both are
// *pgxpool.Pool. Two cross-tenant disclosures were shipped this way and found
// only by attacking the running service:
//
//   - webhook administration shared a store with the delivery workers, so a
//     tenant could list every tenant's endpoints, rotate their HMAC secrets
//     (returned in the response) and disable their delivery;
//   - the execution timeline ran on the owning pool, exposing another tenant's
//     governance history to anyone who knew a configuration id.
//
// Both were single-token mistakes — `pool` where `appPool` belonged — in code
// that compiled, passed every test, and read correctly. This test makes the
// mistake fail the build.
//
// The rule: anything handed to the HTTP server must be built from the
// tenant-scoped pool. Background workers are unconstrained; they legitimately
// process every tenant and are not reachable by a request.
const (
	mainFile      = "../../cmd/controlplane/main.go"
	scopedPoolVar = "appPool"
)

// wiringExemptions are constructor arguments that may use the privileged pool
// despite reaching the server. Each needs a reason, and the reason has to be
// about tenancy — not about convenience.
var wiringExemptions = map[string]string{
	// Resolving a bearer token to a tenant necessarily happens before any
	// tenant is known, so the registry cannot itself be tenant-scoped. The
	// `tenant` table is excluded from row-level security for the same reason
	// (ADR-TENANCY-001 §4).
	"SetTenants": "tenant resolution precedes tenant binding",

	// Reads its own scheduler state and verifies chains across every tenant.
	// It is an operator-facing integrity signal, not tenant data: confining it
	// would make it report healthy precisely because it could no longer see
	// anything to check.
	"SetGovernanceQuery": "audit integrity is a platform-wide operator signal",

	// Diagnostics and administration snapshots describe the process and its
	// dependencies, not tenant rows.
	"SetAdmin":       "process diagnostics, not tenant data",
	"SetDiagnostics": "process diagnostics, not tenant data",
}

// TestServerWiringUsesTenantScopedPool parses the composition root and fails if
// any dependency given to the HTTP server was constructed from the privileged
// pool.
func TestServerWiringUsesTenantScopedPool(t *testing.T) {
	path, err := filepath.Abs(mainFile)
	if err != nil {
		t.Fatalf("resolve main.go: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("composition root not found at %s: %v", mainFile, err)
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}

	// Which pool each local variable was constructed from, so a store assigned
	// to a variable and passed later is still attributed correctly.
	poolOf := map[string]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, rhs := range as.Rhs {
			if i >= len(as.Lhs) {
				break
			}
			name, ok := as.Lhs[i].(*ast.Ident)
			if !ok {
				continue
			}
			if p := poolArgOf(rhs); p != "" {
				poolOf[name.Name] = p
			}
		}
		return true
	})

	var violations []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		recv, ok := sel.X.(*ast.Ident)
		if !ok || recv.Name != "srv" || !strings.HasPrefix(sel.Sel.Name, "Set") {
			return true
		}
		setter := sel.Sel.Name
		if _, exempt := wiringExemptions[setter]; exempt {
			return true
		}

		for _, arg := range call.Args {
			switch a := arg.(type) {
			case *ast.Ident:
				// A variable: check what it was constructed from.
				if p, known := poolOf[a.Name]; known && p != scopedPoolVar {
					violations = append(violations, describe(fset, call.Pos(),
						setter, a.Name+" (built from "+p+")"))
				}
			case *ast.CallExpr:
				// Constructed inline.
				if p := poolArgOf(a); p != "" && p != scopedPoolVar {
					violations = append(violations, describe(fset, call.Pos(),
						setter, "inline constructor using "+p))
				}
			}
		}
		return true
	})

	for _, v := range violations {
		t.Error(v)
	}
	if len(violations) > 0 {
		t.Logf("A dependency reachable from an HTTP handler was built from the "+
			"privileged pool, which bypasses row-level security. Build it from %q "+
			"instead, or add it to wiringExemptions with a tenancy reason.", scopedPoolVar)
	}
}

// poolArgOf returns a pool identifier used by any postgres.NewX(...) call
// anywhere inside expr, preferring a privileged one so a mixed expression is
// reported rather than excused. It returns "" when the expression constructs no
// store.
//
// It walks the whole expression tree rather than inspecting only the outermost
// call. Stores are routinely wrapped — `timeline.NewService(postgres.NewTimelineStore(pool), …)`
// — and an earlier version of this test that matched only the top level passed
// cleanly against exactly the timeline disclosure it was written to prevent.
// A security control with a blind spot shaped like the bug is worse than none,
// because it is trusted.
func poolArgOf(expr ast.Expr) string {
	var found string
	ast.Inspect(expr, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "postgres" || !strings.HasPrefix(sel.Sel.Name, "New") {
			return true
		}
		for _, arg := range call.Args {
			id, ok := arg.(*ast.Ident)
			if !ok || !strings.Contains(strings.ToLower(id.Name), "pool") {
				continue
			}
			// A privileged pool anywhere in the expression taints the whole
			// dependency, so it wins over a scoped one found earlier.
			if found == "" || id.Name != scopedPoolVar {
				found = id.Name
			}
		}
		return true
	})
	return found
}

func describe(fset *token.FileSet, pos token.Pos, setter, detail string) string {
	return fset.Position(pos).String() + ": " + setter +
		" receives " + detail + "; request-path dependencies must use the tenant-scoped pool"
}

// The exemption list is a security control, so it must stay short and
// justified. A growing list means the rule is being worked around rather than
// followed.
func TestWiringExemptionsRemainJustified(t *testing.T) {
	const maxExemptions = 6
	if len(wiringExemptions) > maxExemptions {
		t.Errorf("%d wiring exemptions, expected at most %d — "+
			"each one is a subsystem outside tenant isolation",
			len(wiringExemptions), maxExemptions)
	}
	for setter, reason := range wiringExemptions {
		if strings.TrimSpace(reason) == "" {
			t.Errorf("%s is exempt with no reason recorded", setter)
		}
	}
}
