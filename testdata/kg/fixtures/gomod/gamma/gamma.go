// Package gamma imports both siblings, so the fixture has a package with more
// than one internal edge.
package gamma

import (
	"example.test/fixture/alpha"
	"example.test/fixture/beta"
)

// Both joins the two.
func Both() string { return alpha.Greet() + beta.Name() }
