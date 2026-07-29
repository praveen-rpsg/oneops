package arch_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
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

// The SSRF class is only eliminated if *no* outbound request escapes the guard.
//
// ADR-SECURITY-001 guarded "both outbound clients" — accurate then, but the JWKS
// fetch used a bare `http.Get`, so the class had a third instance the register
// claimed was closed. This sweeps the whole tree rather than naming call sites,
// so a new unguarded client anywhere fails the build (ADR-SECURITY-003).
//
// internal/safehttp is the one place allowed to build a client: it is the guard.
func TestNoUnguardedOutboundHTTP(t *testing.T) {
	banned := []string{"http.Get(", "http.Post(", "http.PostForm(", "http.Head(", "http.DefaultClient"}

	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// The guard itself, and generated/vendored trees.
			if d.Name() == "safehttp" || d.Name() == "node_modules" || d.Name() == "testdata" {
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
		src := stripLineComments(string(raw))
		for _, b := range banned {
			if strings.Contains(src, b) {
				t.Errorf("%s uses %s — every outbound request must go through safehttp, or the "+
					"platform can be made to dial loopback, link-local metadata and private "+
					"ranges on someone else's behalf (ADR-SECURITY-001/003)", path, b)
			}
		}
		// A hand-rolled client is equally unguarded.
		if strings.Contains(src, "&http.Client{") {
			t.Errorf("%s constructs its own http.Client — build it with safehttp.Client so its "+
				"dialer refuses non-public addresses (ADR-SECURITY-001/003)", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// stripLineComments removes // comments so a mention of a banned construct in
// prose does not fail the build, and so commenting one out does not hide it.
func stripLineComments(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
