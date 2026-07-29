package domain

import (
	"context"
	"regexp"
	"strings"
	"time"
)

// SystemTenantID owns every row written before tenancy existed, and is the
// tenant a request resolves to when its token asserts none. Keeping
// single-tenant deployments working without configuration is what makes the
// tenancy rollout non-breaking.
const SystemTenantID = "system"

// TenantStatus is the lifecycle of a tenant. A suspended tenant is rejected at
// the authentication boundary: its data is retained, but no request carrying it
// is served. Deletion is deliberately not modelled — removing a tenant would
// orphan audit history, which is append-only by constitutional guarantee.
type TenantStatus string

const (
	TenantActive    TenantStatus = "active"
	TenantSuspended TenantStatus = "suspended"
)

// Valid reports whether s is a defined status.
func (s TenantStatus) Valid() bool {
	return s == TenantActive || s == TenantSuspended
}

// slugPattern mirrors ck_tenant_slug. Slugs are lowercase, dash-separated, and
// bounded so they remain usable in URLs, log lines and metric labels.
var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

// Tenant is an isolation boundary. Every governed row belongs to exactly one.
type Tenant struct {
	TenantID string
	Slug     string
	Name     string
	// ExternalID is the identifier the identity provider asserts for this
	// tenant (Entra `tid`, Okta org id). Empty until an IdP is bound. Bearer
	// tokens are resolved against this, never against TenantID, so the platform
	// keeps its own identifiers when the IdP changes.
	ExternalID string
	Status     TenantStatus
	RowVersion int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Active reports whether the tenant may serve requests.
func (t *Tenant) Active() bool { return t.Status == TenantActive }

// Validate enforces the invariants the database also enforces, so a bad request
// fails with a field-level 422 rather than a constraint-violation 500.
func (t *Tenant) Validate() error {
	if strings.TrimSpace(t.TenantID) == "" {
		return newValidation("tenant_id", "must not be empty")
	}
	if !slugPattern.MatchString(t.Slug) {
		return newValidation("slug",
			"must be 2-63 characters of lowercase letters, digits or dashes, starting with a letter or digit")
	}
	if strings.TrimSpace(t.Name) == "" {
		return newValidation("name", "must not be empty")
	}
	if !t.Status.Valid() {
		return newValidation("status", "must be one of: active, suspended")
	}
	return nil
}

// TenantRepository persists tenants.
type TenantRepository interface {
	Create(ctx context.Context, t *Tenant) (*Tenant, error)
	Get(ctx context.Context, tenantID string) (*Tenant, error)
	// GetByExternalID resolves the identifier asserted in a bearer token. It
	// returns ErrNotFound for an unknown external id, which the authentication
	// boundary turns into a rejected request rather than a new silo.
	GetByExternalID(ctx context.Context, externalID string) (*Tenant, error)
	List(ctx context.Context) ([]*Tenant, error)
	// SetStatus suspends or reactivates a tenant, guarded by rowVersion.
	SetStatus(ctx context.Context, tenantID string, rowVersion int64, status TenantStatus) (*Tenant, error)
}

// tenantContextKey scopes the resolved tenant in a request context.
type tenantContextKey struct{}

// WithTenant returns ctx carrying the resolved tenant. Set once, at the
// authentication boundary; everything downstream reads it rather than
// re-deriving it from claims, so there is exactly one place where a token
// becomes a tenant.
func WithTenant(ctx context.Context, t *Tenant) context.Context {
	return context.WithValue(ctx, tenantContextKey{}, t)
}

// TenantFrom returns the tenant resolved for this request.
func TenantFrom(ctx context.Context) (*Tenant, bool) {
	t, ok := ctx.Value(tenantContextKey{}).(*Tenant)
	return t, ok
}

// TenantIDFrom returns the resolved tenant's id, or the system tenant when none
// was resolved. Persistence uses this so a code path that has not yet been
// threaded with a tenant writes to the system tenant rather than to an empty
// string, which would violate the foreign key and surface as a 500.
func TenantIDFrom(ctx context.Context) string {
	if t, ok := TenantFrom(ctx); ok && t.TenantID != "" {
		return t.TenantID
	}
	return SystemTenantID
}
