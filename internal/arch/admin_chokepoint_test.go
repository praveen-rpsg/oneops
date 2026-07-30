package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// adminAuditedTables is ADR-AUDIT-007 §6.2's scope: "every mutation of app_user,
// organization, membership, invitation and tenant, however performed".
//
// This is a justified residue, not the subject set. The subject set — which
// functions mutate these tables — is derived from the tree below. What cannot be
// derived is *which* tables are administrative: nothing in the schema marks
// them, so the five names come from the ADR and are checked against the schema
// in both directions. A name that no longer exists fails as stale, which is what
// stops this map outliving its subject the way enumerated guards have three
// times before in this codebase (ADR-TENANCY-009 §"Why the original enforcement
// failed").
var adminAuditedTables = map[string]string{
	"app_user":     "a person; created, renamed and lifecycled by platform administrators",
	"organization": "an Identity scope; created and suspended administratively (ADR-IDENTITY-001)",
	"membership":   "the grant that binds a person to an organisation",
	"invitation":   "the token that becomes a membership; issued, revoked and redeemed",
	"tenant":       "the isolation boundary an organisation is realised as",
}

// chokepointCall is the single write path administrative mutations must pass
// through (OPS-S035). Matching the call by name is deliberate: the point of the
// chokepoint is that there is exactly one, so its name is the invariant.
const chokepointCall = "withAdminAudit("

// writesTable reports whether a function body issues any mutating statement
// against the table. mutatesTable already handles UPDATE and DELETE with the
// word-boundary care that keeps "UPDATE webhook" from matching
// "UPDATE webhook_delivery"; INSERT is added here with the same discipline
// rather than by widening mutatesTable, whose other callers sweep a different
// subject set.
func writesTable(body, table string) (insert, update bool) {
	if mutatesTable(body, table) {
		update = true
	}
	verb := "INSERT INTO " + table
	i := strings.Index(body, verb)
	for i >= 0 {
		rest := body[i+len(verb):]
		if rest == "" {
			insert = true
			break
		}
		switch rest[0] {
		case ' ', '\n', '\t', '\r', '(':
			insert = true
		}
		if insert {
			break
		}
		next := strings.Index(rest, verb)
		if next < 0 {
			break
		}
		i = i + len(verb) + next
	}
	return insert, update
}

// Every administrative mutation must pass the audit chokepoint.
//
// ADR-AUDIT-007 §6.9 makes an unauditable administrative act fail, and OPS-S035
// realised that as a single write path: withAdminAudit owns the transaction, so
// the mutation and its audit record commit together or neither does. A guard is
// what keeps that true after the people who wrote it have moved on — a store
// method added next quarter that calls s.pool.Exec directly would be silently
// unaudited, and nothing else in the tree would notice.
//
// The subject set is DERIVED, not listed. Every non-test Go file in the module
// is parsed, every function that issues a mutating statement against one of
// §6.2's tables is collected, and each must contain the chokepoint call. Adding
// a sixth store, a new handler that writes directly, or a helper that bypasses
// the transaction all enter the sweep automatically.
func TestEveryAdministrativeMutation_PassesTheAuditChokepoint(t *testing.T) {
	// A table named here that the schema no longer creates means this map has
	// stopped describing the tree, which is how an enumerated guard rots.
	migrations := readMigrations(t)
	for table, why := range adminAuditedTables {
		if !strings.Contains(migrations, "CREATE TABLE IF NOT EXISTS "+table+" (") {
			t.Errorf("adminAuditedTables names %q (%s) but no migration creates it; the "+
				"justification is stale and the guard is no longer sweeping what it claims", table, why)
		}
	}

	sites, guarded := 0, 0
	// Counted separately so each half of the detector carries its own weight.
	// The tree contains both insert-only sites (Create) and update-only sites
	// (SetStatus, Revoke, Consume); if either matcher broke, the other would
	// keep the sweep non-empty and the blindness would pass unnoticed.
	insertSites, updateSites := 0, 0
	for _, gf := range goFilesUnder(t, "..") {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, gf.path, gf.src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", gf.path, err)
		}

		ast.Inspect(f, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}
			// Comments are stripped from the extracted body, not from the file:
			// stripComments does not preserve string literals, so a stripped
			// file will not parse. Prose that mentions a table must not enter
			// the subject set, but the SQL that mutates one must.
			body := stripComments(gf.src[fset.Position(fn.Pos()).Offset:fset.Position(fn.End()).Offset])

			var mutated []string
			sawInsert, sawUpdate := false, false
			for table := range adminAuditedTables {
				ins, upd := writesTable(body, table)
				if ins || upd {
					mutated = append(mutated, table)
				}
				sawInsert = sawInsert || ins
				sawUpdate = sawUpdate || upd
			}
			if len(mutated) == 0 {
				return true
			}

			sites++
			if sawInsert {
				insertSites++
			}
			if sawUpdate {
				updateSites++
			}
			if strings.Contains(body, chokepointCall) {
				guarded++
				return false
			}
			t.Errorf("%s.%s mutates %s without passing the audit chokepoint. Every "+
				"administrative mutation must run inside %s so the row and its audit record "+
				"commit in one transaction — an act that cannot be audited must fail "+
				"(ADR-AUDIT-007 §6.9, OPS-S035).",
				gf.path, fn.Name.Name, strings.Join(mutated, ", "), chokepointCall)
			return false
		})
	}

	// Anti-vacuity, in the established style. A guard that sweeps nothing passes
	// forever: if the detector breaks — a renamed table, a changed SQL shape, a
	// tree walk that stops finding files — this fails loudly instead of quietly
	// certifying an unguarded platform.
	if sites == 0 {
		t.Fatal("found no administrative mutation to check — the sweep is vacuous. Either the " +
			"detector is broken or every store stopped writing the §6.2 tables; both are faults.")
	}
	if insertSites == 0 {
		t.Fatal("no INSERT against a §6.2 table was recognised anywhere in the tree; the insert " +
			"matcher is blind and every creation path would go unswept")
	}
	if updateSites == 0 {
		t.Fatal("no UPDATE/DELETE against a §6.2 table was recognised anywhere in the tree; the " +
			"mutatesTable matcher is blind and every lifecycle path would go unswept")
	}
	t.Logf("administrative mutation sites swept: %d (insert %d, update/delete %d), all through %s",
		guarded, insertSites, updateSites, chokepointCall)
}

// The chokepoint must remain the only door.
//
// The guard above proves every mutation passes through withAdminAudit. This
// proves withAdminAudit is worth passing through: exactly one definition, so a
// second helper with the same name and weaker behaviour cannot appear beside it,
// and the transaction it owns is the one the mutation runs on.
func TestAuditChokepoint_HasExactlyOneDefinition(t *testing.T) {
	definitions := 0
	for _, gf := range goFilesUnder(t, "..") {
		src := gf.src
		if strings.Contains(src, "func withAdminAudit(") {
			definitions++
			// The chokepoint must begin the transaction it hands out. A version
			// that accepted a caller's transaction could not guarantee the audit
			// append shares it, which is the whole guarantee.
			if !strings.Contains(src, "pool.Begin(ctx)") {
				t.Errorf("%s defines withAdminAudit without beginning its own transaction; the "+
					"chokepoint's guarantee is that the mutation and its audit record share one",
					gf.path)
			}
		}
	}
	if definitions != 1 {
		t.Fatalf("withAdminAudit has %d definitions, want exactly 1 — the administrative write "+
			"path must be single", definitions)
	}
}
