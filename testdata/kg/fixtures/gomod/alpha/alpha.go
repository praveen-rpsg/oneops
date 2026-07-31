// Package alpha imports one stdlib package and one sibling.
package alpha

import (
	"fmt"

	"example.test/fixture/beta"
)

// Greet returns a greeting built from beta.
func Greet() string { return fmt.Sprintf("alpha/%s", beta.Name()) }
