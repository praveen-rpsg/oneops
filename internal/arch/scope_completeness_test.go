package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
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
// The subject set is derived twice over. Which tables are global comes from the
// schema minus the tenant-ownership registry; which routes administer them
// comes from the router. So a new global table fails this test until someone
// says where it is administered, and a new route under an existing registry is
// covered the moment it is added — neither depends on anyone updating a list.
//
// The rule generalises the original: row-level security is what confines an
// /admin route to one customer's rows, and a global table has no policy at all.
// For those tables requirePlatformAdmin is not one control among several, it is
// the only one.
func TestEveryGlobalRegistryRoute_RequiresPlatformAdmin(t *testing.T) {
	const routerPath = "../httpapi/server.go"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, routerPath, nil, 0)
	if err != nil {
		t.Fatalf("parse router: %v", err)
	}

	// Where each global table is administered. A table mapped to "" exposes no
	// admin route at all, which is a stronger claim than gating one and so is
	// checked below rather than assumed.
	//
	// This map is the justified residue, not the subject set: the subject set is
	// computed from the schema, and a global table missing from here fails.
	globalRegistryPrefix := map[string]string{
		// Resolving a token to a tenant precedes binding one, so the registry
		// cannot be tenant-scoped (ADR-TENANCY-001 §4).
		"tenant": "/admin/tenants",
		// A person exists before, and independently of, any membership, so
		// app_user carries no owner (ADR-IDENTITY-002 §3.1).
		"app_user": "/admin/users",
		// tenant_id is discovered BY reading this mapping; gating the mapping on
		// it would be circular. Creating a row here creates a tenant, and
		// suspending one cascades to that tenant (ADR-IDENTITY-001 §7.1, §8.3).
		"organization": "/admin/organizations",
		// Worker processing state, not customer data, and never exposed over
		// HTTP. The empty prefix is checked: a route appearing under any of
		// these names would make this justification false.
		"webhook_cursor": "",
		"policy_cursor":  "",
	}

	for _, table := range globalTables(t) {
		if _, ok := globalRegistryPrefix[table]; !ok {
			t.Errorf("table %q is global — it is in the schema but not in TenantOwnedTables, so no "+
				"row-level security policy confines it. Record where it is administered in "+
				"globalRegistryPrefix, or \"\" if it is never exposed over HTTP", table)
		}
	}
	for table := range globalRegistryPrefix {
		if !slices.Contains(globalTables(t), table) {
			t.Errorf("globalRegistryPrefix names %q, which is no longer a global table; a "+
				"justification outliving its subject is how the map stops being read", table)
		}
	}

	// Every route the router registers, with whatever it was registered With.
	type route struct{ path, guard string }
	var routes []route
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
				if v := strings.Trim(lit.Value, `"`); strings.HasPrefix(v, "/admin/") {
					path = v
				}
			}
		}
		if path == "" {
			return true
		}
		guard := ""
		if with, ok := verb.X.(*ast.CallExpr); ok {
			if withSel, ok := with.Fun.(*ast.SelectorExpr); ok && withSel.Sel.Name == "With" && len(with.Args) > 0 {
				guard = exprTextOf(with.Args[0])
			}
		}
		routes = append(routes, route{path, guard})
		return true
	})
	if len(routes) == 0 {
		t.Fatal("no /admin routes found; the sweep would be vacuous — if the router moved, this " +
			"guard must move with it")
	}

	swept := 0
	for table, prefix := range globalRegistryPrefix {
		if prefix == "" {
			for _, r := range routes {
				if strings.HasPrefix(r.path, "/admin/"+table) {
					t.Errorf("route %s administers global table %q, which globalRegistryPrefix "+
						"claims is never exposed over HTTP; the justification is now false",
						r.path, table)
				}
			}
			continue
		}
		found := 0
		for _, r := range routes {
			if !strings.HasPrefix(r.path, prefix) {
				continue
			}
			found++
			swept++
			if !strings.Contains(r.guard, "requirePlatformAdmin") {
				t.Errorf("route %s administers global table %q and is registered with %q — every "+
					"route on a global registry must go through requirePlatformAdmin, because "+
					"the table is outside row-level security and that middleware is the only "+
					"control between a caller and every customer's record (ADR-AUTHZ-001)",
					r.path, table, r.guard)
			}
		}
		if found == 0 {
			t.Errorf("globalRegistryPrefix maps %q to %q but no route is mounted there; either "+
				"the prefix moved and this guard must move with it, or the registry is "+
				"unexposed and the prefix should be \"\"", table, prefix)
		}
	}
	t.Logf("global-registry routes swept: %d across %d tables", swept, len(globalRegistryPrefix))
}

