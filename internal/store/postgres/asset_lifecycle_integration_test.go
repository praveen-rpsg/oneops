//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

// E1.3: CI lifecycle + append-only change history + soft-retire.

// The whole legal lifecycle, live: planned -> active -> maintenance -> active
// -> retired -> active ("reinstate"), each transition checked against the
// database, not merely the in-memory state machine domain_test.go already
// covers.
func TestAssetStore_SetStatus_WalksTheLegalLifecycle(t *testing.T) {
	testPool(t)
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "asset-lifecycle-walk")

	scoped := tenantScopedPool(t)
	store := NewAssetStore(scoped)
	ctx := assetTestCtx(tn)

	a, err := domain.NewAsset(tn.TenantID, "server", "lifecycle-host", nil)
	if err != nil {
		t.Fatalf("new asset: %v", err)
	}
	a.Status = domain.AssetPlanned
	created, err := store.Create(ctx, a)
	if err != nil {
		t.Fatalf("create planned asset: %v", err)
	}
	if created.Status != domain.AssetPlanned {
		t.Fatalf("status = %q, want planned", created.Status)
	}

	cur := created
	for _, next := range []domain.AssetStatus{
		domain.AssetActive, domain.AssetMaintenance, domain.AssetActive,
		domain.AssetRetired, domain.AssetActive,
	} {
		updated, err := store.SetStatus(ctx, cur.AssetID, cur.RowVersion, next)
		if err != nil {
			t.Fatalf("transition %s -> %s: %v", cur.Status, next, err)
		}
		if updated.Status != next {
			t.Fatalf("status = %q, want %q", updated.Status, next)
		}
		if updated.RowVersion != cur.RowVersion+1 {
			t.Fatalf("row_version = %d, want %d", updated.RowVersion, cur.RowVersion+1)
		}
		cur = updated
	}
}

// THIS MUST BITE: an illegal transition is refused by the database-backed
// store, not merely by the in-memory domain guard, and no row is mutated by
// the refused attempt.
func TestAssetStore_SetStatus_RejectsIllegalTransition(t *testing.T) {
	testPool(t)
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "asset-illegal-transition")

	scoped := tenantScopedPool(t)
	store := NewAssetStore(scoped)
	ctx := assetTestCtx(tn)

	a, _ := domain.NewAsset(tn.TenantID, "server", "host", nil)
	a.Status = domain.AssetPlanned
	created, err := store.Create(ctx, a)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// planned -> maintenance is not a legal edge (assetTransitions).
	_, err = store.SetStatus(ctx, created.AssetID, created.RowVersion, domain.AssetMaintenance)
	var transErr *domain.AssetTransitionError
	if !errors.As(err, &transErr) {
		t.Fatalf("planned -> maintenance = %v, want *domain.AssetTransitionError", err)
	}
	if !errors.Is(err, domain.ErrInvalidTransition) {
		t.Error("the error does not unwrap to domain.ErrInvalidTransition")
	}

	// The refused attempt left no trace on the row: still planned, still at
	// its original row_version.
	still, err := store.Get(ctx, created.AssetID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if still.Status != domain.AssetPlanned || still.RowVersion != created.RowVersion {
		t.Errorf("a refused transition mutated the row: %+v", still)
	}

	// And it wrote no history row either — an illegal move is not a change.
	hist, err := store.History(ctx, created.AssetID, 0, "")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	for _, h := range hist {
		if h.Kind == domain.AssetChangeTransitioned {
			t.Errorf("a refused transition still wrote a history row: %+v", h)
		}
	}

	// retired -> planned is also illegal (only retired -> active is a
	// permitted reinstate).
	retired, err := store.SetStatus(ctx, created.AssetID, created.RowVersion, domain.AssetRetired)
	if err != nil {
		t.Fatalf("retire: %v", err)
	}
	if _, err := store.SetStatus(ctx, retired.AssetID, retired.RowVersion, domain.AssetPlanned); !errors.As(err, &transErr) {
		t.Errorf("retired -> planned = %v, want *domain.AssetTransitionError", err)
	}
}

