// Command kg derives the Platform Knowledge Graph from the working tree.
//
// It opens no database connection and holds no credentials: every fact it emits
// is derived from files already in the repository. That is what keeps it outside
// the ownership framework ADR-TENANCY-008 requires of privileged tooling — there
// is nothing for it to own.
//
// `kg build` writes pkg.json to the repository root. That file is generated and
// never committed (ADR-PKG-001): CI regenerates it on every run, and a stale one
// is replaced rather than reconciled.
//
// This is the build skeleton (backlog S1.4). §VIII specifies seven further
// subcommands; each arrives with its own story.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/rpsg/oneops/internal/kg/pipeline"
	"github.com/rpsg/oneops/internal/kg/storage"
	"github.com/rpsg/oneops/pkg/version"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "kg: %v\n", err)
		os.Exit(1)
	}
}

func usage(w io.Writer) {
	fmt.Fprintf(w, `kg %s — derive the Platform Knowledge Graph

usage:
  kg build [--root DIR] [--quiet]   derive the graph and write %s
  kg version                        print the build version

`, version.Version, storage.Filename)
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		usage(stderr)
		return fmt.Errorf("no subcommand given")
	}

	switch args[0] {
	case "build":
		return build(args[1:], stdout, stderr)
	case "version":
		fmt.Fprintf(stdout, "kg %s (commit %s, built %s)\n", version.Version, version.Commit, version.Date)
		return nil
	case "-h", "--help", "help":
		usage(stdout)
		return nil
	default:
		usage(stderr)
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

// build derives the graph and publishes it (§IV's Serialize and Publish stages).
//
// The pipeline stops before serialisation because §I places storage below it;
// composing the two is this command's job, and it is the only place that knows
// both.
func build(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", ".", "repository root to derive from")
	quiet := fs.Bool("quiet", false, "suppress the summary line")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("build takes no positional arguments, got %q", fs.Arg(0))
	}

	g, err := pipeline.Build(context.Background(), *root)
	if err != nil {
		return err
	}

	out := filepath.Join(*root, storage.Filename)
	if werr := storage.WriteFile(out, g); werr != nil {
		return werr
	}
	if !*quiet {
		fmt.Fprintf(stdout, "%s: %d nodes, %d edges, commit %s\n",
			out, len(g.Nodes), len(g.Edges), g.Commit)
	}
	return nil
}
