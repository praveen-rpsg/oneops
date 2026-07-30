package domain

import (
	"context"
	"strings"
	"time"
)

// OrganizationStatus is the lifecycle of an organisation.
//
// Deletion is deliberately not modelled. ADR-IDENTITY-001 §8.3 permits no hard
// delete while governed objects exist, and those objects are anchored to the
// organisation's tenant by an append-only audit chain — removing the
// organisation would orphan records that cannot themselves be removed.
// Suspension is the disable, exactly as it is for Tenant.
type OrganizationStatus string

const (
	OrganizationActive    OrganizationStatus = "active"
	OrganizationSuspended OrganizationStatus = "suspended"
)

// Valid reports whether s is a defined status.
func (s OrganizationStatus) Valid() bool {
	return s == OrganizationActive || s == OrganizationSuspended
}

// Organization is a scope of Identity to which Policy attaches (Vol IV Part 9
// C6), realised as an isolation boundary by exactly one Tenant.
//
// The two stand 1:1, enforced by uq_org_tenant (ADR-IDENTITY-001 §4). TenantID
// here is a pointer to the boundary this organisation is realised as — it is
// not this row's owner. organization is global: it carries no ownership key of
// its own, is absent from TenantOwnedTables, and is not under row-level
// security, because tenant_id is discovered BY reading this mapping and gating
// the mapping on it would be circular (ADR-IDENTITY-002 §3.1).
type Organization struct {
	OrgID string
	// TenantID is the tenant realising this organisation's isolation boundary.
	TenantID   string
	Slug       string
	Name       string
	Status     OrganizationStatus
	RowVersion int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Active reports whether the organisation may serve requests.
func (o *Organization) Active() bool { return o.Status == OrganizationActive }

// Validate enforces the invariants the database also enforces, so a bad request
// fails with a field-level 422 rather than a constraint-violation 500.
//
// The slug rule is slugPattern, the same expression ck_org_slug and
// ck_tenant_slug both carry. A laxer rule here would admit an organisation slug
// that its own tenant could not hold, and the backfill copies tenant slugs
// verbatim.
func (o *Organization) Validate() error {
	if strings.TrimSpace(o.OrgID) == "" {
		return newValidation("org_id", "must not be empty")
	}
	if strings.TrimSpace(o.TenantID) == "" {
		return newValidation("tenant_id", "must not be empty")
	}
	if !slugPattern.MatchString(o.Slug) {
		return newValidation("slug",
			"must be 2-63 characters of lowercase letters, digits or dashes, starting with a letter or digit")
	}
	if strings.TrimSpace(o.Name) == "" {
		return newValidation("name", "must not be empty")
	}
	if !o.Status.Valid() {
		return newValidation("status", "must be one of: active, suspended")
	}
	return nil
}

// NewOrganization builds an active organisation and the identity of the tenant
// that will realise it.
//
// Both identifiers are minted here and neither is accepted from a caller: a
// client-supplied tenant id could collide with the system tenant, and a
// client-supplied org id is the global key space the Trust Register records as
// entry 1. The tenant ROW is created alongside the organisation by the
// repository, in one transaction — an organisation without its tenant is an
// Identity scope with no isolation and must not be reachable
// (ADR-IDENTITY-001 §7.1).
func NewOrganization(name, slug string) (*Organization, error) {
	o := &Organization{
		OrgID:    NewID(),
		TenantID: NewID(),
		Slug:     strings.ToLower(strings.TrimSpace(slug)),
		Name:     strings.TrimSpace(name),
		Status:   OrganizationActive,
	}
	if err := o.Validate(); err != nil {
		return nil, err
	}
	return o, nil
}

// OrganizationRepository persists organisations.
//
// No method takes a tenant. organization is global (ADR-IDENTITY-002 §3.1), and
// scoping this repository by tenant would make the mapping unreadable at the
// moment it is needed to resolve one.
type OrganizationRepository interface {
	// Create inserts the organisation AND the tenant realising it, in one
	// transaction. Both rows or neither (ADR-IDENTITY-001 §7.1).
	Create(ctx context.Context, o *Organization) (*Organization, error)
	Get(ctx context.Context, orgID string) (*Organization, error)
	// GetByTenant resolves the organisation a tenant realises. The 1:1 makes
	// this single-valued; uq_org_tenant is what guarantees it.
	GetByTenant(ctx context.Context, tenantID string) (*Organization, error)
	GetBySlug(ctx context.Context, slug string) (*Organization, error)
	List(ctx context.Context, limit int, after string) ([]*Organization, error)
	// SetStatus suspends or reactivates, guarded by rowVersion. Suspension
	// cascades to the realising tenant (ADR-IDENTITY-001 §8.3): an organisation
	// disabled while its tenant still served would be a boundary that answers
	// requests for a scope nobody may enter.
	SetStatus(ctx context.Context, orgID string, rowVersion int64, status OrganizationStatus) (*Organization, error)
}
