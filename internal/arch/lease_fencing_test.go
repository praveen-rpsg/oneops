package arch_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// A claimed row's outcome must be written under a fence on the claim: MarkResult
// updates only if the row is still claimed under the token the worker was handed
// (claimed_at), and signals ErrStaleClaim when it is not. Without the fence, a
// worker whose lease expired and whose row was reclaimed can resurrect a
// delivered row or overwrite the reclaimer's outcome — proven live before the fix
// (ADR-CONCURRENCY-005).
//
// This fails the build if either MarkResult drops the claimed_at guard or the
// stale-claim signal — the exact regression that reopens the unfenced write.
func TestMarkResult_IsFencedOnTheClaim(t *testing.T) {
	cases := []struct {
		file    string
		fn      string
		errName string
	}{
		{"../store/postgres/webhook_store.go", "MarkResult", "events.ErrStaleClaim"},
		{"../store/postgres/policy_store.go", "MarkResult", "policy.ErrStaleClaim"},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			raw, err := os.ReadFile(c.file)
			if err != nil {
				t.Fatalf("read %s: %v", c.file, err)
			}
			src := string(raw)
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, c.file, src, 0)
			if err != nil {
				t.Fatalf("parse: %v", err)
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
			if !strings.Contains(body, "claimed_at = $") {
				t.Errorf("%s.%s does not fence the UPDATE on claimed_at — an evicted worker can overwrite "+
					"the reclaimer's state (ADR-CONCURRENCY-005)", c.file, c.fn)
			}
			if !strings.Contains(body, "RowsAffected()") || !strings.Contains(body, c.errName) {
				t.Errorf("%s.%s does not return %s when the fence rejects the write — the evicted worker "+
					"cannot learn it was fenced", c.file, c.fn, c.errName)
			}
		})
	}
}
