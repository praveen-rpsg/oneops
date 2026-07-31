package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rpsg/oneops/internal/kg/storage"
)

const repoRoot = "../.."

// generated returns the path `kg build --root repoRoot` writes to, and removes
// it afterwards. It is a generated artifact (ADR-PKG-001) — regenerable by
// `make graph` and excluded from the index — so deleting it costs nothing.
func generated(t *testing.T) string {
	t.Helper()
	p := filepath.Join(repoRoot, storage.Filename)
	t.Cleanup(func() { _ = os.Remove(p) })
	return p
}

// The end-to-end path the story exists for: repository -> E1 -> graph ->
// validation -> deterministic storage, driven by one command.
func TestBuildWritesAValidGraph(t *testing.T) {
	out := generated(t)
	var stdout, stderr bytes.Buffer
	if err := run([]string{"build", "--root", repoRoot}, &stdout, &stderr); err != nil {
		t.Fatalf("build: %v (stderr %s)", err, stderr.String())
	}

	g, err := storage.ReadFile(out)
	if err != nil {
		t.Fatalf("the file the command wrote cannot be read back: %v", err)
	}
	if verr := g.Validate(); verr != nil {
		t.Fatalf("the published graph does not satisfy §II: %v", verr)
	}
	if len(g.Nodes) == 0 || len(g.Edges) == 0 {
		t.Fatalf("published an empty graph: %d nodes, %d edges", len(g.Nodes), len(g.Edges))
	}
	if g.Commit == "" {
		t.Error("the published graph carries no commit, so its freshness cannot be checked")
	}
	summary := stdout.String()
	if !strings.Contains(summary, storage.Filename) || !strings.Contains(summary, g.Commit) {
		t.Errorf("summary does not name the file and the commit: %q", summary)
	}
	t.Logf("%s", strings.TrimSpace(summary))
}

// Byte-identical output across runs is what lets the graph be trusted without
// being stored, so it is asserted on the published file rather than in memory.
func TestPublishedFileIsByteIdenticalAcrossRuns(t *testing.T) {
	out := generated(t)
	var discard bytes.Buffer

	if err := run([]string{"build", "--root", repoRoot, "--quiet"}, &discard, &discard); err != nil {
		t.Fatalf("first build: %v", err)
	}
	first, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for i := 0; i < 3; i++ {
		if berr := run([]string{"build", "--root", repoRoot, "--quiet"}, &discard, &discard); berr != nil {
			t.Fatalf("build %d: %v", i, berr)
		}
		again, rerr := os.ReadFile(out)
		if rerr != nil {
			t.Fatalf("read %d: %v", i, rerr)
		}
		if !bytes.Equal(first, again) {
			t.Fatalf("run %d wrote different bytes", i)
		}
	}
	if !bytes.HasSuffix(first, []byte("\n")) {
		t.Error("published file has no trailing newline (§VII)")
	}
	if !bytes.Contains(first, []byte("\n  \"schema_version\"")) {
		t.Error("published file is not indented with two spaces (§VII)")
	}
}

func TestQuietSuppressesTheSummary(t *testing.T) {
	generated(t)
	var stdout, stderr bytes.Buffer
	if err := run([]string{"build", "--root", repoRoot, "--quiet"}, &stdout, &stderr); err != nil {
		t.Fatalf("build: %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("--quiet still wrote %q", stdout.String())
	}
}

func TestBuildFailsOutsideARepository(t *testing.T) {
	var stdout, stderr bytes.Buffer
	dir := t.TempDir()
	if err := run([]string{"build", "--root", dir}, &stdout, &stderr); err == nil {
		t.Fatal("building outside a repository returned no error")
	}
	if _, err := os.Stat(filepath.Join(dir, storage.Filename)); !os.IsNotExist(err) {
		t.Error("a file was published despite the build failing")
	}
}

func TestCommandLine(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{"no subcommand", nil, true},
		{"unknown subcommand", []string{"frobnicate"}, true},
		{"positional argument to build", []string{"build", "extra"}, true},
		{"unknown flag", []string{"build", "--nope"}, true},
		{"version", []string{"version"}, false},
		{"help", []string{"--help"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := run(tc.args, &stdout, &stderr)
			if tc.wantErr && err == nil {
				t.Errorf("expected an error, got none (stdout %q)", stdout.String())
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
