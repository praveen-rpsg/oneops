//go:build integration

package postgres

import (
	"errors"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

// settingFixture creates a real organisation (and therefore a real tenant),
// mirroring teamFixture.
func settingFixture(t *testing.T) *domain.Organization {
	t.Helper()
	pool := testPool(t)
	ctx := adminTestCtx()

	org, err := NewOrganizationStore(pool).Create(ctx, mustNewOrg(t, "setting-probe"))
	if err != nil {
		t.Fatalf("create org: %v", err)
	}
	return org
}

func TestSettingStore_GetAllIsEmptyForANewTenant(t *testing.T) {
	org := settingFixture(t)
	ctx := domain.WithTenant(adminTestCtx(), &domain.Tenant{TenantID: org.TenantID, Status: domain.TenantActive})
	store := NewSettingStore(tenantScopedPool(t))

	got, err := store.GetAll(ctx)
	if err != nil {
		t.Fatalf("get all: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("a tenant with no overrides has %d stored settings, want 0", len(got))
	}
}

func TestSettingStore_UpsertCreatesThenLists(t *testing.T) {
	org := settingFixture(t)
	ctx := domain.WithTenant(adminTestCtx(), &domain.Tenant{TenantID: org.TenantID, Status: domain.TenantActive})
	store := NewSettingStore(tenantScopedPool(t))

	s, err := domain.NewSetting(org.TenantID, "default_page_size", "100", 0)
	if err != nil {
		t.Fatalf("new setting: %v", err)
	}
	created, err := store.Upsert(ctx, s)
	if err != nil {
		t.Fatalf("upsert (create): %v", err)
	}
	if created.Value != "100" || created.RowVersion != 1 {
		t.Errorf("unexpected created row: %+v", created)
	}

	all, err := store.GetAll(ctx)
	if err != nil {
		t.Fatalf("get all: %v", err)
	}
	if len(all) != 1 || all[0].Key != "default_page_size" || all[0].Value != "100" {
		t.Errorf("unexpected overrides: %+v", all)
	}
}

// Creating twice — row_version 0 both times — must be a conflict, not a
// silent second insert or a silent overwrite.
func TestSettingStore_CreateOverExistingIsVersionMismatch(t *testing.T) {
	org := settingFixture(t)
	ctx := domain.WithTenant(adminTestCtx(), &domain.Tenant{TenantID: org.TenantID, Status: domain.TenantActive})
	store := NewSettingStore(tenantScopedPool(t))

	s, _ := domain.NewSetting(org.TenantID, "default_page_size", "100", 0)
	if _, err := store.Upsert(ctx, s); err != nil {
		t.Fatalf("first create: %v", err)
	}

	again, _ := domain.NewSetting(org.TenantID, "default_page_size", "200", 0)
	if _, err := store.Upsert(ctx, again); !errors.Is(err, domain.ErrVersionMismatch) {
		t.Errorf("second create-with-zero = %v, want ErrVersionMismatch", err)
	}

	// The first write must have won; the refused second write must not apply.
	all, err := store.GetAll(ctx)
	if err != nil {
		t.Fatalf("get all: %v", err)
	}
	if len(all) != 1 || all[0].Value != "100" {
		t.Errorf("unexpected state after refused create: %+v", all)
	}
}

// Optimistic locking on update: a caller holding a stale row_version must be
// refused, not silently overwrite a concurrent change.
func TestSettingStore_UpdateDetectsVersionConflict(t *testing.T) {
	org := settingFixture(t)
	ctx := domain.WithTenant(adminTestCtx(), &domain.Tenant{TenantID: org.TenantID, Status: domain.TenantActive})
	store := NewSettingStore(tenantScopedPool(t))

	s, _ := domain.NewSetting(org.TenantID, "default_page_size", "100", 0)
	created, err := store.Upsert(ctx, s)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	first, err := domain.NewSetting(org.TenantID, "default_page_size", "150", created.RowVersion)
	if err != nil {
		t.Fatalf("new setting: %v", err)
	}
	updated, err := store.Upsert(ctx, first)
	if err != nil {
		t.Fatalf("first update: %v", err)
	}
	if updated.Value != "150" || updated.RowVersion != created.RowVersion+1 {
		t.Errorf("unexpected updated row: %+v", updated)
	}

	// Reusing the now-stale row_version must be a conflict, not a second
	// successful update.
	stale, _ := domain.NewSetting(org.TenantID, "default_page_size", "300", created.RowVersion)
	if _, err := store.Upsert(ctx, stale); !errors.Is(err, domain.ErrVersionMismatch) {
		t.Errorf("stale update = %v, want ErrVersionMismatch", err)
	}

	all, err := store.GetAll(ctx)
	if err != nil {
		t.Fatalf("get all: %v", err)
	}
	if len(all) != 1 || all[0].Value != "150" {
		t.Errorf("a refused update must not apply: %+v", all)
	}
}

// Updating a key that has never been overridden — a nonzero row_version
// against a row that does not exist — is also a version mismatch, not a
// silent create.
func TestSettingStore_UpdateOnUnknownOverrideIsVersionMismatch(t *testing.T) {
	org := settingFixture(t)
	ctx := domain.WithTenant(adminTestCtx(), &domain.Tenant{TenantID: org.TenantID, Status: domain.TenantActive})
	store := NewSettingStore(tenantScopedPool(t))

	s, _ := domain.NewSetting(org.TenantID, "default_page_size", "100", 3)
	if _, err := store.Upsert(ctx, s); !errors.Is(err, domain.ErrVersionMismatch) {
		t.Errorf("update on unknown override = %v, want ErrVersionMismatch", err)
	}
}

// An unknown key or an invalid value must be refused by domain validation
// before any statement reaches the database — Setting.Validate is the single
// gate NewSetting and Upsert both go through.
func TestSettingStore_UpsertRejectsUnknownKeyAndInvalidValue(t *testing.T) {
	org := settingFixture(t)

	if _, err := domain.NewSetting(org.TenantID, "not_a_real_setting", "anything", 0); err == nil {
		t.Error("an unknown key was accepted")
	}
	if _, err := domain.NewSetting(org.TenantID, "default_page_size", "not-a-number", 0); err == nil {
		t.Error("an invalid value was accepted")
	}
}

// The class this story exists to prevent: setting is tenant data, and one
// tenant's overrides must not be visible to, or writable by, another tenant's
// connection.
func TestRLS_SettingsAreTenantIsolated(t *testing.T) {
	priv := testPool(t)
	ctx := adminTestCtx()

	orgs := NewOrganizationStore(priv)
	a, err := orgs.Create(ctx, mustNewOrg(t, "rls-setting-a"))
	if err != nil {
		t.Fatalf("create org a: %v", err)
	}
	b, err := orgs.Create(ctx, mustNewOrg(t, "rls-setting-b"))
	if err != nil {
		t.Fatalf("create org b: %v", err)
	}

	scoped := tenantScopedPool(t)
	store := NewSettingStore(scoped)

	aCtx := domain.WithTenant(ctx, &domain.Tenant{TenantID: a.TenantID, Status: domain.TenantActive})
	settingA, err := domain.NewSetting(a.TenantID, "notification_from_name", "Alpha Secret Corp", 0)
	if err != nil {
		t.Fatalf("build setting a: %v", err)
	}
	created, err := store.Upsert(aCtx, settingA)
	if err != nil {
		t.Fatalf("tenant a creates override: %v", err)
	}

	// Tenant B must not see tenant A's override when listing its own settings...
	bCtx := domain.WithTenant(ctx, &domain.Tenant{TenantID: b.TenantID, Status: domain.TenantActive})
	listAsB, err := store.GetAll(bCtx)
	if err != nil {
		t.Fatalf("list as tenant b: %v", err)
	}
	if len(listAsB) != 0 {
		t.Fatalf("tenant b listed %d of tenant a's settings — isolation is not enforced", len(listAsB))
	}

	// ...and must not be able to update it by presenting its known
	// row_version: from tenant b's connection no row exists for that key at
	// all (RLS makes tenant a's row invisible, not merely off-limits), so the
	// UPDATE matches nothing and fails exactly as a stale version would — a
	// forged version number cannot be used to blindly overwrite a row the
	// attacker cannot even see.
	settingAsB, err := domain.NewSetting(b.TenantID, "notification_from_name", "Overwritten", created.RowVersion)
	if err != nil {
		t.Fatalf("build attacker setting: %v", err)
	}
	if _, err := store.Upsert(bCtx, settingAsB); !errors.Is(err, domain.ErrVersionMismatch) {
		t.Fatalf("tenant b's update against tenant a's row = %v, want ErrVersionMismatch", err)
	}

	// Tenant A's row must be untouched by tenant B's refused write.
	stillA, err := store.GetAll(aCtx)
	if err != nil {
		t.Fatalf("get all as tenant a: %v", err)
	}
	if len(stillA) != 1 || stillA[0].Value != "Alpha Secret Corp" {
		t.Fatalf("tenant a's override was perturbed by tenant b's write: %+v", stillA)
	}

	// The owning tenant still creates and reads its own override, so the
	// refusals above are about isolation and not about a path that never
	// works.
	settingB, err := domain.NewSetting(b.TenantID, "notification_from_name", "Bravo Corp", 0)
	if err != nil {
		t.Fatalf("build setting b: %v", err)
	}
	if _, err := store.Upsert(bCtx, settingB); err != nil {
		t.Fatalf("tenant b creates its own override: %v", err)
	}
	ownB, err := store.GetAll(bCtx)
	if err != nil {
		t.Fatalf("get all as tenant b: %v", err)
	}
	if len(ownB) != 1 || ownB[0].Value != "Bravo Corp" {
		t.Fatalf("tenant b must still see its own override: %+v", ownB)
	}
}
