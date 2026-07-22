package auth

// Permission is a coarse capability checked at the API boundary. Fine-grained
// authorization (OPA) is layered in later milestones; the permission set is
// designed to map cleanly onto OPA input.
type Permission string

const (
	PermRead   Permission = "artifacts:read"
	PermWrite  Permission = "artifacts:write"
	PermDelete Permission = "artifacts:delete"
	PermAdmin  Permission = "artifacts:admin"
)

// rolePermissions maps an OneOps role to the permissions it grants.
var rolePermissions = map[string][]Permission{
	"oneops-reader": {PermRead},
	"oneops-editor": {PermRead, PermWrite},
	"oneops-admin":  {PermRead, PermWrite, PermDelete, PermAdmin},
}

// HasPermission reports whether any of the subject's roles grants perm.
func HasPermission(roles []string, perm Permission) bool {
	for _, role := range roles {
		for _, p := range rolePermissions[role] {
			if p == perm || p == PermAdmin {
				return true
			}
		}
	}
	return false
}
