package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A privileged (RLS-bypassing) read that scopes to one tenant must say so in SQL.
//
// This is the read-side analogue of TestPrivilegedMutations_AreScopedToAnOwner
// and closes the follow-up ADR-TENANCY-012 recorded but did not build.
//
// The hazard (ADR-TENANCY-012 §Context): every tenant-owned repository omits a
// tenant_id predicate and is correct ONLY because it runs on the tenant-scoped
// pool, where row-level security supplies the filter. The privileged pool holds
// BYPASSRLS by design (ADR-TENANCY-002), so the same statement run there filters
// on nothing but asset_id/metric — "trusting that no two tenants' rows can ever
// match them, an assumption that happens to hold today only because asset_id is a
// platform-minted ULID, not because anything enforces it." telemetry_sample's
// primary key is (tenant_id, asset_id, metric, ts): two tenants CAN share an
// asset_id, so a privileged read keyed on asset_id alone can return both.
//
// The subject set is derived, not named:
//
//   - the privileged store TYPES are read from the composition root (main.go) —
//     any postgres.NewX(...) constructed over the privileged pool rather than the
//     tenant-scoped appPool — exactly how TestServerWiringUsesTenantScopedPool
//     derives privileged-vs-scoped wiring. A type built only over appPool (e.g.
//     AssetStore) is never swept, so its RLS-scoped asset_id reads are not false
//     positives;
//   - the tenant-owned tables are read from postgres.TenantOwnedTables;
//   - the dangerous shape is a SELECT from one of those tables that filters by
//     asset_id (asset_id = $ / asset_id = ANY) without an explicit
//     tenant_id = $ predicate. asset_id is the one tenant-shared, non-globally-
//     unique key used as a read predicate in this schema; a by-primary-key
//     lookup (WHERE incident_id = $1) returns a single globally-unique row and is
//     not this class, exactly as the mutation guard treats WHERE id=$1 as already
//     confined.
//
// PRECISION / RECALL — stated honestly, per Trust-Register discipline:
//
//   - The tenant_id check matches a PREDICATE (tenant_id = $), never the
//     projection (SELECT tenant_id, ...). QueryRange projects tenant_id but does
//     NOT filter on it, so it is correctly flagged; QueryRangeForTenant filters on
//     it and passes. The canary assertions below pin this so the guard cannot
//     regress into the illusory-coverage flaw the SSRF guard had.
//   - This guard is TYPE-granular, not instance-granular. It cannot see which
//     pool a given call site holds, so a read that is safe only because it is
//     invoked exclusively on the scoped pool (QueryRange) is flagged and must be
//     justified below. Proving it is never reached from a privileged instance is
//     a SEPARATE control (alerting.TelemetryReader deliberately excludes
//     QueryRange); this guard does not claim that.
//   - RECALL is bounded to the asset_id-scoped class. A future privileged read
//     that scopes to one tenant by a DIFFERENT shared key (a bare metric, a
//     recipient, an FK on a child table) without tenant_id would NOT be caught.
//     Widening the key set is the way to extend it; the value is checked, not
//     assumed. This mirrors the mutation guard, which likewise catches only its
//     one narrow shape (id = ANY($)) rather than every possible ownership hole.
//   - CTE-with-UPDATE BLIND SPOT (recorded 2026-08-05, E3.3a review). A read
//     embedded in a statement that also contains "UPDATE <table>" (a
//     write-CTE like MaintenanceWindowStore.Suppress or
//     IncidentStore.FindOrCreateOpenAlertIncident) is SKIPPED here as the
//     mutation guard's subject (see the DELETE/UPDATE skip below) — yet the
//     mutation guard only fires on the id = ANY($) shape, which such methods
//     do not have. So neither STATIC guard enforces the tenant predicate on a
//     read hidden inside a write-CTE; that predicate is enforced only by a live
//     cross-tenant integration test per method (e.g.
//     TestMaintenanceWindowStore_SuppressIsTenantIsolated). Do NOT assume the
//     static sweep covers this class. Closing it is tracked debt (plan: "arch
//     guard — CTE-with-UPDATE tenant-read coverage").
//
// privilegedReadExemptions: each is a privileged-type SELECT that filters by
// asset_id without tenant_id but is not a cross-tenant disclosure. A reason per
// entry, and a stale entry (one that no longer matches a detected read) fails —
// the discipline the work-queue and wiring guards use.
var privilegedReadExemptions = map[string]string{
	"TelemetryStore.QueryRange": "the RLS-scoped telemetry read: it carries no tenant predicate BY DESIGN " +
		"because row-level security supplies it on the tenant-scoped pool (its own doc comment says so). " +
		"It must never be invoked on a privileged instance; its privileged counterpart is " +
		"QueryRangeForTenant. That it is unreachable from a privileged consumer is enforced separately by " +
		"alerting.TelemetryReader excluding it, not by this type-granular guard (ADR-TENANCY-012).",
	"TelemetryStore.queryRollupRange": "the rollup arm of QueryRange, reached only through QueryRange; same " +
		"RLS-scoped contract and same exclusion from the privileged read surface (ADR-TENANCY-012).",
	"TelemetryStore.WriteSamples": "echoes back only the asset_ids the caller already supplied " +
		"(SELECT asset_id FROM asset WHERE asset_id = ANY($1)) to reject unknown assets; it discloses no " +
		"tenant row data and gates an RLS/FK-confined INSERT.",
	"AlertRuleStore.Create": "asset existence probe (SELECT EXISTS(SELECT 1 FROM asset WHERE asset_id=$1)): " +
		"returns a boolean, discloses no tenant row data, and gates an RLS/FK-confined INSERT. Runs on the " +
		"request-path admin instance; listed because AlertRuleStore is dual-role and this guard is type-granular.",
	"CollectorCheckStore.Create": "asset existence probe; identical shape and reason to AlertRuleStore.Create " +
		"(ADR-TENANCY-012).",
	"IncidentStore.verifyAssetExists": "asset existence probe; identical shape and reason to " +
		"AlertRuleStore.Create (ADR-TENANCY-012).",
	"MaintenanceWindowStore.Create": "asset existence probe; identical shape and reason to " +
		"AlertRuleStore.Create (ADR-TENANCY-012). Runs on the request-path admin instance; listed because " +
		"MaintenanceWindowStore is dual-role (the same admin-CRUD/evaluator-surface split AlertRuleStore " +
		"draws) and this guard is type-granular.",
}

