// Package kg is the root of the Platform Knowledge Graph derivation tooling.
//
// It holds no logic. The graph model, the extractors, the pipeline and the
// validators live in subpackages, created by later stories; this file exists so
// that the package is in the tree — and therefore inside every tree-sweeping
// architecture guard — before any extractor code is written.
//
// # Why the package needs a foundation story at all
//
// Nine guards under internal/arch walk every non-test .go file below internal/
// and read the SQL they find. The extractors describe the very schema those
// guards protect, so an extractor that named a table in Go source would be read
// as a real mutation of that table and fail the build — a naming collision that
// presents as a guard defect.
//
// Two rules keep the collision impossible, and guard_safety_test.go enforces
// both against this package and everything added beneath it:
//
//   - Table names are derived from the migration corpus at run time, never
//     written as literals in Go source. Fixtures live in testdata/kg as .sql
//     data files.
//   - No type declares Run(ctx context.Context) error. That signature marks a
//     background worker and is claimed by the leadership-cancellation guard.
//     Derivation entry points use Build(ctx).
//
// Both rules come from the PKG Implementation Specification §0.3.
//
// # Trust
//
// Nothing here opens a database connection, reads the network, the clock or the
// environment. Every fact the tooling emits is derived from files already in
// the working tree, which is what keeps cmd/kg outside the ownership framework
// ADR-TENANCY-008 requires of privileged tooling.
package kg
