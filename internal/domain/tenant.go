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
	// AllowedIssuers are the identity-provider issuers (JWT `iss`) permitted to
	// authenticate this tenant. An EMPTY set means the tenant may authenticate
	// ONLY via the default IdP (ONEOPS_JWT_ISSUER) — safe by default and
	// backward compatible, since every tenant that predates multi-IdP support
	// has an empty set and keeps working with the default issuer. A tenant that
	// brings its own additional IdP must list that IdP's issuer here. Enforced
	// fail-closed at the authentication boundary by AllowsIssuer
	// (ADR-IDENTITY-003).
	AllowedIssuers []string
	Status         TenantStatus
	RowVersion     int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Active reports whether the tenant may serve requests.
func (t *Tenant) Active() bool { return t.Status == TenantActive }

// AllowsIssuer reports whether a token issued by issuer may authenticate this
// tenant, given the platform's default issuer.
//
// The empty-set rule is the whole point: a tenant with no explicit binding may
// authenticate only via the default IdP. This is what keeps single-IdP
// deployments and every pre-existing tenant working without configuration while
// closing the multi-IdP cross-tenant bypass — an additional IdP can authenticate
// a tenant only once that tenant has explicitly listed it. It fails closed: an
// empty issuer, or a default issuer that is itself empty, matches nothing.
func (t *Tenant) AllowsIssuer(issuer, defaultIssuer string) bool {
	if issuer == "" {
		return false
	}
	if len(t.AllowedIssuers) == 0 {
		return defaultIssuer != "" && issuer == defaultIssuer
	}
	for _, a := range t.AllowedIssuers {
		if a == issuer {
			return true
		}
	}
	return false
}

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
	if err := ValidateAllowedIssuers(t.AllowedIssuers); err != nil {
		return err
	}
	return nil
}

// ValidateAllowedIssuers rejects a binding that could not enforce anything: an
// empty or blank issuer string would either match no token (dead entry) or, if
// treated loosely, widen the set. Keeping the set clean is what lets AllowsIssuer
// stay a simple membership test.
func ValidateAllowedIssuers(issuers []string) error {
	for _, iss := range issuers {
		if strings.TrimSpace(iss) == "" {
			return newValidation("allowed_issuers", "must not contain an empty issuer")
		}
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
	// SetAllowedIssuers replaces a tenant's issuer binding, guarded by
	// rowVersion. An empty slice restores the safe default (default IdP only).
	SetAllowedIssuers(ctx context.Context, tenantID string, rowVersion int64, issuers []string) (*Tenant, error)
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
