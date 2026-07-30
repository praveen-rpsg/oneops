//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

func newStoredOrg(ctx context.Context, t *testing.T, s *OrganizationStore, slug string) *domain.Organization {
	t.Helper()
	o, err := domain.NewOrganization("Org "+slug, slug)
	if err != nil {
		t.Fatalf("build organisation: %v", err)
	}
	created, err := s.Create(ctx, o)
	if err != nil {
		t.Fatalf("create organisation: %v", err)
	}
	return created
}

// Create must produce the organisation AND its tenant. An organisation without
// its tenant is an Identity scope with no isolation (ADR-IDENTITY-001 §7.1).
func TestOrganizationStore_CreateMakesBothRows(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewOrganizationStore(pool)

	o := newStoredOrg(ctx, t, s, "both-rows")
	if o.RowVersion != 1 {
		t.Errorf("row version = %d, want 1", o.RowVersion)
	}
	if o.Status != domain.OrganizationActive {
		t.Errorf("status = %q, want active", o.Status)
	}

	var tenantStatus string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM tenant WHERE tenant_id = $1`, o.TenantID).Scan(&tenantStatus); err != nil {
		t.Fatalf("the realising tenant was not created: %v", err)
	}
	if tenantStatus != string(domain.TenantActive) {
		t.Errorf("tenant status = %q, want active", tenantStatus)
	}
}

// Both rows or neither. A failure on the second insert must leave no tenant
// behind — otherwise the 1:1 is violated by an orphan boundary nothing points
// at (ADR-IDENTITY-001 AC-2).
func TestOrganizationStore_CreateIsAtomic(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewOrganizationStore(pool)

	first := newStoredOrg(ctx, t, s, "atomic")

	// A second organisation reusing the first's org_id: the tenant insert
	// succeeds, the organisation insert violates the primary key.
	clash, err := domain.NewOrganization("Clash", "atomic-clash")
	if err != nil {
		t.Fatal(err)
	}
	clash.OrgID = first.OrgID
	orphanTenant := clash.TenantID

	if _, err := s.Create(ctx, clash); err == nil {
		t.Fatal("a duplicate org_id was accepted")
	}

	var orphans int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM tenant WHERE tenant_id = $1`, orphanTenant).Scan(&orphans); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if orphans != 0 {
		t.Errorf("the tenant insert was left behind after the organisation insert failed "+
			"(%d rows) — an isolation boundary nothing points at", orphans)
	}
}

func TestOrganizationStore_DuplicateSlugIsConflict(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewOrganizationStore(pool)

	newStoredOrg(ctx, t, s, "taken-slug")

	same, err := domain.NewOrganization("Same Slug", "taken-slug")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, same); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("got %v, want ErrConflict", err)
	}
}

// uq_org_tenant is what makes the 1:1 real. A second organisation on one tenant
// must be refused by the database, not merely discouraged by convention.
func TestOrganizationStore_OneOrganisationPerTenant(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewOrganizationStore(pool)

	first := newStoredOrg(ctx, t, s, "one-per-tenant")

	second, err := domain.NewOrganization("Second", "one-per-tenant-2")
	if err != nil {
		t.Fatal(err)
	}
	second.TenantID = first.TenantID // point at a tenant that already has one

	if _, err := s.Create(ctx, second); err == nil {
		t.Error("a second organisation was accepted on a tenant that already had one — " +
			"uq_org_tenant is not enforcing the 1:1 of ADR-IDENTITY-001 §4")
	}
}

