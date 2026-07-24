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

// PermAdmin was a wildcard: HasPermission returned true for any requested
// permission whenever a role held it. That made every permission defined
// afterwards retroactively granted to tenant administrators.
func TestTenantAdminIsNotPlatformAdmin(t *testing.T) {
	tenantAdmin := []string{"oneops-admin"}

	if !HasPermission(tenantAdmin, PermAdmin) {
		t.Error("a tenant administrator must retain tenant administration")
	}
	if HasPermission(tenantAdmin, PermPlatformAdmin) {
		t.Fatal("a tenant administrator must not hold platform administration: " +
			"it could enumerate every customer and suspend a competitor")
	}

	platformAdmin := []string{"oneops-platform-admin"}
	if !HasPermission(platformAdmin, PermPlatformAdmin) {
		t.Error("the platform administrator must hold platform administration")
	}
}

// No permission may be implied by another. A future capability must be granted
// where somebody writes it down, never by inheriting an existing role's
// wildcard.
func TestNoPermissionIsImpliedByAnother(t *testing.T) {
	future := Permission("some:future-capability")
	for role := range rolePermissions {
		if HasPermission([]string{role}, future) {
			t.Errorf("role %q grants an undeclared permission %q; "+
				"permissions must be granted explicitly, never implied", role, future)
		}
	}
}

// An unknown or absent role grants nothing, so a malformed claim cannot escalate.
func TestUnknownRolesGrantNothing(t *testing.T) {
	for _, roles := range [][]string{
		nil, {}, {""}, {"admin"}, {"oneops-ADMIN"}, {"oneops-platform-admin "},
	} {
		for _, p := range []Permission{PermRead, PermWrite, PermDelete, PermAdmin, PermPlatformAdmin} {
			if HasPermission(roles, p) {
				t.Errorf("roles %q granted %q", roles, p)
			}
		}
	}
}
