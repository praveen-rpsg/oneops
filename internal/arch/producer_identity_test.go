package arch_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// A producer that mints a queue row with a random id is not idempotent: a
// re-processed event (a crash before the cursor advanced, or a leadership
// overlap when two workers run) becomes a SECOND row with a NEW id — a duplicate
// the receiver cannot deduplicate, because the id it dedups on differs. This was
// proven live: a cursor reset produced two delivery rows with two ids.
//
// The fix (ADR-CONCURRENCY-003) is a content-derived identity: the row id is a
// pure function of the event's logical coordinates, so re-production collides on
// the primary key (ON CONFLICT DO NOTHING) instead of duplicating. This test
// makes the regression — a random id on a produced row — fail the build.
//
// It parses each producer and requires the row literal it enqueues to set ID
// from the deterministic constructor, never from a random generator.
func TestProducers_UseDeterministicRowIdentity(t *testing.T) {
	cases := []struct {
		file    string // producer source
		litType string // composite-literal type name whose ID must be deterministic
		wantFn  string // the required id constructor
	}{
		{"../events/relay.go", "Delivery", "DeliveryID"},
		{"../events/replay.go", "Delivery", "DeliveryID"},
		{"../policy/consumer.go", "Execution", "ExecutionID"},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, c.file, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", c.file, err)
			}
			found := 0
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				id, ok := lit.Type.(*ast.Ident)
				if !ok || id.Name != c.litType {
					return true
				}
				for _, el := range lit.Elts {
					kv, ok := el.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, ok := kv.Key.(*ast.Ident)
					if !ok || key.Name != "ID" {
						continue
					}
					found++
					call, ok := kv.Value.(*ast.CallExpr)
					if !ok {
						t.Errorf("%s: %s literal at %s sets ID from a non-call %T; it must be %s(...) so production is idempotent",
							c.file, c.litType, fset.Position(kv.Pos()), kv.Value, c.wantFn)
						continue
					}
					fn, ok := call.Fun.(*ast.Ident)
					if !ok || fn.Name != c.wantFn {
						t.Errorf("%s: %s literal at %s sets ID from %s(...), not %s(...) — a produced row must have a "+
							"content-derived id, never a random one, or a re-processed event duplicates (ADR-CONCURRENCY-003)",
							c.file, c.litType, fset.Position(kv.Pos()), callName(call.Fun), c.wantFn)
					}
				}
				return true
			})
			if found == 0 {
				t.Errorf("%s: no %s literal setting ID found — producer identity check cannot verify idempotency", c.file, c.litType)
			}
		})
	}
}

func callName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return callName(x.X) + "." + x.Sel.Name
	default:
		return "<expr>"
	}
}