func TestOrganizationStore_Lookups(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewOrganizationStore(pool)

	o := newStoredOrg(ctx, t, s, "lookups")

	byID, err := s.Get(ctx, o.OrgID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if byID.OrgID != o.OrgID {
		t.Errorf("Get returned %q, want %q", byID.OrgID, o.OrgID)
	}

	byTenant, err := s.GetByTenant(ctx, o.TenantID)
	if err != nil {
		t.Fatalf("GetByTenant: %v", err)
	}
	if byTenant.OrgID != o.OrgID {
		t.Errorf("GetByTenant returned %q, want %q", byTenant.OrgID, o.OrgID)
	}

	bySlug, err := s.GetBySlug(ctx, o.Slug)
	if err != nil {
		t.Fatalf("GetBySlug: %v", err)
	}
	if bySlug.OrgID != o.OrgID {
		t.Errorf("GetBySlug returned %q, want %q", bySlug.OrgID, o.OrgID)
	}

	for name, call := range map[string]func() error{
		"unknown id":     func() error { _, e := s.Get(ctx, "org_missing"); return e },
		"unknown tenant": func() error { _, e := s.GetByTenant(ctx, "tn_missing"); return e },
		"unknown slug":   func() error { _, e := s.GetBySlug(ctx, "no-such-slug"); return e },
		"empty tenant":   func() error { _, e := s.GetByTenant(ctx, ""); return e },
		"empty slug":     func() error { _, e := s.GetBySlug(ctx, ""); return e },
	} {
		if err := call(); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s: got %v, want ErrNotFound", name, err)
		}
	}
}

// Suspension cascades to the realising tenant (ADR-IDENTITY-001 §8.3). The
// authentication boundary reads TENANT status, so an organisation suspended
// without its tenant would keep serving — a suspension that suspends nothing.
func TestOrganizationStore_SuspensionCascadesToTenant(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewOrganizationStore(pool)

	o := newStoredOrg(ctx, t, s, "cascade")

	suspended, err := s.SetStatus(ctx, o.OrgID, o.RowVersion, domain.OrganizationSuspended)
	if err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if suspended.Status != domain.OrganizationSuspended {
		t.Fatalf("organisation status = %q, want suspended", suspended.Status)
	}
	if suspended.RowVersion != o.RowVersion+1 {
		t.Errorf("row version = %d, want %d", suspended.RowVersion, o.RowVersion+1)
	}

	var tenantStatus string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM tenant WHERE tenant_id = $1`, o.TenantID).Scan(&tenantStatus); err != nil {
		t.Fatalf("probe tenant: %v", err)
	}
	if tenantStatus != string(domain.TenantSuspended) {
		t.Errorf("tenant status = %q, want suspended — the authentication boundary reads "+
			"tenant status, so an uncascaded suspension suspends nothing", tenantStatus)
	}

	// And back again.
	revived, err := s.SetStatus(ctx, suspended.OrgID, suspended.RowVersion, domain.OrganizationActive)
	if err != nil {
		t.Fatalf("reactivate: %v", err)
	}
	if revived.Status != domain.OrganizationActive {
		t.Fatalf("organisation status = %q, want active", revived.Status)
	}
	if err := pool.QueryRow(ctx,
		`SELECT status FROM tenant WHERE tenant_id = $1`, o.TenantID).Scan(&tenantStatus); err != nil {
		t.Fatalf("probe tenant: %v", err)
	}
	if tenantStatus != string(domain.TenantActive) {
		t.Errorf("tenant status = %q, want active after reactivation", tenantStatus)
	}
}

func TestOrganizationStore_SetStatusGuards(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewOrganizationStore(pool)

	o := newStoredOrg(ctx, t, s, "guards")

	if _, err := s.SetStatus(ctx, o.OrgID, o.RowVersion, "dissolved"); err == nil {
		t.Error("an undefined status was accepted")
	} else if _, ok := domain.AsValidation(err); !ok {
		t.Errorf("undefined status: got %T, want a ValidationError", err)
	}

	if _, err := s.SetStatus(ctx, o.OrgID, o.RowVersion+99, domain.OrganizationSuspended); !errors.Is(err, domain.ErrVersionMismatch) {
		t.Errorf("stale row version: got %v, want ErrVersionMismatch", err)
	}

	if _, err := s.SetStatus(ctx, "org_missing", 1, domain.OrganizationSuspended); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown organisation: got %v, want ErrNotFound", err)
	}

	// A rejected status change must leave the tenant untouched: a half-applied
	// cascade is worse than none, because it is invisible.
	var tenantStatus string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM tenant WHERE tenant_id = $1`, o.TenantID).Scan(&tenantStatus); err != nil {
		t.Fatalf("probe tenant: %v", err)
	}
	if tenantStatus != string(domain.TenantActive) {
		t.Errorf("tenant status = %q after rejected changes, want active — the cascade ran "+
			"despite the organisation update being refused", tenantStatus)
	}
}