// THIS MUST BITE: a stale row_version on a transition is refused, exactly as
// it is for Update.
func TestAssetStore_SetStatus_DetectsVersionConflict(t *testing.T) {
	testPool(t)
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "asset-transition-conflict")

	scoped := tenantScopedPool(t)
	store := NewAssetStore(scoped)
	ctx := assetTestCtx(tn)

	a, _ := domain.NewAsset(tn.TenantID, "server", "host", nil)
	created, err := store.Create(ctx, a)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := store.SetStatus(ctx, created.AssetID, created.RowVersion, domain.AssetMaintenance); err != nil {
		t.Fatalf("first transition: %v", err)
	}

	// created.RowVersion is now stale (the asset moved to row_version 2).
	if _, err := store.SetStatus(ctx, created.AssetID, created.RowVersion, domain.AssetRetired); !errors.Is(err, domain.ErrVersionMismatch) {
		t.Errorf("stale transition = %v, want ErrVersionMismatch", err)
	}
}

// THIS MUST BITE: List's default view excludes a retired asset, but Get still
// finds it — soft-retire, not deletion.
func TestAssetStore_List_ExcludesRetiredByDefaultButGetStillFindsIt(t *testing.T) {
	testPool(t)
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "asset-soft-retire")

	scoped := tenantScopedPool(t)
	store := NewAssetStore(scoped)
	ctx := assetTestCtx(tn)

	active, _ := domain.NewAsset(tn.TenantID, "server", "still-active", nil)
	active, err := store.Create(ctx, active)
	if err != nil {
		t.Fatalf("create active: %v", err)
	}
	toRetire, _ := domain.NewAsset(tn.TenantID, "server", "about-to-retire", nil)
	toRetire, err = store.Create(ctx, toRetire)
	if err != nil {
		t.Fatalf("create to-retire: %v", err)
	}
	retired, err := store.SetStatus(ctx, toRetire.AssetID, toRetire.RowVersion, domain.AssetRetired)
	if err != nil {
		t.Fatalf("retire: %v", err)
	}

	defaultPage, err := store.List(ctx, 0, "", "")
	if err != nil {
		t.Fatalf("list default: %v", err)
	}
	ids := map[string]bool{}
	for _, a := range defaultPage {
		ids[a.AssetID] = true
	}
	if !ids[active.AssetID] {
		t.Error("the default list is missing an active asset")
	}
	if ids[retired.AssetID] {
		t.Error("the default list must exclude a retired asset (soft-retire)")
	}

	// Still individually fetchable.
	got, err := store.Get(ctx, retired.AssetID)
	if err != nil {
		t.Fatalf("get retired asset: %v", err)
	}
	if got.Status != domain.AssetRetired {
		t.Errorf("status = %q, want retired", got.Status)
	}

	// And explicitly listable by status.
	retiredPage, err := store.List(ctx, 0, "", domain.AssetRetired)
	if err != nil {
		t.Fatalf("list retired: %v", err)
	}
	found := false
	for _, a := range retiredPage {
		if a.AssetID == retired.AssetID {
			found = true
		}
		if a.Status != domain.AssetRetired {
			t.Errorf("?status=retired returned a non-retired asset: %+v", a)
		}
	}
	if !found {
		t.Error("?status=retired did not include the retired asset")
	}

	// Its relationship survives too: soft-retire touches only status.
	other, _ := domain.NewAsset(tn.TenantID, "server", "peer", nil)
	other, err = store.Create(ctx, other)
	if err != nil {
		t.Fatalf("create peer: %v", err)
	}
	rel, _ := domain.NewAssetRelationship(tn.TenantID, retired.AssetID, other.AssetID, domain.RelationshipDependsOn)
	if _, err := store.CreateRelationship(ctx, rel); err != nil {
		t.Fatalf("relate retired asset: %v", err)
	}
	from, err := store.RelationshipsFrom(ctx, retired.AssetID)
	if err != nil {
		t.Fatalf("relationships from retired asset: %v", err)
	}
	if len(from) != 1 {
		t.Errorf("a retired asset's relationship did not survive: %+v", from)
	}
}

