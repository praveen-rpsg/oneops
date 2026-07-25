package arch_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// The platform makes server-side HTTP requests to tenant-supplied URLs (webhook
// delivery, policy HTTP actions). A default http.Client dials any host — loopback,
// link-local (169.254.169.254 cloud metadata), private ranges — which is SSRF,
// proven live (ADR-SECURITY-001). Those clients must be built by safehttp.Client,
// whose dialer refuses non-public addresses.
//
// This fails the build if the composition root passes a bare &http.Client{} to
// the dispatcher or the policy registry, or stops using safehttp.Client — the
// exact regression that reopens the SSRF class.
func TestOutboundClients_AreSSRFGuarded(t *testing.T) {
	const mainFile = "../../cmd/controlplane/main.go"
	raw, err := os.ReadFile(mainFile)
	if err != nil {
		t.Fatalf("read main: %v", err)
	}
	src := string(raw)
	if !strings.Contains(src, "safehttp.Client(") {
		t.Fatal("main.go does not construct any safehttp.Client — outbound delivery must be SSRF-guarded (ADR-SECURITY-001)")
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, mainFile, src, 0)
	if err != nil {
		t.Fatalf("parse main: %v", err)
	}
	// The constructors that receive the outbound HTTP client.
	guarded := map[string]bool{"NewDispatcher": true, "DefaultRegistry": true}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !guarded[sel.Sel.Name] {
			return true
		}
		for _, arg := range call.Args {
			if isBareHTTPClient(arg) {
				t.Errorf("%s is passed a bare &http.Client{} at %s — it must receive a safehttp.Client "+
					"so outbound delivery cannot reach internal addresses (ADR-SECURITY-001)",
					sel.Sel.Name, fset.Position(arg.Pos()))
			}
		}
		return true
	})
}

// isBareHTTPClient matches &http.Client{...}.
func isBareHTTPClient(e ast.Expr) bool {
	u, ok := e.(*ast.UnaryExpr)
	if !ok {
		return false
	}
	lit, ok := u.X.(*ast.CompositeLit)
	if !ok {
		return false
	}
	sel, ok := lit.Type.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "http" && sel.Sel.Name == "Client"
}