// globalTables returns the schema's tables that TenantOwnedTables does not
// claim: those with no row-level security policy confining them to one tenant.
//
// Both halves are read from source rather than restated. A table added to a
// migration without being added to the ownership registry appears here
// automatically, which is the whole point — that combination is exactly how a
// table escapes isolation unnoticed.
func globalTables(t *testing.T) []string {
	t.Helper()
	owned := tenantOwnedTables(t)

	dir := "../store/migrate/sql"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	// CREATE TABLE [IF NOT EXISTS] <name>, capturing the name only.
	create := regexp.MustCompile(`(?i)^\s*CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z_][a-z0-9_]*)(.*)$`)

	var out []string
	seen := map[string]bool{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		for _, line := range strings.Split(readFile(t, filepath.Join(dir, e.Name())), "\n") {
			// Comments describe tables as often as statements create them, and a
			// prose mention must not enter the subject set.
			if strings.HasPrefix(strings.TrimSpace(line), "--") {
				continue
			}
			m := create.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			// A partition is not a table in its own right; its parent carries
			// the policy, and sweeping both would demand a justification for a
			// name no one administers.
			if strings.Contains(strings.ToUpper(m[2]), "PARTITION OF") {
				continue
			}
			name := m[1]
			if seen[name] || slices.Contains(owned, name) {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		t.Fatal("no global tables derived; the sweep would be vacuous — if the migration " +
			"directory moved, this helper must move with it")
	}
	sort.Strings(out)
	return out
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

// Completeness sweep for outcome durability (entry 21).
//
// ADR-CONCURRENCY-006 established that an outcome the platform has already
// produced in the outside world must be written on a context detached from the
// worker's cancellation. Its guard names two workers — the dispatcher and the
// policy executor. The replay worker is a third, and it wrote its outcome with
// the worker context: proven live, a replay executed under a cancelled context
// left its job in `running` with the outcome lost, and this queue has no lease
// recovery, so it is stuck there permanently (ADR-CONCURRENCY-008).
//
// The subject set is derived from the tree: every package that runs background
// work, and every outcome-recording call within it.
func TestEveryWorkerOutcomeWrite_UsesADetachedContext(t *testing.T) {
	// A method that records the result of work already performed. Matched on a
	// word boundary so `MarkResultFor` or a helper whose name merely ends in one
	// of these cannot slip through, and so `x.UpdateJobStatus` is not confused
	// for `x.UpdateJob`.
	outcomeWriters := regexp.MustCompile(`\.(MarkResult|UpdateJob)\(\s*ctx\b`)

	files := goFilesUnder(t, "..")
	workerPkgs := 0
	for _, f := range files {
		src := stripComments(f.src)
		// A worker package is one that runs a loop over a context.
		if !strings.Contains(src, "func (w *") && !strings.Contains(src, "func (d *") &&
			!strings.Contains(src, "func (e *") {
			continue
		}
		if !strings.Contains(src, "RunOnce(ctx") && !strings.Contains(src, "Run(ctx context.Context)") {
			continue
		}
		workerPkgs++
		if loc := outcomeWriters.FindString(src); loc != "" {
			t.Errorf("%s records an outcome with the worker's cancellable context (%q) — a "+
				"demotion or shutdown mid-work loses an outcome that already happened in the "+
				"outside world (ADR-CONCURRENCY-006/008)", f.path, strings.TrimSpace(loc))
		}
	}
	if workerPkgs == 0 {
		t.Fatal("no worker files found; the sweep would be vacuous")
	}
	t.Logf("worker files swept for outcome-write context: %d", workerPkgs)
}

// Completeness sweep for audit-chain append authority (entry 11).
//
// The chain-head lock is the platform's most load-bearing invariant: the gapless
// committed prefix (ADR-CONCURRENCY-004) and audit-derived ownership
// (ADR-TENANCY-003/004) both rest on it. Proven live — 12 concurrent appends
// take seqs 1..12 under the lock; without it only 4 commit and eight governance
// operations are lost.
//
// Entry 11's enforcement was an integration test with no architecture tier, and
// the lock was a *boolean argument*, so the unsafe form was expressible by
// forgetting a parameter. ADR-AUDIT-006 split it into a distinctly named method;
// this sweep proves no production code can reach the non-locking read.
func TestAuditAppend_TakesTheChainHeadLock(t *testing.T) {
	// The non-locking read exists for read-only verification. No production file
	// may call it — reaching it from an append path silently removes the
	// serialisation the whole governance model depends on.
	nonLocking := regexp.MustCompile(`\.ReadChainHead\(`)
	locking := regexp.MustCompile(`\.ReadChainHeadForUpdate\(`)

	files := goFilesUnder(t, "..")
	appenders := 0
	for _, f := range files {
		src := stripComments(f.src)
		// Strip the locking calls first: ".ReadChainHead(" is a substring of
		// ".ReadChainHeadForUpdate(", so matching the short name naively would
		// flag every correct call site.
		stripped := locking.ReplaceAllString(src, ".READ_LOCKED(")

		if nonLocking.MatchString(stripped) {
			t.Errorf("%s calls the non-locking ReadChainHead — every append path must serialise "+
				"on the chain head, or concurrent appends collide and governance events are lost "+
				"(ADR-AUDIT-006)", f.path)
		}
		// The append path itself must take the lock.
		if strings.Contains(src, "AppendAuditEvent(ctx, tx") {
			appenders++
			if !locking.MatchString(src) {
				t.Errorf("%s appends to the audit log without reading the chain head under "+
					"FOR UPDATE — this is the split-brain/lost-event class (ADR-AUDIT-006)", f.path)
			}
		}
	}
	if appenders == 0 {
		t.Fatal("no audit append path found; the sweep would be vacuous")
	}
	t.Logf("audit append paths swept: %d", appenders)
}

// The transport half of the cross-tenant key-confusion class (entry 1).
//
// Several unique keys on tenant-owned tables are justified as safe because the
// platform generates their identity — `configuration_object.cfg_id` and the
// `wh_`/`pol_`/`rpl_` ids. Those justifications hold only while a client cannot
// supply one. The repository itself honours a pre-set id by design (the
// governance engine and replay both construct rows with known ids), so the
// guarantee lives entirely at the transport boundary: no client-facing create
// DTO may expose an entity-identity field.
//
// Verified live during EVR-005: a POST carrying `"cfg_id":"ATTACKER-CHOSEN-ID"`
// was ignored and a server ULID assigned.
func TestCreateDTOs_DoNotExposeEntityIdentity(t *testing.T) {
	src := stripComments(readFile(t, "../httpapi/dto.go"))

	// Identity fields a client must never be able to set on a create.
	banned := []string{`json:"cfg_id`, `json:"id`, `json:"chain_id`, `json:"tenant_id`}

	checked := 0
	for _, decl := range []string{"type createRequest struct", "type patchRequest struct"} {
		i := strings.Index(src, decl)
		if i < 0 {
			t.Fatalf("%s not found in dto.go — this guard can no longer see the DTO it protects", decl)
		}
		checked++
		body := src[i:]
		if end := strings.Index(body, "\n}"); end > 0 {
			body = body[:end]
		}
		for _, b := range banned {
			if strings.Contains(body, b) {
				t.Errorf("%s exposes %s — a client-supplied entity identity turns every "+
					"single-column key on a tenant-owned table into a shared key space across "+
					"tenants, which is the defect Trust Register entry 1 records (EVR-005)",
					decl, b)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no create/patch DTOs examined; the guard would be vacuous")
	}
	t.Logf("client-facing DTOs checked for entity identity: %d", checked)
}

// Completeness sweep for leader step-down (entry 16).
//
// ADR-CONCURRENCY-003 made a demoted leader stop its workers: leadership runs
// under a context cancelled the instant the advisory lock is lost. That
// mechanism has two halves — the leader must cancel, and every worker must
// honour the cancellation. The first half is one line in ops.campaign and is
// covered by an integration test. The second half was never swept: a worker
// whose loop ignores ctx.Done() keeps running after demotion, and cancelling
// achieves nothing for it.
//
// The subject set is derived from the tree: any type with a
// `Run(ctx context.Context) error` loop is a worker, and its loop must observe
// cancellation.
func TestEveryWorkerRunLoop_ObservesCancellation(t *testing.T) {
	files := goFilesUnder(t, "..")
	checked := 0
	for _, f := range files {
		src := stripComments(f.src)
		i := strings.Index(src, ") Run(ctx context.Context) error {")
		for i >= 0 {
			body := src[i:]
			// The function body ends at the first line-start closing brace.
			if end := strings.Index(body, "\n}"); end > 0 {
				body = body[:end]
			}
			checked++
			if !strings.Contains(body, "ctx.Done()") {
				t.Errorf("%s has a Run loop that never observes ctx.Done() — a demoted leader "+
					"cancels the leadership context, but this worker would keep running, which "+
					"is the two-leader overlap ADR-CONCURRENCY-003 closed", f.path)
			}
			next := strings.Index(src[i+1:], ") Run(ctx context.Context) error {")
			if next < 0 {
				break
			}
			i = i + 1 + next
		}
	}
	if checked == 0 {
		t.Fatal("no worker Run loops found; the sweep would be vacuous — if the worker signature " +
			"changed, this guard must move with it")
	}
	t.Logf("worker Run loops swept for cancellation: %d", checked)
}

// Completeness guard for the metrics listener (entry 3).
//
// `/metrics` discloses audit-integrity state, per-route request volumes and
// dependency health. It is not tenant data, but it tells an attacker when audit
// verification is failing, so it belongs on its own listener.
//
// The Trust Register recorded `arch` enforcement for this entry. No architecture
// test existed — the protection was a production-config check plus a conditional
// mount, and the register overstated the tier (EVR-009). This is that guard.
func TestMetricsIsNotOnThePublicListenerInProduction(t *testing.T) {
	server := stripComments(readFile(t, "../httpapi/server.go"))

	// The public router may mount /metrics only when no separate observability
	// listener is configured — a development convenience, refused in production.
	i := strings.Index(server, `r.Handle("/metrics"`)
	if i < 0 {
		t.Log("the public router does not mount /metrics at all; stronger than required")
	} else {
		window := server[max0(i-300):i]
		if !strings.Contains(window, "MetricsAddr ==") {
			t.Error("the public router mounts /metrics unconditionally — it would be reachable on " +
				"the tenant-facing listener in every deployment, disclosing audit-integrity and " +
				"dependency state (entry 3)")
		}
	}

	// And production configuration must refuse the conditional case outright.
	cfg := stripComments(readFile(t, "../config/config.go"))
	if !strings.Contains(cfg, "ONEOPS_METRICS_ADDR is empty") {
		t.Error("production config validation no longer refuses an empty ONEOPS_METRICS_ADDR, so " +
			"a production deployment could serve /metrics on the public listener (entry 3)")
	}
	if !strings.Contains(cfg, "insecure production configuration") {
		t.Error("production config problems no longer fail startup — the metrics check would be " +
			"advisory rather than enforced (entry 3)")
	}
}

func max0(i int) int {
	if i < 0 {
		return 0
	}
	return i
}
