package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Administrative audit events must never be observable by a tenant subscriber
// (ADR-AUDIT-007 §6.5). These guards derive everything they need from the tree:
// no filename, no function name, no table name and no event name is written
// down here.
//
// The derivation runs in three steps, each anchored on the previous:
//
//  1. The administrative write path is the call graph of the audit chokepoint —
//     found as the sole function whose declaration the S036 guard pins.
//  2. The administrative tables are whatever that call graph's SQL touches.
//  3. The delivery path is whatever reaches the statement that enqueues a
//     webhook delivery, and the discovery surface is the set of methods a
//     consumer package can call through an interface.
//
// The invariant then has two halves, and both are necessary. A leak happens
// either because something in the delivery path learns to read an
// administrative table, or because an administrative row is written into a
// table the delivery path already reads.

var sqlTableRe = regexp.MustCompile(`(?is)(?:INSERT\s+INTO|UPDATE|DELETE\s+FROM|FROM|JOIN)\s+([a-z_][a-z0-9_]*)`)

// tablesIn returns every table name the SQL in a body refers to.
func tablesIn(body string) map[string]bool {
	out := map[string]bool{}
	for _, m := range sqlTableRe.FindAllStringSubmatch(body, -1) {
		out[strings.ToLower(m[1])] = true
	}
	return out
}

// funcBody is one function declaration with its source text.
type funcBody struct {
	pkgDir string
	name   string
	recv   string
	src    string
	calls  map[string]bool
}

// allFuncs parses every non-test Go file in the module and returns each
// function with its body text, comments stripped so prose cannot enter any
// subject set.
func allFuncs(t *testing.T) []funcBody {
	t.Helper()
	var out []funcBody
	for _, gf := range goFilesUnder(t, "..") {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, gf.path, gf.src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", gf.path, err)
		}
		dir := gf.path[:strings.LastIndex(gf.path, "/")]
		ast.Inspect(f, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}
			recv := ""
			if fn.Recv != nil && len(fn.Recv.List) > 0 {
				recv = exprTypeName(fn.Recv.List[0].Type)
			}
			calls := map[string]bool{}
			ast.Inspect(fn.Body, func(m ast.Node) bool {
				call, ok := m.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch fun := call.Fun.(type) {
				case *ast.Ident:
					calls[fun.Name] = true
				case *ast.SelectorExpr:
					calls[fun.Sel.Name] = true
				}
				return true
			})
			out = append(out, funcBody{
				pkgDir: dir,
				name:   fn.Name.Name,
				recv:   recv,
				src:    stripComments(gf.src[fset.Position(fn.Pos()).Offset:fset.Position(fn.End()).Offset]),
				calls:  calls,
			})
			return false
		})
	}
	if len(out) == 0 {
		t.Fatal("parsed no functions — the tree walk is broken and every assertion below would be vacuous")
	}
	return out
}

func exprTypeName(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.StarExpr:
		return exprTypeName(v.X)
	case *ast.Ident:
		return v.Name
	}
	return ""
}

// administrativeTables derives the tables the audit chokepoint's call graph
// writes, without naming any of them.
//
// The chokepoint is located by its own definition — the same single-door
// property the S036 guard pins — then every function it calls is resolved
// within the same package, and the SQL of that closure is collected. Rename the
// chokepoint, move it, or change which tables it writes, and this follows.
func administrativeTables(t *testing.T, funcs []funcBody) (tables map[string]bool, reachSet map[string]bool, pkgDir string) {
	t.Helper()

	var entry *funcBody
	for i := range funcs {
		if strings.HasPrefix(funcs[i].src, "func "+chokepointName+"(") {
			if entry != nil {
				t.Fatalf("%s has more than one definition; the administrative write path must be single", chokepointName)
			}
			entry = &funcs[i]
		}
	}
	if entry == nil {
		t.Fatalf("no definition of %s found — the administrative write path has moved or been "+
			"removed, and this guard can no longer derive what it protects", chokepointName)
	}

	// A real call-graph walk over AST edges, confined to the chokepoint's own
	// package. Substring matching over bodies was tried first and over-reached:
	// it pulled in unrelated functions and derived a table the administrative
	// path never touches, which would have made every assertion below compare
	// the wrong sets.
	byName := map[string][]funcBody{}
	for _, f := range funcs {
		if f.pkgDir == entry.pkgDir {
			byName[f.name] = append(byName[f.name], f)
		}
	}
	reach := map[string]bool{entry.name: true}
	queue := []funcBody{*entry}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for callee := range cur.calls {
			if reach[callee] {
				continue
			}
			next, ok := byName[callee]
			if !ok {
				continue
			}
			reach[callee] = true
			queue = append(queue, next...)
		}
	}

	// Only tables the reached closure WRITES identify the administrative store.
	written := map[string]bool{}
	for _, f := range funcs {
		if f.pkgDir != entry.pkgDir || !reach[f.name] {
			continue
		}
		for _, m := range regexp.MustCompile(`(?is)INSERT\s+INTO\s+([a-z_][a-z0-9_]*)`).FindAllStringSubmatch(f.src, -1) {
			written[strings.ToLower(m[1])] = true
		}
	}
	if len(written) == 0 {
		t.Fatalf("derived no administrative table from %s's call graph — the derivation is broken "+
			"and every assertion below would be vacuous", chokepointName)
	}
	return written, reach, entry.pkgDir
}