func TestPrivilegedReads_AreScopedToATenant(t *testing.T) {
	tenantTables := tenantOwnedTables(t)
	if len(tenantTables) == 0 {
		t.Fatal("no tenant-owned tables found; the sweep would be vacuous")
	}
	privileged := privilegedStoreTypes(t)
	if len(privileged) == 0 {
		t.Fatal("no privileged store types derived from the composition root; the sweep would be vacuous — " +
			"if main.go moved, this guard must move with it")
	}

	assetPred := regexp.MustCompile(`asset_id\s*=\s*(\$|ANY)`)
	tenantPred := regexp.MustCompile(`tenant_id\s*=\s*\$`)

	// candidates: privileged-type reads that filter by asset_id on a tenant-owned
	// table WITHOUT a tenant_id predicate. safe: the same shape WITH one — kept so
	// the canary can prove the predicate/projection distinction actually works.
	candidates := map[string]string{} // "Type.Method" -> table
	safe := map[string]bool{}         // "Type.Method"

	for _, m := range privilegedStoreMethods(t, privileged) {
		for _, lit := range sqlLiterals(m.body) {
			if !assetPred.MatchString(lit) {
				continue
			}
			// Key on the FROM+WHERE literal, not on the SELECT keyword: the house
			// style concatenates the column list (SELECT `+cols+` FROM ... WHERE),
			// which puts SELECT in a separate tiny literal while FROM and the WHERE
			// predicates stay together in the trailing one. Requiring SELECT here
			// would let a future unsafe read evade the guard simply by using that
			// dominant pattern.
			table := fromTenantTable(lit, tenantTables)
			if table == "" {
				continue
			}
			// Mutations are the mutation guard's subject (ADR-TENANCY-009); this
			// guard is about disclosure through reads.
			if strings.Contains(lit, "DELETE FROM "+table) || strings.Contains(lit, "UPDATE "+table) {
				continue
			}
			key := m.typ + "." + m.name
			if tenantPred.MatchString(lit) {
				safe[key] = true
				continue
			}
			candidates[key] = table
		}
	}

	// Canary: the detector must flag the canonical unsafe read and clear its
	// predicated counterpart. If either flips, the guard has gone blind and every
	// pass below is worthless (this is exactly the SSRF-guard failure mode).
	if _, ok := candidates["TelemetryStore.QueryRange"]; !ok {
		t.Fatal("canary failed: TelemetryStore.QueryRange (asset_id filter, no tenant_id predicate) was NOT " +
			"flagged — the detector is not seeing the very read ADR-TENANCY-012 is about; the guard is vacuous")
	}
	if !safe["TelemetryStore.QueryRangeForTenant"] {
		t.Fatal("canary failed: TelemetryStore.QueryRangeForTenant was not recognised as tenant-predicated — " +
			"the guard is matching the projected tenant_id column instead of the WHERE predicate, so a read " +
			"that merely SELECTs tenant_id would pass unscoped (illusory coverage)")
	}

	for key, table := range candidates {
		if _, exempt := privilegedReadExemptions[key]; exempt {
			continue
		}
		t.Errorf("%s issues a privileged SELECT on tenant-owned %q filtered by asset_id with no explicit "+
			"tenant_id predicate. On the privileged pool RLS is off, so two tenants sharing an asset_id are "+
			"both returned. Add WHERE tenant_id = $N sourced from the authoritative row (ADR-TENANCY-012), "+
			"or, if the read is genuinely tenant-agnostic, add it to privilegedReadExemptions with a reason.",
			key, table)
	}

	// A justification must describe a read that still exists. One that matches no
	// detected candidate is stale, and a stale exemption is how the next real
	// unscoped read slips in under a name someone once excused.
	for key := range privilegedReadExemptions {
		if _, ok := candidates[key]; !ok {
			t.Errorf("privilegedReadExemptions names %q, but no privileged asset_id read without a tenant_id "+
				"predicate matches it — the justification is stale and must be removed", key)
		}
	}
	t.Logf("privileged store types: %d; asset_id-scoped reads — safe(predicated): %d, exempted: %d",
		len(privileged), len(safe), len(candidates))
}

