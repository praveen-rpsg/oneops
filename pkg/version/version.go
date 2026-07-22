// Package version holds build-time version metadata, injected via -ldflags at
// build time (see Makefile / Dockerfile).
package version

var (
	// Version is the semantic version of the build.
	Version = "0.1.0-dev"
	// Commit is the short git SHA of the build.
	Commit = "unknown"
)
