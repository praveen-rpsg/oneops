package auth

// Permission is a coarse capability checked at the API boundary. Fine-grained
// authorization (OPA) is layered in later milestones; the permission set is
// designed to map cleanly onto OPA input.
//
// Permissions carry a scope, and the scope is the security-relevant part.
// Tenant-scoped permissions authorise action on data inside the caller's own
// isolation boundary, where row-level security confines the result regardless.
// Platform-scoped permissions authorise action on the boundaries themselves,
// where nothing confines the result — and so they are granted separately and
// checked separately.
type Permission string

const (
	// Tenant-scoped: everything these reach is confined by row-level security
	// to the caller's tenant.
	PermRead   Permission = "artifacts:read"
	PermWrite  Permission = "artifacts:write"
	PermDelete Permission = "artifacts:delete"
	// PermAdmin administers subsystems *within* a tenant — its webhooks, its
	// policies, its evidence. It is the highest tenant-scoped permission and is
	// deliberately not the highest permission.
	PermAdmin Permission = "artifacts:admin"

	// PermPlatformAdmin administers the tenants themselves: registering them,
	// suspending them, enumerating them. The tenant registry is exempt from
	// row-level security by necessity (ADR-TENANCY-001 §4 — resolving a token
	// to a tenant must precede binding one), so this permission is the only
	// thing standing between a caller and every customer's record.
	PermPlatformAdmin Permission = "platform:admin"
)

// rolePermissions maps an OneOps role to the permissions it grants.
//
// Every grant is written out in full. There is deliberately no wildcard and no
// implication between permissions: see HasPermission.
var rolePermissions = map[string][]Permission{
	"oneops-reader": {PermRead},
	"oneops-editor": {PermRead, PermWrite},
	// A tenant administrator. Complete authority inside one tenant, none over
	// the set of tenants.
	"oneops-admin": {PermRead, PermWrite, PermDelete, PermAdmin},
	// The operator of the installation. Held by nobody's customer.
	"oneops-platform-admin": {
		PermRead, PermWrite, PermDelete, PermAdmin, PermPlatformAdmin,
	},
}

// HasPermission reports whether any of the subject's roles grants perm.
//
// The match is exact. An earlier version returned true when a role held
// PermAdmin regardless of which permission was requested, which made PermAdmin
// a wildcard over every permission that existed or would ever be defined. Its
// consequences were not theoretical: an ordinary tenant administrator could
// enumerate every customer, register tenants binding identifiers of its
// choosing, and suspend a different customer outright — locking that customer
// out of the whole platform. Verified against the running service.
//
// The deeper defect was that the wildcard made every *future* permission
// retroactively granted to tenant administrators. A new capability would have
// been authorised for them the moment it was defined, silently and without
// anyone choosing it. Exact matching means a grant only exists where somebody
// wrote it down.
func HasPermission(roles []string, perm Permission) bool {
	for _, role := range roles {
		for _, p := range rolePermissions[role] {
			if p == perm {
				return true
			}
		}
	}
	return false
}