// The exemption list is a security control: short and each entry justified. A
// growing list means the rule is being worked around, not followed.
func TestPrivilegedReadExemptionsRemainJustified(t *testing.T) {
	const maxExemptions = 8
	if len(privilegedReadExemptions) > maxExemptions {
		t.Errorf("%d privileged-read exemptions, expected at most %d — each one is a privileged read outside "+
			"the tenant-predicate rule and must earn its place", len(privilegedReadExemptions), maxExemptions)
	}
	for key, reason := range privilegedReadExemptions {
		if strings.TrimSpace(reason) == "" {
			t.Errorf("%s is exempt with no reason recorded", key)
		}
	}
}

// privilegedStoreTypes parses the composition root and returns the set of store
// type names constructed over the privileged pool (any pool identifier that is
// not the tenant-scoped appPool). NewX is mapped to X by the package's own
// constructor convention (verified: every postgres.NewX returns *X).
func privilegedStoreTypes(t *testing.T) map[string]bool {
	t.Helper()
	const scopedPoolVar = "appPool"
	path, err := filepath.Abs("../../cmd/controlplane/main.go")
	if err != nil {
		t.Fatalf("resolve main.go: %v", err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}
	out := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
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
			if id.Name != scopedPoolVar {
				out[strings.TrimPrefix(sel.Sel.Name, "New")] = true
			}
		}
		return true
	})
	return out
}

type storeMethod struct {
	typ  string
	name string
	body string // source from `func` through the closing brace, incl. SQL literals
}

// privilegedStoreMethods returns every method whose receiver type is in the
// privileged set, with its source text, parsed from the store package.
func privilegedStoreMethods(t *testing.T, privileged map[string]bool) []storeMethod {
	t.Helper()
	dir := "../store/postgres"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	var out []storeMethod
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src, rerr := os.ReadFile(filepath.Join(dir, e.Name()))
		if rerr != nil {
			t.Fatalf("read %s: %v", e.Name(), rerr)
		}
		f, perr := parser.ParseFile(fset, e.Name(), src, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", e.Name(), perr)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 {
				continue
			}
			typ := receiverType(fn.Recv.List[0].Type)
			if !privileged[typ] {
				continue
			}
			// Slice from the `func` keyword (not the doc comment) to the end, so
			// backticks inside a doc comment cannot be read as SQL.
			start := fset.Position(fn.Pos()).Offset
			end := fset.Position(fn.End()).Offset
			out = append(out, storeMethod{typ: typ, name: fn.Name.Name, body: string(src[start:end])})
		}
	}
	if len(out) == 0 {
		t.Fatal("no methods found on any privileged store type; the sweep would be vacuous")
	}
	return out
}

func receiverType(e ast.Expr) string {
	if star, ok := e.(*ast.StarExpr); ok {
		e = star.X
	}
	if id, ok := e.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// sqlLiterals returns the raw (backtick-delimited) string literals in a method
// body — the statements, kept whole so a tenant_id predicate is checked against
// the same statement the asset_id predicate is in, not against the body at large.
func sqlLiterals(body string) []string {
	parts := strings.Split(body, "`")
	var out []string
	for i := 1; i < len(parts); i += 2 { // odd segments are inside backticks
		out = append(out, parts[i])
	}
	return out
}

// fromTenantTable reports which tenant-owned table a statement reads FROM,
// matching on a word boundary so a name that prefixes another (telemetry_rollup
// vs telemetry_rollup_5m) is not confused for it.
func fromTenantTable(lit string, tables []string) string {
	for _, table := range tables {
		needle := "FROM " + table
		i := strings.Index(lit, needle)
		for i >= 0 {
			rest := lit[i+len(needle):]
			if rest == "" {
				return table
			}
			c := rest[0]
			if c == ' ' || c == '\n' || c == '\t' || c == '\r' {
				return table
			}
			next := strings.Index(rest, needle)
			if next < 0 {
				break
			}
			i = i + len(needle) + next
		}
	}
	return ""
}