// chokepointName is derived from the S036 guard's own constant so the two
// cannot drift apart. It is the only identifier this file shares with the
// implementation, and it is the identifier the platform's single-door property
// is defined by.
var chokepointName = strings.TrimSuffix(chokepointCall, "(")

// HALF ONE — nothing outside the administrative write path may read an
// administrative table.
//
// The relay, the replay worker and the policy consumer all reach events through
// a store. If none of them may so much as name an administrative table, none of
// them can deliver one. The two legitimate exceptions are derived from the
// function's own body, not from a list: a function is allowed to touch these
// tables when it is part of the write path, or when it is inspecting the
// database catalogue (a schema or invariant validator, which reads pg_* rather
// than the rows).
func TestAdministrativeTables_AreUnreadableOutsideTheWritePath(t *testing.T) {
	funcs := allFuncs(t)
	adminTables, writePath, writePathDir := administrativeTables(t, funcs)

	names := make([]string, 0, len(adminTables))
	for n := range adminTables {
		names = append(names, n)
	}
	sort.Strings(names)

	readers := 0
	for _, f := range funcs {
		touched := tablesIn(f.src)
		var hit []string
		for tbl := range adminTables {
			if touched[tbl] {
				hit = append(hit, tbl)
			}
		}
		if len(hit) == 0 {
			continue
		}
		readers++

		// Membership of the write path is membership of the chokepoint's call
		// graph, NOT of its package. Exempting the whole package would exempt
		// the constitutional event source, which lives beside it — precisely
		// the function that must never learn to read these tables.
		inWritePath := f.pkgDir == writePathDir && writePath[f.name]
		catalogue := strings.Contains(f.src, "pg_trigger") ||
			strings.Contains(f.src, "pg_class") ||
			strings.Contains(f.src, "pg_inherits") ||
			strings.Contains(f.src, "information_schema")
		if inWritePath || catalogue {
			continue
		}
		sort.Strings(hit)
		t.Errorf("%s.%s references %s but is neither part of the administrative write path nor a "+
			"catalogue validator. Administrative audit is not an event source: anything that can "+
			"read it can deliver it, and ADR-AUDIT-007 §6.5 confines it to platform administrators.",
			f.pkgDir, f.name, strings.Join(hit, ", "))
	}
	if readers == 0 {
		t.Fatalf("no function anywhere references %v — the derivation found tables nothing uses, "+
			"so this sweep proves nothing", names)
	}
	t.Logf("administrative tables derived: %v; functions referencing them: %d", names, readers)
}

// HALF TWO — the tables the delivery path reads must not be tables the
// administrative write path writes.
//
// Half one stops the delivery path reading administrative tables. This stops
// the converse: an administrative row written into a table the delivery path
// already reads would be fanned out correctly, by a relay doing exactly its job.
//
// The delivery surface is derived, not named. Every interface declared outside
// the store package is a port some consumer calls through; any store function
// whose name matches one of those port methods is reachable by a consumer. The
// tables those functions read are what a subscriber can ultimately observe.
func TestAdministrativeWritePath_AndDeliveryPath_AreDisjoint(t *testing.T) {
	funcs := allFuncs(t)
	adminTables, _, writePathDir := administrativeTables(t, funcs)

	portMethods := map[string]bool{}
	for _, gf := range goFilesUnder(t, "..") {
		dir := gf.path[:strings.LastIndex(gf.path, "/")]
		if dir == writePathDir {
			continue // a port is declared by the consumer, not the implementation
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, gf.path, gf.src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", gf.path, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			it, ok := n.(*ast.InterfaceType)
			if !ok || it.Methods == nil {
				return true
			}
			for _, m := range it.Methods.List {
				for _, nm := range m.Names {
					portMethods[nm.Name] = true
				}
			}
			return true
		})
	}
	if len(portMethods) == 0 {
		t.Fatal("derived no port method — no interface was found outside the store package, so " +
			"the delivery surface is unknown and this assertion would be vacuous")
	}

	observable := map[string]bool{}
	served := 0
	for _, f := range funcs {
		if f.pkgDir != writePathDir || !portMethods[f.name] {
			continue
		}
		served++
		for tbl := range tablesIn(f.src) {
			observable[tbl] = true
		}
	}
	if served == 0 {
		t.Fatal("no store function serves any port — the delivery surface derived empty and this " +
			"assertion would be vacuous")
	}

	for tbl := range adminTables {
		if observable[tbl] {
			t.Errorf("table %q is written by the administrative audit path AND readable through a "+
				"port a consumer calls. Administrative events would be fanned out to tenant "+
				"subscribers by a relay doing exactly its job (ADR-AUDIT-007 §6.5).", tbl)
		}
	}
	t.Logf("port-serving store functions: %d; tables observable through them: %d", served, len(observable))
}
