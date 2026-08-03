//go:build integration

package postgres

import (
	"errors"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

// E1.4: bulk import/export and reconciliation of CIs from multiple sources.

// newImportedAsset builds an asset with an explicit source/external_ref,
// mirroring domain.NewAsset + Asset.ApplySource's contract.
func newImportedAsset(t *testing.T, tenantID, assetType, name, source, externalRef string) *domain.Asset {
	t.Helper()
	a, err := domain.NewAsset(tenantID, assetType, name, nil)
	if err != nil {
		t.Fatalf("new asset: %v", err)
	}
	ref := externalRef
	if err := a.ApplySource(source, &ref); err != nil {
		t.Fatalf("apply source: %v", err)
	}
	return a
}

// THIS MUST BITE: re-importing the identical payload creates no new row, and
// writes no new history — the idempotency guarantee the whole feature exists
// for.
func TestAssetStore_Upsert_ReimportIsIdempotent(t *testing.T) {
	testPool(t)
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "asset-import-idempotent")

	scoped := tenantScopedPool(t)
	store := NewAssetStore(scoped)
	ctx := assetTestCtx(tn)

	a := newImportedAsset(t, tn.TenantID, "server", "db-primary-01", "aws", "i-0123")
	first, created, err := store.Upsert(ctx, a)
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if !created {
		t.Fatal("first upsert must create")
	}

	before, err := store.List(ctx, 0, "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	beforeHist, err := store.History(ctx, first.AssetID, 0, "")
	if err != nil {
		t.Fatalf("history: %v", err)
	}

	// Re-import the IDENTICAL payload — a fresh domain.Asset built the exact
	// same way, mirroring what a second call to POST .../import would send.
	again := newImportedAsset(t, tn.TenantID, "server", "db-primary-01", "aws", "i-0123")
	second, created2, err := store.Upsert(ctx, again)
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if created2 {
		t.Error("re-importing the same payload must not create a new row")
	}
	if second.AssetID != first.AssetID {
		t.Fatalf("re-import matched a different asset: %s vs %s", second.AssetID, first.AssetID)
	}
	if second.RowVersion != first.RowVersion {
		t.Errorf("row_version changed on a no-op re-import: %d -> %d", first.RowVersion, second.RowVersion)
	}

	after, err := store.List(ctx, 0, "", "")
	if err != nil {
		t.Fatalf("list after: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("re-import changed the row count: %d -> %d", len(before), len(after))
	}
	afterHist, err := store.History(ctx, first.AssetID, 0, "")
	if err != nil {
		t.Fatalf("history after: %v", err)
	}
	if len(afterHist) != len(beforeHist) {
		t.Errorf("re-import wrote spurious history: %d -> %d rows", len(beforeHist), len(afterHist))
	}
	if len(afterHist) != 1 {
		t.Errorf("history has %d row(s), want exactly 1 (created)", len(afterHist))
	}
}

// A row without external_ref always creates — re-"importing" a manual entry
// twice produces two distinct rows, exactly as a direct POST /admin/assets
// already would.
func TestAssetStore_Upsert_NilExternalRefAlwaysCreates(t *testing.T) {
	testPool(t)
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "asset-import-nil-ref")

	scoped := tenantScopedPool(t)
	store := NewAssetStore(scoped)
	ctx := assetTestCtx(tn)

	a1, _ := domain.NewAsset(tn.TenantID, "server", "manual-host", nil)
	first, created1, err := store.Upsert(ctx, a1)
	if err != nil || !created1 {
		t.Fatalf("first upsert: created=%v err=%v", created1, err)
	}

	a2, _ := domain.NewAsset(tn.TenantID, "server", "manual-host", nil)
	second, created2, err := store.Upsert(ctx, a2)
	if err != nil || !created2 {
		t.Fatalf("second upsert: created=%v err=%v", created2, err)
	}
	if first.AssetID == second.AssetID {
		t.Error("two manual (nil external_ref) upserts must not collapse into one row")
	}
}

// THIS MUST BITE: a matched row is updated — every field the import row
// carries replaces the stored value, and each field that actually changed
// is recorded through the E1.3 change-history path.
func TestAssetStore_Upsert_UpdatesExistingAndRecordsHistory(t *testing.T) {
	testPool(t)
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "asset-import-update")

	scoped := tenantScopedPool(t)
	store := NewAssetStore(scoped)
	ctx := assetTestCtx(tn)

	original := newImportedAsset(t, tn.TenantID, "server", "web-01", "aws", "i-9999")
	created, _, err := store.Upsert(ctx, original)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	renamed := newImportedAsset(t, tn.TenantID, "server", "web-01-renamed", "aws", "i-9999")
	if err := renamed.ApplyClassification("production", "high", nil, nil); err != nil {
		t.Fatalf("apply classification: %v", err)
	}
	updated, createdAgain, err := store.Upsert(ctx, renamed)
	if err != nil {
		t.Fatalf("update via upsert: %v", err)
	}
	if createdAgain {
		t.Error("a matching external_ref must update, not create")
	}
	if updated.AssetID != created.AssetID {
		t.Fatalf("update matched a different asset")
	}
	if updated.Name != "web-01-renamed" || updated.Environment != domain.AssetEnvironmentProduction ||
		updated.Criticality != domain.AssetCriticalityHigh {
		t.Errorf("fields not replaced: %+v", updated)
	}
	if updated.RowVersion != created.RowVersion+1 {
		t.Errorf("row_version = %d, want %d", updated.RowVersion, created.RowVersion+1)
	}

	hist, err := store.History(ctx, created.AssetID, 0, "")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	// created + (name, environment, criticality) = 4 rows.
	if len(hist) != 4 {
		t.Fatalf("history has %d row(s), want 4: %+v", len(hist), hist)
	}
	fields := map[string]bool{}
	for _, h := range hist {
		fields[h.Field] = true
	}
	for _, want := range []string{"name", "environment", "criticality"} {
		if !fields[want] {
			t.Errorf("history is missing a row for field %q: %+v", want, hist)
		}
	}
}

