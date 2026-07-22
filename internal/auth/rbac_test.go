package auth

import "testing"

func TestHasPermission(t *testing.T) {
	cases := []struct {
		roles []string
		perm  Permission
		want  bool
	}{
		{[]string{"oneops-reader"}, PermRead, true},
		{[]string{"oneops-reader"}, PermWrite, false},
		{[]string{"oneops-editor"}, PermWrite, true},
		{[]string{"oneops-editor"}, PermDelete, false},
		{[]string{"oneops-admin"}, PermDelete, true},
		{[]string{"oneops-admin"}, PermRead, true},
		{[]string{"unknown"}, PermRead, false},
		{nil, PermRead, false},
		{[]string{"oneops-reader", "oneops-admin"}, PermDelete, true},
	}
	for _, c := range cases {
		if got := HasPermission(c.roles, c.perm); got != c.want {
			t.Errorf("HasPermission(%v, %s) = %v, want %v", c.roles, c.perm, got, c.want)
		}
	}
}
