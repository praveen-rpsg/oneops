// Package beta is a leaf: it imports stdlib only.
package beta

import "strings"

// Name is beta's identity.
func Name() string { return strings.ToUpper("beta") }