// THIS MUST BITE: the partial unique index is enforced by the database, not
// merely by application logic that happened to check first — a direct SQL
// INSERT bypassing AssetStore entirely is still refused.
func TestAsset_SourceExternalRefUniqueness_IsEnforcedByTheDatabase(t *testing.T) {
	testPool(t)
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "asset-source-uniqueness")

	scoped := tenantScopedPool(t)
	store := NewAssetStore(scoped)
	ctx := assetTestCtx(tn)

	a := newImportedAsset(t, tn.TenantID, "server", "host-1", "aws", "i-dup")
	if _, _, err := store.Upsert(ctx, a); err != nil {
		t.Fatalf("first insert: %v", err)
	}

	// A raw INSERT for the same (tenant, source, external_ref) — bypassing
	// AssetStore.Upsert's own FOR UPDATE/SELECT-first logic entirely — must
	// still be refused by ux_asset_tenant_source_external_ref.
	_, err := scoped.Exec(ctx, `
		INSERT INTO asset (asset_id, tenant_id, type, name, source, external_ref)
		VALUES ($1, $2, 'server', 'sneaked-in', 'aws', 'i-dup')`,
		domain.NewID(), tn.TenantID)
	if err == nil {
		t.Fatal("a second row with the same (tenant, source, external_ref) was accepted by the database")
	}

	// Different source, same external_ref: NOT a uniqueness violation — the
	// constraint is scoped per source, per (tenant, source, external_ref).
	other := newImportedAsset(t, tn.TenantID, "server", "host-1-discovered", "discovery", "i-dup")
	if _, _, err := store.Upsert(ctx, other); err != nil {
		t.Fatalf("a different source with the same external_ref must be accepted: %v", err)
	}
}

