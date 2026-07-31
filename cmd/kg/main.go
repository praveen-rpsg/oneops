// Command kg derives the Platform Knowledge Graph from the working tree.
//
// It opens no database connection and holds no credentials: every fact it emits
// is derived from files already in the repository. That is what keeps it outside
// the ownership framework ADR-TENANCY-008 requires of privileged tooling — there
// is nothing for it to own.
//
// This is the registration stub (backlog S0.1). The extractors, the pipeline and
// the subcommands arrive in S1.4 onwards; until then the binary exists so that
// `cmd/kg` is registered before any code under `internal/kg` is written.
package main

import (
	"fmt"
	"os"

	"github.com/rpsg/oneops/pkg/version"
)

func main() {
	fmt.Fprintf(os.Stdout, "kg %s (commit %s, built %s)\n", version.Version, version.Commit, version.Date)
}
