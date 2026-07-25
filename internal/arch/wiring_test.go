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

// Authorization scope is a property of routing.
//
// Operations on the tenant registry are platform operations: the registry is
// exempt from row-level security by necessity, so the middleware guarding it is
// the only control between a caller and every customer's record. They were
// routed under PermAdmin — the same permission that administers webhooks inside
// a tenant — and any tenant administrator could therefore enumerate every
// customer, register tenants binding identifiers of its choosing, and suspend a
// different customer outright. Verified against the running service; see
// ADR-AUTHZ-001.
//
// This asserts the routing, not the permission constant, because the defect was
// that a correct permission check guarded the wrong scope.
const routerFile = "../../internal/httpapi/server.go"

// platformHandlers must be reachable only through requirePlatformAdmin.
var platformHandlers = []string{"listTenants", "createTenant", "patchTenant"}

func TestPlatformRoutesRequirePlatformAdmin(t *testing.T) {
	path, err := filepath.Abs(routerFile)
	if err != nil {
		t.Fatalf("resolve router: %v", err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse router: %v", err)
	}

	// Handler name -> the middleware chain its route was registered With().
	guardOf := map[string]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		// Matches rt.With(<guard>).Method("/path", s.<handler>)
		method, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		with, ok := method.X.(*ast.CallExpr)
		if !ok {
			return true
		}
		withSel, ok := with.Fun.(*ast.SelectorExpr)
		if !ok || withSel.Sel.Name != "With" || len(with.Args) == 0 {
			return true
		}
		guard := exprText(with.Args[0])
		for _, arg := range call.Args {
			if sel, ok := arg.(*ast.SelectorExpr); ok {
				guardOf[sel.Sel.Name] = guard
			}
		}
		return true
	})

	for _, handler := range platformHandlers {
		guard, routed := guardOf[handler]
		if !routed {
			t.Errorf("%s is not routed in %s; if it was renamed, update platformHandlers",
				handler, routerFile)
			continue
		}
		if !strings.Contains(guard, "requirePlatformAdmin") {
			t.Errorf("%s is guarded by %q, not requirePlatformAdmin. Operations on the "+
				"tenant registry are platform operations: row-level security does not "+
				"confine them, so a tenant-scoped permission would let any tenant "+
				"administrator enumerate and suspend other customers.", handler, guard)
		}
	}
}

// exprText renders a guard expression well enough to identify it.
func exprText(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.SelectorExpr:
		return exprText(v.X) + "." + v.Sel.Name
	case *ast.Ident:
		return v.Name
	case *ast.CallExpr:
		var args []string
		for _, a := range v.Args {
			args = append(args, exprText(a))
		}
		return exprText(v.Fun) + "(" + strings.Join(args, ",") + ")"
	default:
		return "?"
	}
}

// Acting across tenants is forbidden, and no worker decides that for itself.
//
// A privileged worker may read every tenant's rows — the relay, the policy
// consumer and the replay worker all must, because they serve the whole
// installation from one process on a connection that bypasses row-level
// security by design. What none of them may do is act on what they read
// without re-establishing whose data it was.
//
// Two workers got that wrong in the same shape: the relay matched audit events
// to webhook subscriptions on operation and resource, and the policy consumer
// matched them to policy conditions on operation, resource, actor and metadata.
// Neither compared tenant. An unfiltered subscription — the documented way to
// subscribe to everything — therefore selected every other tenant's events.
//
// Ownership now lives in domain.FanOut and domain.SameOwner, so a worker's
// predicate answers only "is this the kind of thing I act on". This test fails
// if a worker package fans out without going through that framework.
var workerPackages = []string{"../events", "../policy"}

// callsIn returns the set of "pkg.Func" and ".Method" call names in a file,
// parsed from the syntax tree.
//
// Text scanning was tried first and was defeated by this test's own subject
// matter: the comment above the relay's fan-out names domain.FanOut, which
// satisfied a strings.Contains check even after the call itself was removed.
// The control passed against a deliberately reintroduced confused deputy. Only
// the AST distinguishes a call from a sentence about a call.
func callsIn(t *testing.T, path string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0) // no ParseComments: comments are not code
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	out := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			if pkg, ok := sel.X.(*ast.Ident); ok {
				out[pkg.Name+"."+sel.Sel.Name] = true
			}
			out["."+sel.Sel.Name] = true
		}
		return true
	})
	return out
}

func TestWorkersFanOutThroughOwnershipFramework(t *testing.T) {
	for _, dir := range workerPackages {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatalf("glob %s: %v", dir, err)
		}
		fanOutSites := 0
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			calls := callsIn(t, f)

			// A file that invokes a match predicate is fanning out: that is the
			// site where a privileged cross-tenant read becomes per-subscriber
			// work, and where ownership has to be re-established.
			if !calls[".Matches"] {
				continue
			}
			fanOutSites++
			if !calls["domain.FanOut"] && !calls["domain.SameOwner"] {
				t.Errorf("%s invokes a match predicate without calling domain.FanOut or "+
					"domain.SameOwner. A privileged worker that fans out without them "+
					"selects other tenants' events — the relay delivered a victim's "+
					"governance event to an attacker's endpoint exactly this way.", f)
			}
		}
		if fanOutSites == 0 {
			t.Errorf("no fan-out site found in %s — the detector matched nothing, "+
				"so it is proving nothing", dir)
		}
	}
}

