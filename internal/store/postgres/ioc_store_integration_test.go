//go:build integration

package postgres

import (
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

func TestIOCStore_CreateGetListUpdateDelete(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "ioc-crud")

	scoped := tenantScopedPool(t)
	iocs := NewIOCStore(scoped)
	ctx := assetTestCtx(tn)

	i, err := domain.NewIOC(tn.TenantID, domain.IOCIndicatorTypeIP, "203.0.113.5", domain.IncidentSeverityHigh)
	if err != nil {
		t.Fatalf("new ioc: %v", err)
	}
	created, err := iocs.Create(ctx, i)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.RowVersion != 1 {
		t.Errorf("row_version = %d, want 1", created.RowVersion)
	}
	if !created.Enabled {
		t.Error("a new ioc must be enabled by default")
	}
	if created.Description != "" || created.Source != "" {
		t.Errorf("a new ioc must have no description/source by default: %+v", created)
	}

	got, err := iocs.Get(ctx, created.IOCID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.IndicatorValue != "203.0.113.5" || got.IndicatorType != domain.IOCIndicatorTypeIP {
		t.Errorf("get returned %+v, want the created fields", got)
	}

	list, err := iocs.List(ctx, 0, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, item := range list {
		if item.IOCID == created.IOCID {
			found = true
		}
	}
	if !found {
		t.Errorf("list did not include the created ioc: %+v", list)
	}

	newSource := "misp"
	updated, err := iocs.Update(ctx, created.IOCID, created.RowVersion, domain.IOCPatch{Source: &newSource})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Source != newSource || updated.RowVersion != 2 {
		t.Errorf("update = %+v, want source %v and row_version 2", updated, newSource)
	}

	// A stale row_version is refused.
	if _, err := iocs.Update(ctx, created.IOCID, created.RowVersion, domain.IOCPatch{Source: &newSource}); err != domain.ErrVersionMismatch {
		t.Errorf("stale update err = %v, want ErrVersionMismatch", err)
	}

	if err := iocs.Delete(ctx, created.IOCID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := iocs.Get(ctx, created.IOCID); err != domain.ErrNotFound {
		t.Errorf("get after delete err = %v, want ErrNotFound", err)
	}
	if err := iocs.Delete(ctx, created.IOCID); err != domain.ErrNotFound {
		t.Errorf("delete of an already-deleted ioc err = %v, want ErrNotFound", err)
	}
}

// TestIOCStore_DuplicateIndicatorWithinTenantConflicts proves the UNIQUE
// (tenant_id, indicator_type, indicator_value) constraint surfaces as
// domain.ErrConflict, not a silently-absorbed write or a 500.
func TestIOCStore_DuplicateIndicatorWithinTenantConflicts(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "ioc-dup")

	scoped := tenantScopedPool(t)
	iocs := NewIOCStore(scoped)
	ctx := assetTestCtx(tn)

	first, err := domain.NewIOC(tn.TenantID, domain.IOCIndicatorTypeDomain, "Evil.Example.COM", domain.IncidentSeverityHigh)
	if err != nil {
		t.Fatalf("new ioc: %v", err)
	}
	if _, err := iocs.Create(ctx, first); err != nil {
		t.Fatalf("create first: %v", err)
	}

	// Same indicator, different case — normalizes to the identical value, so
	// this must ALSO conflict, not slip past the constraint on a case
	// technicality.
	dup, err := domain.NewIOC(tn.TenantID, domain.IOCIndicatorTypeDomain, "evil.example.com", domain.IncidentSeverityLow)
	if err != nil {
		t.Fatalf("new ioc: %v", err)
	}
	if _, err := iocs.Create(ctx, dup); err != domain.ErrConflict {
		t.Errorf("duplicate indicator err = %v, want ErrConflict", err)
	}

	// A different indicator_type with the SAME value string is a distinct key
	// and must be accepted.
	distinctType, err := domain.NewIOC(tn.TenantID, domain.IOCIndicatorTypeURL, "evil.example.com", domain.IncidentSeverityHigh)
	if err != nil {
		t.Fatalf("new ioc: %v", err)
	}
	if _, err := iocs.Create(ctx, distinctType); err != nil {
		t.Errorf("distinct indicator_type with same value: create err = %v, want nil", err)
	}
}

// TestIOCIsolation_RLSByTenant is the security gate for this story: tenant B
// can never see, list, patch or delete tenant A's IOC entries, even by the
// exact ioc_id — the same two-tenant proof
// TestSecurityRuleIsolation_RLSByTenant establishes for security_rule. It
// also proves the SAME indicator_value can exist independently in two
// tenants — the unique constraint is tenant-scoped, not global.
func TestIOCIsolation_RLSByTenant(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a, err := tenants.Create(adminTestCtx(), newTenant("ioc-iso-alpha", "ext-ioc-iso-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(adminTestCtx(), newTenant("ioc-iso-bravo", "ext-ioc-iso-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	scoped := tenantScopedPool(t)
	iocs := NewIOCStore(scoped)
	ctxA := assetTestCtx(a)
	ctxB := assetTestCtx(b)

	iA, err := domain.NewIOC(a.TenantID, domain.IOCIndicatorTypeIP, "198.51.100.7", domain.IncidentSeverityHigh)
	if err != nil {
		t.Fatalf("new ioc a: %v", err)
	}
	createdA, err := iocs.Create(ctxA, iA)
	if err != nil {
		t.Fatalf("create as tenant a: %v", err)
	}

	// The SAME indicator, created independently by tenant B, must succeed —
	// proving the unique constraint is scoped to (tenant_id, ...), not
	// global.
	iB, err := domain.NewIOC(b.TenantID, domain.IOCIndicatorTypeIP, "198.51.100.7", domain.IncidentSeverityLow)
	if err != nil {
		t.Fatalf("new ioc b: %v", err)
	}
	createdB, err := iocs.Create(ctxB, iB)
	if err != nil {
		t.Fatalf("tenant b create of the identical indicator value err = %v, want nil (unique must be tenant-scoped)", err)
	}
	if createdB.IOCID == createdA.IOCID {
		t.Fatalf("tenant b's create returned tenant a's row: %+v", createdB)
	}

	// READ: tenant B cannot get tenant A's entry by id, and does not see it
	// in its own list (only its own, independently-created entry).
	if _, err := iocs.Get(ctxB, createdA.IOCID); err != domain.ErrNotFound {
		t.Errorf("tenant B read tenant A's ioc: err = %v, want ErrNotFound", err)
	}
	listB, err := iocs.List(ctxB, 0, "")
	if err != nil {
		t.Fatalf("list as tenant b: %v", err)
	}
	if len(listB) != 1 || listB[0].IOCID != createdB.IOCID {
		t.Errorf("tenant B's list = %+v, want exactly its own entry", listB)
	}

	// WRITE: tenant B cannot patch tenant A's entry, even with the correct
	// row_version.
	newSource := "hijacked"
	if _, err := iocs.Update(ctxB, createdA.IOCID, createdA.RowVersion, domain.IOCPatch{Source: &newSource}); err != domain.ErrNotFound {
		t.Errorf("tenant B patched tenant A's ioc: err = %v, want ErrNotFound", err)
	}

	// DELETE: tenant B cannot delete tenant A's entry.
	if err := iocs.Delete(ctxB, createdA.IOCID); err != domain.ErrNotFound {
		t.Errorf("tenant B deleted tenant A's ioc: err = %v, want ErrNotFound", err)
	}

	// Tenant A still sees its own entry, completely undisturbed by tenant
	// B's attempts above.
	stillThere, err := iocs.Get(ctxA, createdA.IOCID)
	if err != nil || stillThere.IOCID != createdA.IOCID {
		t.Fatalf("tenant A lost its own ioc: %v, %+v", err, stillThere)
	}
	if stillThere.Source != "" || stillThere.RowVersion != createdA.RowVersion {
		t.Errorf("tenant A's ioc was mutated by tenant B's attempt: %+v", stillThere)
	}
}