// THIS MUST BITE: Create/Update/SetStatus each write the history row(s) their
// doc comments promise, with the correct old->new value and the real actor —
// not a fixture constant this test happens to reuse for the write.
func TestAssetStore_History_RecordsCreateUpdateAndTransition(t *testing.T) {
	testPool(t)
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "asset-history-record")

	scoped := tenantScopedPool(t)
	store := NewAssetStore(scoped)
	creator := assetTestCtxAs(tn, "user-creator")
	updater := assetTestCtxAs(tn, "user-updater")
	retirer := assetTestCtxAs(tn, "user-retirer")

	a, _ := domain.NewAsset(tn.TenantID, "server", "history-host", nil)
	created, err := store.Create(creator, a)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	newName := "history-host-renamed"
	updated, err := store.Update(updater, created.AssetID, created.RowVersion,
		domain.AssetPatch{Name: &newName})
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	retired, err := store.SetStatus(retirer, updated.AssetID, updated.RowVersion, domain.AssetRetired)
	if err != nil {
		t.Fatalf("set status: %v", err)
	}

	hist, err := store.History(creator, created.AssetID, 0, "")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(hist) != 3 {
		t.Fatalf("history has %d row(s), want 3 (created, updated name, status_transitioned): %+v", len(hist), hist)
	}

	if retired.Status != domain.AssetRetired {
		t.Fatalf("retired.Status = %q, want retired", retired.Status)
	}

	// change_id is a ULID: History is already chronological ascending.
	createdRow, updatedRow, transitionedRow := hist[0], hist[1], hist[2]

	if createdRow.Kind != domain.AssetChangeCreated || createdRow.Actor != "user-creator" ||
		createdRow.OldValue != nil || createdRow.NewValue != nil || createdRow.RowVersion != 1 {
		t.Errorf("created row wrong: %+v", createdRow)
	}
	if updatedRow.Kind != domain.AssetChangeUpdated || updatedRow.Field != "name" || updatedRow.Actor != "user-updater" ||
		updatedRow.OldValue == nil || *updatedRow.OldValue != "history-host" ||
		updatedRow.NewValue == nil || *updatedRow.NewValue != newName || updatedRow.RowVersion != 2 {
		t.Errorf("updated row wrong: %+v", updatedRow)
	}
	if transitionedRow.Kind != domain.AssetChangeTransitioned || transitionedRow.Field != "status" || transitionedRow.Actor != "user-retirer" ||
		transitionedRow.OldValue == nil || *transitionedRow.OldValue != "active" ||
		transitionedRow.NewValue == nil || *transitionedRow.NewValue != "retired" || transitionedRow.RowVersion != 3 {
		t.Errorf("transitioned row wrong: %+v", transitionedRow)
	}
}

// A patch that changes nothing (the new value equals the old) writes no
// history row — a no-op is not a change worth recording.
func TestAssetStore_History_NoOpUpdateWritesNoRow(t *testing.T) {
	testPool(t)
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "asset-history-noop")

	scoped := tenantScopedPool(t)
	store := NewAssetStore(scoped)
	ctx := assetTestCtx(tn)

	a, _ := domain.NewAsset(tn.TenantID, "server", "same-name", nil)
	created, err := store.Create(ctx, a)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	sameName := "same-name"
	if _, err := store.Update(ctx, created.AssetID, created.RowVersion, domain.AssetPatch{Name: &sameName}); err != nil {
		t.Fatalf("no-op update: %v", err)
	}

	hist, err := store.History(ctx, created.AssetID, 0, "")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(hist) != 1 {
		t.Errorf("history has %d row(s) after a no-op update, want 1 (created only): %+v", len(hist), hist)
	}
}