// Execution-time ownership must be re-derived, never read from the queue.
//
// A privileged execution consumer performs an outbound action from a queued
// item. The queued item carries an owner label, but the label is forgeable: a
// row forged self-consistently — label matching the action's target while the
// event content belongs to another tenant — passed the dispatcher's first fix
// and was delivered, and the policy executor exfiltrated a victim's event to an
// attacker's endpoint the same way. Both were verified against the running
// service.
//
// The fix re-derives ownership from the audit log through
// domain.ResolveAndAuthorize. This test fails if a consumer stops calling it,
// or authorises from a queue-supplied owner instead — the two shapes the
// regression takes.
var executionConsumers = []string{
	"../events/dispatcher.go",
	"../policy/executor.go",
}

func TestExecutionConsumersReDeriveOwnership(t *testing.T) {
	for _, file := range executionConsumers {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		if !strings.Contains(string(src), "domain.ResolveAndAuthorize") {
			t.Errorf("%s does not call domain.ResolveAndAuthorize; a privileged "+
				"execution consumer must re-derive ownership from the authoritative "+
				"source, not authorise from the queued item's label", file)
		}

		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		// AuthorizeExecution must never be called directly by a consumer: it
		// authorises whatever Owned it is handed, so passing the queued item
		// (del / ex) reinstates label trust. Ownership must flow only through
		// ResolveAndAuthorize, which re-derives the owner first.
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "AuthorizeExecution" {
				return true
			}
			t.Errorf("%s calls AuthorizeExecution directly at %s; consumers must go "+
				"through domain.ResolveAndAuthorize so the owner is re-derived, never "+
				"taken from the queued item", file, fset.Position(call.Pos()))
			return true
		})
	}
}

// Replay owns no outbound action, and therefore no ownership authority.
//
// Replay is a producer: the time-window worker re-enqueues deliveries and the
// delivery-id worker requeues existing rows, both of which are then delivered by
// the dispatcher, which re-derives ownership through domain.ResolveAndAuthorize
// (ADR-TENANCY-003/004). Verified live: a cross-tenant replay produced nothing,
// and a replay of a split-brain event was refused at the dispatcher.
//
// That guarantee rests entirely on replay having no way to send. If a future
// change gave the replay worker an HTTP client and a direct-send path, it would
// bypass the authoritative check — the exact class this whole phase closes. This
// test fails if replay ever acquires an outbound capability, so replay stays a
// producer by construction rather than by discipline.
//
// The policy consumer and the webhook relay are producers for the same reason
// and are held to the same rule.
var producerOnlyWorkers = []string{
	"../events/replay.go",
	"../events/relay.go",
	"../policy/consumer.go",
}

func TestProducersHaveNoOutboundCapability(t *testing.T) {
	for _, file := range producerOnlyWorkers {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, imp := range f.Imports {
			if imp.Path.Value == `"net/http"` {
				t.Errorf("%s imports net/http; a producer must not be able to perform "+
					"an outbound request. Outbound work goes through the dispatcher, which "+
					"re-derives ownership — a direct-send producer would bypass it.", file)
			}
		}

		// Belt and braces: no call expression named Do (the HTTP client verb),
		// in case the client arrived by interface rather than by importing http.
		full, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		ast.Inspect(full, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Do" {
				t.Errorf("%s calls .Do() at %s; a producer must not perform outbound "+
					"requests directly", file, fset.Position(call.Pos()))
			}
			return true
		})
	}
}

// Operational tooling is production code and must satisfy the trust model.
//
// The only privileged binary is the control plane, whose privileged writes all
// go through the shared security framework: the request path on the
// tenant-scoped pool, the workers on the owning pool but with no outbound action
// except through the authoritative resolver, and every restore or schema change
// caught at startup. There is deliberately no separate repair or maintenance CLI
// that could write to the database outside that framework.
//
// This test pins that. A new binary under cmd/ — a bulk importer, a queue
// repair tool, a backfill — is exactly where an operator capability would bypass
// tenant stamping and authoritative ownership. Adding one fails the build until
// it is registered here, which forces the reviewer to state how it obtains
// ownership: through the same stores and resolver as production, never by
// encoding a security decision in a script.
var registeredBinaries = map[string]string{
	"controlplane": "the platform; all privileged writes go through the shared security framework",
}

func TestOperationalBinariesAreRegistered(t *testing.T) {
	entries, err := os.ReadDir("../../cmd")
	if err != nil {
		t.Fatalf("read cmd/: %v", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, ok := registeredBinaries[e.Name()]; !ok {
			t.Errorf("cmd/%s is an unregistered privileged binary. Operational tooling must "+
				"obtain ownership through the same stores and authoritative resolver as "+
				"production, never encode a security decision itself. Register it in "+
				"registeredBinaries with how it does so, or it must not exist.", e.Name())
		}
	}
}
