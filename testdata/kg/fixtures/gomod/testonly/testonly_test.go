// Package testonly has no non-test source, so `go list` reports it with no
// GoFiles and no Imports. internal/arch is exactly this shape, and it is why
// E1 anchors a package's evidence on its directory rather than on a file.
package testonly

import "testing"

func TestNothing(t *testing.T) { t.Log("present so the package exists") }