// THIS MUST BITE: asset_change_history is append-only at the schema level,
// exactly like audit_event — a direct UPDATE/DELETE/TRUNCATE, issued on the
// tenant-scoped connection that wrote the row, is refused by the database
// itself, not merely by store discipline.
func TestAssetChangeHistory_IsAppendOnly(t *testing.T) {
	testPool(t)
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "asset-history-append-only")

	scoped := tenantScopedPool(t)
	store := NewAssetStore(scoped)
	ctx := assetTestCtx(tn)

	a, _ := domain.NewAsset(tn.TenantID, "server", "append-only-host", nil)
	created, err := store.Create(ctx, a)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	hist, err := store.History(ctx, created.AssetID, 0, "")
	if err != nil || len(hist) == 0 {
		t.Fatalf("history: %v (%d rows)", err, len(hist))
	}
	changeID := hist[0].ChangeID

	if _, err := scoped.Exec(ctx,
		`UPDATE asset_change_history SET actor = 'tampered' WHERE change_id = $1`, changeID); err == nil {
		t.Error("asset_change_history must refuse UPDATE on the request-path connection")
	}
	if _, err := scoped.Exec(ctx,
		`DELETE FROM asset_change_history WHERE change_id = $1`, changeID); err == nil {
		t.Error("asset_change_history must refuse DELETE on the request-path connection")
	}
	if _, err := scoped.Exec(ctx, `TRUNCATE asset_change_history`); err == nil {
		t.Error("asset_change_history must refuse TRUNCATE on the request-path connection")
	}

	// The row is exactly as recorded by the three refused tamper attempts.
	after, err := store.History(ctx, created.AssetID, 0, "")
	if err != nil {
		t.Fatalf("re-read history: %v", err)
	}
	if len(after) == 0 || after[0].Actor == "tampered" {
		t.Errorf("history was mutated despite three refused attempts: %+v", after)
	}

	// Also proven at the privileged connection, using ENABLE ALWAYS (not the
	// default origin-mode trigger a session_replication_role='replica' would
	// suppress) — the same hardening audit_event carries.
	conn, err := priv.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(context.Background(), `SET session_replication_role = 'replica'`); err != nil {
		t.Fatalf("set replication role: %v", err)
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), `SET session_replication_role = 'origin'`)
	}()
	if _, err := conn.Exec(context.Background(),
		`UPDATE asset_change_history SET actor = 'tampered' WHERE change_id = $1`, changeID); err == nil {
		t.Error("session_replication_role='replica' bypassed asset_change_history's guard — it is not ENABLE ALWAYS")
	}
}

// THIS MUST BITE: tenant B cannot see tenant A's change history, either
// through the store's own History method or through a direct query on the
// tenant-scoped connection — row-level security, not a WHERE clause the store
// remembered to add.
func TestAssetChangeHistory_RLSIsolatesByTenant(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a, err := tenants.Create(adminTestCtx(), newTenant("asset-hist-alpha", "ext-asset-hist-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(adminTestCtx(), newTenant("asset-hist-bravo", "ext-asset-hist-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	scoped := tenantScopedPool(t)
	store := NewAssetStore(scoped)
	ctxA := assetTestCtx(a)
	ctxB := assetTestCtx(b)

	assetA, _ := domain.NewAsset(a.TenantID, "server", "tenant-a-host", nil)
	createdA, err := store.Create(ctxA, assetA)
	if err != nil {
		t.Fatalf("create for tenant a: %v", err)
	}

	// Tenant B cannot read tenant A's history via the store...
	histAsB, err := store.History(ctxB, createdA.AssetID, 0, "")
	if err != nil {
		t.Fatalf("history as tenant b: %v", err)
	}
	if len(histAsB) != 0 {
		t.Fatalf("tenant B's History() call for tenant A's asset returned rows: %+v", histAsB)
	}

	// ...nor by querying the table directly on tenant B's own tenant-scoped
	// connection, which is the property that actually matters: RLS, not a
	// predicate the store happened to add.
	var count int
	if err := scoped.QueryRow(ctxB,
		`SELECT count(*) FROM asset_change_history WHERE asset_id = $1`, createdA.AssetID).Scan(&count); err != nil {
		t.Fatalf("direct query as tenant b: %v", err)
	}
	if count != 0 {
		t.Errorf("tenant B's own connection saw %d of tenant A's history row(s) directly", count)
	}

	// Tenant A sees its own history just fine.
	histAsA, err := store.History(ctxA, createdA.AssetID, 0, "")
	if err != nil {
		t.Fatalf("history as tenant a: %v", err)
	}
	if len(histAsA) == 0 {
		t.Error("tenant A cannot see its own asset's history")
	}
}