func TestOrganizationStore_ListPaginates(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewOrganizationStore(pool)

	for i := 0; i < 4; i++ {
		newStoredOrg(ctx, t, s, fmt.Sprintf("page-%d", i))
	}

	first, err := s.List(ctx, 2, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("first page holds %d, want 2", len(first))
	}
	if first[0].OrgID >= first[1].OrgID {
		t.Error("page is not ordered by org_id")
	}

	second, err := s.List(ctx, 2, first[len(first)-1].OrgID)
	if err != nil {
		t.Fatalf("list page 2: %v", err)
	}
	for _, o := range second {
		if o.OrgID <= first[len(first)-1].OrgID {
			t.Errorf("keyset cursor leaked a row from the previous page: %q", o.OrgID)
		}
	}

	// Both caps must hold, and each is only observable above its own threshold.
	if _, err := pool.Exec(ctx, `
		INSERT INTO tenant (tenant_id, slug, name)
		SELECT 'tn-obulk-' || lpad(i::text, 6, '0'), 'obulk-' || i, 'Bulk ' || i
		  FROM generate_series(1, $1) AS i
		ON CONFLICT DO NOTHING`, maxOrgPageSize+5); err != nil {
		t.Fatalf("bulk seed tenants: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO organization (org_id, tenant_id, slug, name)
		SELECT 'org_bulk_' || lpad(i::text, 6, '0'), 'tn-obulk-' || lpad(i::text, 6, '0'),
		       'obulk-' || i, 'Bulk ' || i
		  FROM generate_series(1, $1) AS i
		ON CONFLICT DO NOTHING`, maxOrgPageSize+5); err != nil {
		t.Fatalf("bulk seed organisations: %v", err)
	}

	// Removed once the caps have been observed: see the note in the user store's
	// equivalent test. Organisations drag their tenants with them, so both go.
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM organization WHERE org_id LIKE 'org_bulk_%'`)
		_, _ = pool.Exec(bg, `DELETE FROM tenant WHERE tenant_id LIKE 'tn-obulk-%'`)
	})

	all, err := s.List(ctx, 0, "")
	if err != nil {
		t.Fatalf("list with no limit: %v", err)
	}
	if len(all) != defaultOrgPageSize {
		t.Errorf("an unbounded request returned %d rows, want exactly %d", len(all), defaultOrgPageSize)
	}

	over, err := s.List(ctx, maxOrgPageSize*10, "")
	if err != nil {
		t.Fatalf("list over max: %v", err)
	}
	if len(over) != maxOrgPageSize {
		t.Errorf("a request for %d returned %d, want %d — an unclamped limit lets one "+
			"caller pull the whole table", maxOrgPageSize*10, len(over), maxOrgPageSize)
	}
}

// organization is global (ADR-IDENTITY-002 §3.1): it must stay readable under
// any tenant context, because tenant_id is discovered BY reading it.
func TestOrganizationStore_IsNotTenantScoped(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewOrganizationStore(pool)

	o := newStoredOrg(ctx, t, s, "global-read")

	other := domain.WithTenant(ctx, &domain.Tenant{TenantID: "tn-unrelated"})
	if _, err := s.Get(other, o.OrgID); err != nil {
		t.Fatalf("an organisation was not visible under a different tenant context: %v — "+
			"tenant_id is resolved BY reading this mapping, so gating it would be circular", err)
	}

	for _, table := range TenantOwnedTables {
		if table == "organization" {
			t.Error("organization is registered in TenantOwnedTables; it is global")
		}
	}
}