// THIS MUST BITE: Export, Duplicates and Upsert are all tenant-scoped by row-
// level security — tenant B's import/export/duplicate view never contains a
// trace of tenant A's data, even when both use the identical source and
// external_ref.
func TestAssetImport_RLSIsolatesByTenant(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a, err := tenants.Create(adminTestCtx(), newTenant("import-alpha", "ext-import-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(adminTestCtx(), newTenant("import-bravo", "ext-import-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	scoped := tenantScopedPool(t)
	store := NewAssetStore(scoped)
	ctxA := assetTestCtx(a)
	ctxB := assetTestCtx(b)

	// Both tenants use the identical (source, external_ref) — legal, because
	// the uniqueness constraint (and RLS) is scoped per tenant.
	assetA := newImportedAsset(t, a.TenantID, "server", "shared-host", "aws", "i-shared")
	createdA, _, err := store.Upsert(ctxA, assetA)
	if err != nil {
		t.Fatalf("upsert for tenant a: %v", err)
	}
	assetB := newImportedAsset(t, b.TenantID, "server", "shared-host", "aws", "i-shared")
	if _, _, err := store.Upsert(ctxB, assetB); err != nil {
		t.Fatalf("upsert for tenant b: %v", err)
	}

	// Export: tenant B never sees tenant A's row.
	pageB, err := store.Export(ctxB, 0, "")
	if err != nil {
		t.Fatalf("export as tenant b: %v", err)
	}
	for _, got := range pageB {
		if got.AssetID == createdA.AssetID {
			t.Fatalf("tenant B's export leaked tenant A's asset: %+v", got)
		}
	}

	// Duplicates: sharing (source, external_ref) ACROSS TENANTS must never be
	// reported as a duplicate group — RLS confines Duplicates' own queries to
	// one tenant, so it cannot even see the other tenant's row to compare
	// against.
	groupsB, err := store.Duplicates(ctxB)
	if err != nil {
		t.Fatalf("duplicates as tenant b: %v", err)
	}
	for _, g := range groupsB {
		for _, m := range g.Members {
			if m.AssetID == createdA.AssetID {
				t.Fatalf("tenant B's duplicate report leaked tenant A's asset: %+v", g)
			}
		}
	}

	// Upsert itself: tenant B re-importing the same (source, external_ref)
	// must match ITS OWN row, never tenant A's.
	again := newImportedAsset(t, b.TenantID, "server", "shared-host-v2", "aws", "i-shared")
	updatedB, createdAgain, err := store.Upsert(ctxB, again)
	if err != nil {
		t.Fatalf("re-upsert as tenant b: %v", err)
	}
	if createdAgain {
		t.Error("tenant B's re-import of its own external_ref must update, not create a duplicate")
	}
	if updatedB.AssetID == createdA.AssetID {
		t.Fatal("tenant B's upsert matched tenant A's row across the tenant boundary")
	}
}

// THIS MUST BITE: the cross-source rule groups CIs that share one
// external_ref reported by two or more DIFFERENT sources, and nothing else.
func TestAssetStore_Duplicates_GroupsSameExternalRefAcrossSources(t *testing.T) {
	testPool(t)
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "asset-dup-external-ref")

	scoped := tenantScopedPool(t)
	store := NewAssetStore(scoped)
	ctx := assetTestCtx(tn)

	fromAWS := newImportedAsset(t, tn.TenantID, "server", "i-cross-1-as-seen-by-aws", "aws", "i-cross-1")
	if _, _, err := store.Upsert(ctx, fromAWS); err != nil {
		t.Fatalf("upsert aws: %v", err)
	}
	fromDiscovery := newImportedAsset(t, tn.TenantID, "server", "i-cross-1-as-seen-by-discovery", "discovery", "i-cross-1")
	if _, _, err := store.Upsert(ctx, fromDiscovery); err != nil {
		t.Fatalf("upsert discovery: %v", err)
	}
	// A lone, unrelated CI must not appear in any group.
	lone := newImportedAsset(t, tn.TenantID, "server", "lonely-host", "snmp", "i-lonely")
	if _, _, err := store.Upsert(ctx, lone); err != nil {
		t.Fatalf("upsert lone: %v", err)
	}

	groups, err := store.Duplicates(ctx)
	if err != nil {
		t.Fatalf("duplicates: %v", err)
	}
	var found *domain.AssetDuplicateGroup
	for _, g := range groups {
		if g.Reason == domain.AssetDuplicateSameExternalRef && g.Key == "i-cross-1" {
			found = g
		}
		for _, m := range g.Members {
			if m.ExternalRef != nil && *m.ExternalRef == "i-lonely" {
				t.Errorf("a CI with no cross-source match must not appear in any group: %+v", g)
			}
		}
	}
	if found == nil {
		t.Fatalf("no group found for external_ref i-cross-1: %+v", groups)
	}
	if len(found.Members) != 2 {
		t.Errorf("group has %d member(s), want 2: %+v", len(found.Members), found.Members)
	}
	sources := map[string]bool{}
	for _, m := range found.Members {
		sources[m.Source] = true
	}
	if !sources["aws"] || !sources["discovery"] {
		t.Errorf("group must span both sources: %+v", found.Members)
	}
}

// THIS MUST BITE: the manual-entry rule groups CIs of the same type whose
// name is identical only after normalization (trimmed, case-folded).
func TestAssetStore_Duplicates_GroupsSameTypeAndNormalizedName(t *testing.T) {
	testPool(t)
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "asset-dup-type-name")

	scoped := tenantScopedPool(t)
	store := NewAssetStore(scoped)
	ctx := assetTestCtx(tn)

	first, err := domain.NewAsset(tn.TenantID, "server", "DB-Primary-01", nil)
	if err != nil {
		t.Fatalf("new asset: %v", err)
	}
	if _, _, err := store.Upsert(ctx, first); err != nil {
		t.Fatalf("upsert first: %v", err)
	}
	second, err := domain.NewAsset(tn.TenantID, "server", "  db-primary-01  ", nil)
	if err != nil {
		t.Fatalf("new asset: %v", err)
	}
	if _, _, err := store.Upsert(ctx, second); err != nil {
		t.Fatalf("upsert second: %v", err)
	}
	// Same name, DIFFERENT type: must not be grouped with the pair above.
	differentType, err := domain.NewAsset(tn.TenantID, "network_device", "db-primary-01", nil)
	if err != nil {
		t.Fatalf("new asset: %v", err)
	}
	if _, _, err := store.Upsert(ctx, differentType); err != nil {
		t.Fatalf("upsert different type: %v", err)
	}

	groups, err := store.Duplicates(ctx)
	if err != nil {
		t.Fatalf("duplicates: %v", err)
	}
	var found *domain.AssetDuplicateGroup
	for _, g := range groups {
		if g.Reason == domain.AssetDuplicateSameTypeName && g.Key == "server:db-primary-01" {
			found = g
		}
	}
	if found == nil {
		t.Fatalf("no group found for server:db-primary-01: %+v", groups)
	}
	if len(found.Members) != 2 {
		t.Errorf("group has %d member(s), want 2 (the network_device must be excluded): %+v",
			len(found.Members), found.Members)
	}
	for _, m := range found.Members {
		if m.Type != "server" {
			t.Errorf("a non-server asset leaked into the server:db-primary-01 group: %+v", m)
		}
	}
}

