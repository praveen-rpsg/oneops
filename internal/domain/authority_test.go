package domain

import "testing"

// AuthorityState is the computed constitutional authority of a Configuration
// Object (Configuration State Model §2) — never stored, hardcoded, or
// inferred. Valid is the guard that stops an uncomputed/garbage value from
// masquerading as one of the four states the resolver actually produces.
func TestAuthorityStateValid(t *testing.T) {
	for _, s := range []AuthorityState{
		AuthorityStateActive, AuthorityStateHistorical,
		AuthorityStateNonNormative, AuthorityStateUnknown,
	} {
		if !s.Valid() {
			t.Errorf("authority state %q should be valid", s)
		}
	}
	for _, s := range []AuthorityState{"bogus", "", "active", "ACTIVE "} {
		if AuthorityState(s).Valid() {
			t.Errorf("authority state %q should be invalid", s)
		}
	}
}
