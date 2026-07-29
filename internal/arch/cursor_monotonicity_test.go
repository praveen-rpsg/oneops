package arch_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// A cursor is a watermark and must never move backward. The store enforces that
// with a monotonic upsert — `last_seq = GREATEST(<table>.last_seq,
// EXCLUDED.last_seq)`. The original blind `last_seq = EXCLUDED.last_seq` let a
// stale or overlapping writer rewind the watermark, proven live: the cursor
// regressed from 10 to 5 (ADR-CONCURRENCY-004).
//
// This test fails the build if either cursor writer drops the GREATEST guard —
// the exact regression that reintroduces a non-monotonic cursor. It reads each
// writer's own function body (via AST bounds) so it cannot be fooled by GREATEST
// appearing elsewhere in the file.
func TestCursorWriters_AreMonotonic(t *testing.T) {
	cases := []struct {
		file string
		fn   string
	}{
		{"../store/postgres/webhook_store.go", "SetCursor"},
		{"../store/postgres/policy_store.go", "SetPolicyCursor"},
	}
	for _, c := range cases {
		t.Run(c.fn, func(t *testing.T) {
			raw, err := os.ReadFile(c.file)
			if err != nil {
				t.Fatalf("read %s: %v", c.file, err)
			}
			src := string(raw)
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, c.file, src, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", c.file, err)
			}
			var body string
			ast.Inspect(f, func(n ast.Node) bool {
				fn, ok := n.(*ast.FuncDecl)
				if !ok || fn.Name.Name != c.fn || fn.Recv == nil {
					return true
				}
				body = src[fset.Position(fn.Pos()).Offset:fset.Position(fn.End()).Offset]
				return false
			})
			if body == "" {
				t.Fatalf("%s: method %s not found", c.file, c.fn)
			}
			if !strings.Contains(body, "GREATEST(") {
				t.Errorf("%s.%s does not use GREATEST(...) — a cursor write must be monotonic so a stale "+
					"writer cannot rewind the watermark (ADR-CONCURRENCY-004)", c.file, c.fn)
			}
			// The blind form is only safe when wrapped by GREATEST; a bare
			// assignment to EXCLUDED.last_seq (not inside GREATEST) is the defect.
			if strings.Contains(body, "last_seq=EXCLUDED.last_seq") ||
				strings.Contains(body, "last_seq = EXCLUDED.last_seq") {
				t.Errorf("%s.%s assigns last_seq directly from EXCLUDED — the watermark can regress; "+
					"wrap it in GREATEST(<table>.last_seq, EXCLUDED.last_seq)", c.file, c.fn)
			}
		})
	}
}
