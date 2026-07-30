package postgres

import "testing"

// The page bound is what keeps a list operation from becoming unbounded as an
// organisation grows. Asserted directly: proving it through the store would
// require seeding more rows than the cap.
func TestClampMembershipPage(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"unspecified takes the default", 0, defaultMembershipPageSize},
		{"negative takes the default", -1, defaultMembershipPageSize},
		{"a reasonable request is honoured", 10, 10},
		{"the maximum is honoured exactly", maxMembershipPageSize, maxMembershipPageSize},
		{"one beyond the maximum is capped", maxMembershipPageSize + 1, maxMembershipPageSize},
		{"an absurd request is capped", 100000, maxMembershipPageSize},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampMembershipPage(tc.in); got != tc.want {
				t.Errorf("clampMembershipPage(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
	if maxMembershipPageSize <= defaultMembershipPageSize {
		t.Error("the cap must exceed the default, or the default is the cap")
	}
}
