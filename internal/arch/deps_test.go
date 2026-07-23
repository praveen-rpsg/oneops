package arch_test

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const module = "github.com/rpsg/oneops/"

type rule struct {
	Name         string
	From         string
	Forbidden    []string
	IncludeTests bool
	Allow        []string
}

var rules = []rule{
	{Name: "domain-purity", From: module + "internal/domain", Forbidden: []string{module}, IncludeTests: true},
	{Name: "transport-not-persistence", From: module + "internal/httpapi", Forbidden: []string{module + "internal/store/"}, IncludeTests: false},
	{Name: "sdk-isolation", From: module + "sdk", Forbidden: []string{module + "internal/"}, IncludeTests: true},
	{Name: "version-is-a-leaf", From: module + "pkg/version", Forbidden: []string{module}, IncludeTests: true},
}

type pkg struct {
	ImportPath   string
	Imports      []string
	TestImports  []string
	XTestImports []string
}

func TestDependencyRules(t *testing.T) {
	for _, tags := range [][]string{nil, {"integration"}} {
		pkgs := listPackages(t, tags)
		for _, r := range rules {
			for _, v := range violations(pkgs, r) {
				t.Errorf("dependency rule %q violated (tags=%v): %s", r.Name, tags, v)
			}
		}
	}
}

func violations(pkgs []pkg, r rule) []string {
	var out []string
	for _, p := range pkgs {
		if !under(p.ImportPath, r.From) {
			continue
		}
		imports := p.Imports
		if r.IncludeTests {
			imports = concat(p.Imports, p.TestImports, p.XTestImports)
		}
		for _, imp := range imports {
			if imp == p.ImportPath || contains(r.Allow, imp) {
				continue
			}
			for _, f := range r.Forbidden {
				if strings.HasPrefix(imp, f) {
					out = append(out, fmt.Sprintf("%s imports %s", p.ImportPath, imp))
					break
				}
			}
		}
	}
	return out
}

func under(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func concat(xss ...[]string) []string {
	var out []string
	for _, xs := range xss {
		out = append(out, xs...)
	}
	return out
}

func listPackages(t *testing.T, tags []string) []pkg {
	t.Helper()
	args := []string{"list", "-json"}
	if len(tags) > 0 {
		args = append(args, "-tags", strings.Join(tags, ","))
	}
	args = append(args, "./...")
	cmd := exec.Command("go", args...)
	cmd.Dir = moduleRoot(t)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list (tags=%v): %v\n%s", tags, err, stderr.String())
	}
	var pkgs []pkg
	dec := json.NewDecoder(strings.NewReader(string(out)))
	for dec.More() {
		var p pkg
		if err := dec.Decode(&p); err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		pkgs = append(pkgs, p)
	}
	if len(pkgs) == 0 {
		t.Fatal("go list returned no packages")
	}
	return pkgs
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	gomod := strings.TrimSpace(string(out))
	if gomod == "" || gomod == "/dev/null" {
		t.Fatal("not inside a Go module")
	}
	return filepath.Dir(gomod)
}
