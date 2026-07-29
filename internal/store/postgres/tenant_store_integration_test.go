//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/oklog/ulid/v2"

	"github.com/rpsg/oneops/internal/domain"
)

func newTenant(slug, external string) *domain.Tenant {
	return &domain.Tenant{
		TenantID: ulid.Make().String(), Slug: slug, Name: slug,
		ExternalID: external, Status: domain.TenantActive,
	}
}

// tenantStore returns a store over a pool whose tenant table is empty except
// for the system row seeded by the migration. truncateAll does not clear
// tenant — the system row is a fixture the schema depends on — so tests use
// unique slugs rather than assuming an empty table.
func tenantStore(t *testing.T) *TenantStore {
	t.Helper()
	return NewTenantStore(testPool(t))
}

func TestTenantStore_CreateGet(t *testing.T) {
	s := tenantStore(t)
	ctx := context.Background()

	in := newTenant("acme-create", "ext-acme-create")
	created, err := s.Create(ctx, in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.RowVersion != 1 || created.Status != domain.TenantActive {
		t.Fatalf("unexpected created tenant: %+v", created)
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Errorf("timestamps not returned: %+v", created)
	}

	got, err := s.Get(ctx, created.TenantID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Slug != "acme-create" || got.ExternalID != "ext-acme-create" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	if _, err := s.Get(ctx, "no-such-tenant"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown id = %v, want ErrNotFound", err)
	}
}

// The system tenant is seeded by the migration. Every pre-tenancy row will
// reference it, so its absence would break the backfill in the next migration.
func TestTenantStore_SystemTenantSeeded(t *testing.T) {
	s := tenantStore(t)

	sys, err := s.Get(context.Background(), domain.SystemTenantID)
	if err != nil {
		t.Fatalf("system tenant must exist: %v", err)
	}
	if !sys.Active() {
		t.Errorf("system tenant must be active, got %q", sys.Status)
	}
	if sys.ExternalID != "system" {
		t.Errorf("system external_id = %q, want \"system\"", sys.ExternalID)
	}
}

func TestTenantStore_DuplicateSlugConflicts(t *testing.T) {
	s := tenantStore(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, newTenant("dup-slug", "ext-dup-1")); err != nil {
		t.Fatalf("first create: %v", err)
	}
	// Same slug, different external id.
	_, err := s.Create(ctx, newTenant("dup-slug", "ext-dup-2"))
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate slug = %v, want ErrConflict", err)
	}
}

func TestTenantStore_DuplicateExternalIDConflicts(t *testing.T) {
	s := tenantStore(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, newTenant("ext-dup-a", "ext-shared")); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := s.Create(ctx, newTenant("ext-dup-b", "ext-shared"))
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate external id = %v, want ErrConflict", err)
	}
}

// external_id is empty until an identity provider is bound. The unique index is
// partial precisely so many unbound tenants can coexist; a plain UNIQUE would
// make the second one fail.
func TestTenantStore_MultipleTenantsMayHaveNoExternalID(t *testing.T) {
	s := tenantStore(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, newTenant("unbound-one", "")); err != nil {
		t.Fatalf("first unbound tenant: %v", err)
	}
	if _, err := s.Create(ctx, newTenant("unbound-two", "")); err != nil {
		t.Fatalf("second unbound tenant must be allowed: %v", err)
	}
}

func TestTenantStore_GetByExternalID(t *testing.T) {
	s := tenantStore(t)
	ctx := context.Background()

	created, err := s.Create(ctx, newTenant("lookup-me", "ext-lookup"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := s.GetByExternalID(ctx, "ext-lookup")
	if err != nil {
		t.Fatalf("get by external: %v", err)
	}
	if got.TenantID != created.TenantID {
		t.Fatalf("resolved %q, want %q", got.TenantID, created.TenantID)
	}

	if _, err := s.GetByExternalID(ctx, "ext-nobody"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown external id = %v, want ErrNotFound", err)
	}
}

// An empty external id must never resolve. The column defaults to empty for
// unbound tenants, so matching on it would let a token asserting no tenant
// land inside an arbitrary customer's boundary.
func TestTenantStore_EmptyExternalIDNeverResolves(t *testing.T) {
	s := tenantStore(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, newTenant("unbound-lookup", "")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.GetByExternalID(ctx, ""); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("empty external id = %v, want ErrNotFound", err)
	}
}

func TestTenantStore_SetStatusOptimistic(t *testing.T) {
	s := tenantStore(t)
	ctx := context.Background()

	created, err := s.Create(ctx, newTenant("suspend-me", "ext-suspend"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	updated, err := s.SetStatus(ctx, created.TenantID, created.RowVersion, domain.TenantSuspended)
	if err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if updated.Status != domain.TenantSuspended || updated.RowVersion != 2 {
		t.Fatalf("unexpected update: %+v", updated)
	}

	// The stale version must be refused, not silently applied.
	_, err = s.SetStatus(ctx, created.TenantID, created.RowVersion, domain.TenantActive)
	if !errors.Is(err, domain.ErrVersionMismatch) {
		t.Fatalf("stale version = %v, want ErrVersionMismatch", err)
	}

	// A missing tenant is 404, not a version conflict — the store probes to
	// distinguish them so the transport can map each correctly.
	_, err = s.SetStatus(ctx, "no-such-tenant", 1, domain.TenantActive)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown tenant = %v, want ErrNotFound", err)
	}
}

func TestTenantStore_List(t *testing.T) {
	s := tenantStore(t)
	ctx := context.Background()

	for _, slug := range []string{"list-charlie", "list-alpha", "list-bravo"} {
		if _, err := s.Create(ctx, newTenant(slug, "ext-"+slug)); err != nil {
			t.Fatalf("create %s: %v", slug, err)
		}
	}

	all, err := s.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	// Ordering is by slug; assert the created three appear in order relative to
	// each other rather than asserting a total count, which the system row and
	// any other test's rows would make brittle.
	var seen []string
	for _, tn := range all {
		switch tn.Slug {
		case "list-alpha", "list-bravo", "list-charlie":
			seen = append(seen, tn.Slug)
		}
	}
	want := []string{"list-alpha", "list-bravo", "list-charlie"}
	if len(seen) != len(want) {
		t.Fatalf("found %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("order = %v, want %v", seen, want)
		}
	}
}

// The database enforces the same slug shape the domain does. If the two ever
// drift, a request that should fail with a 422 would instead reach the driver
// and surface as a 500 — so the constraint is asserted directly.
func TestTenantStore_DatabaseRejectsMalformedSlug(t *testing.T) {
	s := tenantStore(t)

	bad := newTenant("acme-ok", "ext-bad-slug")
	bad.Slug = "Not A Slug"
	if _, err := s.Create(context.Background(), bad); err == nil {
		t.Fatal("the database must reject a malformed slug")
	}
}

// Status is constrained in the schema as well as the domain.
func TestTenantStore_DatabaseRejectsUnknownStatus(t *testing.T) {
	s := tenantStore(t)

	bad := newTenant("status-check", "ext-status-check")
	bad.Status = "deleted"
	if _, err := s.Create(context.Background(), bad); err == nil {
		t.Fatal("the database must reject an unknown status")
	}
}