// No duplicates present must report an empty, not vacuously-matching, result.
func TestAssetStore_Duplicates_EmptyWhenNothingMatches(t *testing.T) {
	testPool(t)
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "asset-dup-none")

	scoped := tenantScopedPool(t)
	store := NewAssetStore(scoped)
	ctx := assetTestCtx(tn)

	a, _ := domain.NewAsset(tn.TenantID, "server", "unique-host-name", nil)
	if _, _, err := store.Upsert(ctx, a); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	groups, err := store.Duplicates(ctx)
	if err != nil {
		t.Fatalf("duplicates: %v", err)
	}
	for _, g := range groups {
		for _, m := range g.Members {
			if m.AssetID == a.AssetID {
				t.Errorf("a lone asset was reported as a duplicate: %+v", g)
			}
		}
	}
}

// Export includes a retired asset (unlike List's default view) and paginates
// with the same keyset shape List uses.
func TestAssetStore_Export_IncludesRetiredAndPaginates(t *testing.T) {
	testPool(t)
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "asset-export-page")

	scoped := tenantScopedPool(t)
	store := NewAssetStore(scoped)
	ctx := assetTestCtx(tn)

	var ids []string
	for i := 0; i < 3; i++ {
		a, _ := domain.NewAsset(tn.TenantID, "server", "export-host", nil)
		created, err := store.Create(ctx, a)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		ids = append(ids, created.AssetID)
	}
	retired, err := store.SetStatus(ctx, ids[0], 1, domain.AssetRetired)
	if err != nil {
		t.Fatalf("retire: %v", err)
	}

	full, err := store.Export(ctx, 0, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	seen := map[string]bool{}
	for _, a := range full {
		seen[a.AssetID] = true
	}
	if !seen[retired.AssetID] {
		t.Error("export must include a retired asset")
	}
	for _, id := range ids {
		if !seen[id] {
			t.Errorf("export is missing asset %s", id)
		}
	}

	// Keyset pagination: page through with limit=1 and confirm every asset is
	// visited exactly once, in ascending asset_id order.
	var walked []string
	after := ""
	for {
		page, err := store.Export(ctx, 1, after)
		if err != nil {
			t.Fatalf("export page: %v", err)
		}
		if len(page) == 0 {
			break
		}
		walked = append(walked, page[0].AssetID)
		after = page[0].AssetID
		if len(walked) > len(ids)+1 {
			t.Fatal("export pagination looped past the known rows")
		}
	}
	if len(walked) != len(ids) {
		t.Fatalf("paginated export visited %d assets, want %d: %v", len(walked), len(ids), walked)
	}
}

// Sanity: Upsert reports domain.ErrNotFound (not a raw SQL error) for an
// owner reference the tenant cannot see, exactly as Create already does.
func TestAssetStore_Upsert_OwnerRefNotFound(t *testing.T) {
	testPool(t)
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "asset-import-owner-not-found")

	scoped := tenantScopedPool(t)
	store := NewAssetStore(scoped)
	ctx := assetTestCtx(tn)

	a := newImportedAsset(t, tn.TenantID, "server", "host-1", "aws", "i-owner")
	ghostTeam := "no-such-team"
	if err := a.ApplyClassification("", "", &ghostTeam, nil); err != nil {
		t.Fatalf("apply classification: %v", err)
	}
	if _, _, err := store.Upsert(ctx, a); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("upsert with unknown owner_team_id = %v, want ErrNotFound", err)
	}

	// And no row was created despite the failure.
	list, err := store.List(ctx, 0, "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("a failed upsert left a row behind: %+v", list)
	}
}
