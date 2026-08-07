//go:build integration

package postgres

import (
	"errors"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

// riskAsset creates a real asset on the given tenant-scoped store, for a risk
// test to link a risk against — mirrors vulnFindingAsset's fixture shape
// exactly.
func riskAsset(t *testing.T, assets *AssetStore, tn *domain.Tenant, name string) *domain.Asset {
	t.Helper()
	ctx := assetTestCtx(tn)
	a, err := domain.NewAsset(tn.TenantID, "server", name, nil)
	if err != nil {
		t.Fatalf("new asset: %v", err)
	}
	created, err := assets.Create(ctx, a)
	if err != nil {
		t.Fatalf("create asset: %v", err)
	}
	return created
}

// TestRiskStore_CreateGetList proves the basic CRUD path: a freshly created
// risk is open, server-minted, and appears in the caller's own List.
func TestRiskStore_CreateGetList(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "risk-crud")

	scoped := tenantScopedPool(t)
	store := NewRiskStore(scoped)
	ctx := assetTestCtx(tn)

	r, err := domain.NewRisk(tn.TenantID, "Single vendor dependency", "we rely on one supplier",
		"operational", domain.RiskLikelihoodPossible, domain.RiskImpactMajor, nil, nil)
	if err != nil {
		t.Fatalf("new risk: %v", err)
	}
	created, err := store.Create(ctx, r)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.RiskID == "" {
		t.Fatal("risk_id must be server-minted")
	}
	if created.Status != domain.RiskOpen {
		t.Errorf("Status = %q, want open", created.Status)
	}
	if created.RowVersion != 1 {
		t.Errorf("row_version = %d, want 1", created.RowVersion)
	}

	got, err := store.Get(ctx, created.RiskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "Single vendor dependency" || got.Category != "operational" {
		t.Errorf("get returned %+v, want the created fields", got)
	}

	list, err := store.List(ctx, 0, "", "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].RiskID != created.RiskID {
		t.Errorf("list = %+v, want exactly the created risk", list)
	}
}

// TestRiskStore_CreateRejectsCrossTenantOrNonexistentAsset mirrors
// TestVulnFindingStore_UpsertRejectsCrossTenantOrNonexistentAsset.
func TestRiskStore_CreateRejectsCrossTenantOrNonexistentAsset(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a, err := tenants.Create(adminTestCtx(), newTenant("risk-rej-alpha", "ext-risk-rej-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(adminTestCtx(), newTenant("risk-rej-bravo", "ext-risk-rej-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	store := NewRiskStore(scoped)
	ctxB := assetTestCtx(b)

	victim := riskAsset(t, assets, a, "victim-host")
	victimID := victim.AssetID

	crossTenant, err := domain.NewRisk(b.TenantID, "x", "", "", domain.RiskLikelihoodRare, domain.RiskImpactMinor, nil, &victimID)
	if err != nil {
		t.Fatalf("new risk: %v", err)
	}
	if _, err := store.Create(ctxB, crossTenant); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("create with cross-tenant asset_id err = %v, want ErrNotFound", err)
	}

	unknown := "no-such-asset"
	badAsset, err := domain.NewRisk(b.TenantID, "y", "", "", domain.RiskLikelihoodRare, domain.RiskImpactMinor, nil, &unknown)
	if err != nil {
		t.Fatalf("new risk: %v", err)
	}
	if _, err := store.Create(ctxB, badAsset); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("create with nonexistent asset_id err = %v, want ErrNotFound", err)
	}
}

// TestRiskStore_UpdateFields proves an ordinary field edit under optimistic
// locking, including clearing category/treatment/asset_id via the tri-state
// "" rule.
func TestRiskStore_UpdateFields(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "risk-update")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	store := NewRiskStore(scoped)
	ctx := assetTestCtx(tn)

	host := riskAsset(t, assets, tn, "host-1")
	treatment := domain.RiskTreatmentMitigate
	r, err := domain.NewRisk(tn.TenantID, "Unpatched host", "", "security",
		domain.RiskLikelihoodLikely, domain.RiskImpactSevere, &treatment, &host.AssetID)
	if err != nil {
		t.Fatalf("new risk: %v", err)
	}
	created, err := store.Create(ctx, r)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	newTitle := "Unpatched host (critical)"
	newImpact := domain.RiskImpactModerate
	updated, err := store.Update(ctx, created.RiskID, 1, domain.RiskPatch{
		Title: &newTitle, Impact: &newImpact,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Title != newTitle || updated.Impact != newImpact {
		t.Errorf("updated = %+v, want title/impact changed", updated)
	}
	if updated.Category != "security" || updated.Treatment == nil || *updated.Treatment != domain.RiskTreatmentMitigate {
		t.Errorf("untouched fields must survive: %+v", updated)
	}
	if updated.RowVersion != 2 {
		t.Errorf("row_version = %d, want 2", updated.RowVersion)
	}

	// Clear category, treatment and asset_id in one call via the tri-state
	// "" rule.
	empty := ""
	cleared, err := store.Update(ctx, created.RiskID, 2, domain.RiskPatch{
		Category: &empty, Treatment: &empty, AssetID: &empty,
	})
	if err != nil {
		t.Fatalf("clear update: %v", err)
	}
	if cleared.Category != "" || cleared.Treatment != nil || cleared.AssetID != nil {
		t.Errorf("cleared = %+v, want category/treatment/asset_id all cleared", cleared)
	}

	// Stale row_version is refused.
	if _, err := store.Update(ctx, created.RiskID, 2, domain.RiskPatch{Title: &newTitle}); err != domain.ErrVersionMismatch {
		t.Errorf("stale row_version err = %v, want ErrVersionMismatch", err)
	}

	// Unknown risk_id.
	if _, err := store.Update(ctx, "no-such-risk", 1, domain.RiskPatch{Title: &newTitle}); err != domain.ErrNotFound {
		t.Errorf("unknown risk_id err = %v, want ErrNotFound", err)
	}
}

// TestRiskStore_UpdateRevalidatesTheWholeEntity proves Category's format
// rule is enforced even though it is applied via a merge-then-Validate
// shape, not a store-local pattern check.
func TestRiskStore_UpdateRevalidatesTheWholeEntity(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "risk-revalidate")

	scoped := tenantScopedPool(t)
	store := NewRiskStore(scoped)
	ctx := assetTestCtx(tn)

	r, err := domain.NewRisk(tn.TenantID, "x", "", "", domain.RiskLikelihoodRare, domain.RiskImpactMinor, nil, nil)
	if err != nil {
		t.Fatalf("new risk: %v", err)
	}
	created, err := store.Create(ctx, r)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	bad := "Not Snake Case"
	if _, err := store.Update(ctx, created.RiskID, 1, domain.RiskPatch{Category: &bad}); err == nil {
		t.Fatal("update with an invalid category must fail")
	} else {
		ve, ok := domain.AsValidation(err)
		if !ok || ve.Field != "category" {
			t.Errorf("err = %v, want a category ValidationError", err)
		}
	}
}

// TestRiskStore_SetStatusTransitions proves the legal edges write through
// the store and illegal edges are refused with the transition error, and
// that a stale row_version is refused with ErrVersionMismatch.
func TestRiskStore_SetStatusTransitions(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "risk-transitions")

	scoped := tenantScopedPool(t)
	store := NewRiskStore(scoped)
	ctx := assetTestCtx(tn)

	r, err := domain.NewRisk(tn.TenantID, "x", "", "", domain.RiskLikelihoodPossible, domain.RiskImpactMinor, nil, nil)
	if err != nil {
		t.Fatalf("new risk: %v", err)
	}
	created, err := store.Create(ctx, r)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id := created.RiskID

	// Illegal: open -> open is a self-transition, refused.
	if _, err := store.SetStatus(ctx, id, 1, domain.RiskOpen); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("open->open err = %v, want ErrInvalidTransition", err)
	}

	// Legal: open -> mitigating.
	updated, err := store.SetStatus(ctx, id, 1, domain.RiskMitigating)
	if err != nil {
		t.Fatalf("open->mitigating: %v", err)
	}
	if updated.RowVersion != 2 {
		t.Errorf("row_version = %d, want 2", updated.RowVersion)
	}

	// Illegal: mitigating -> mitigating.
	if _, err := store.SetStatus(ctx, id, 2, domain.RiskMitigating); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("mitigating->mitigating err = %v, want ErrInvalidTransition", err)
	}

	// Legal: mitigating -> accepted -> closed -> open (reopened).
	if _, err := store.SetStatus(ctx, id, 2, domain.RiskAccepted); err != nil {
		t.Fatalf("mitigating->accepted: %v", err)
	}
	if _, err := store.SetStatus(ctx, id, 3, domain.RiskClosed); err != nil {
		t.Fatalf("accepted->closed: %v", err)
	}
	reopened, err := store.SetStatus(ctx, id, 4, domain.RiskOpen)
	if err != nil {
		t.Fatalf("closed->open (reopen): %v", err)
	}
	if reopened.Status != domain.RiskOpen {
		t.Errorf("Status = %q, want open", reopened.Status)
	}

	// Stale row_version.
	if _, err := store.SetStatus(ctx, id, 2, domain.RiskClosed); err != domain.ErrVersionMismatch {
		t.Errorf("stale row_version err = %v, want ErrVersionMismatch", err)
	}

	// Not found.
	if _, err := store.SetStatus(ctx, "no-such-risk", 1, domain.RiskOpen); err != domain.ErrNotFound {
		t.Errorf("unknown risk_id err = %v, want ErrNotFound", err)
	}
}

// TestRiskStore_ListFilters proves the status/category filters each narrow
// the page independently.
func TestRiskStore_ListFilters(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "risk-listfilter")

	scoped := tenantScopedPool(t)
	store := NewRiskStore(scoped)
	ctx := assetTestCtx(tn)

	mk := func(title, category string) *domain.Risk {
		r, err := domain.NewRisk(tn.TenantID, title, "", category,
			domain.RiskLikelihoodPossible, domain.RiskImpactModerate, nil, nil)
		if err != nil {
			t.Fatalf("new risk: %v", err)
		}
		created, err := store.Create(ctx, r)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		return created
	}

	opsRisk := mk("ops risk", "operational")
	mk("compliance risk", "compliance")
	mk("another ops risk", "operational")

	if _, err := store.SetStatus(ctx, opsRisk.RiskID, 1, domain.RiskMitigating); err != nil {
		t.Fatalf("set status: %v", err)
	}

	byCategory, err := store.List(ctx, 0, "", "", "operational")
	if err != nil {
		t.Fatalf("list by category: %v", err)
	}
	if len(byCategory) != 2 {
		t.Errorf("list by category=operational = %d, want 2", len(byCategory))
	}

	byStatus, err := store.List(ctx, 0, "", domain.RiskMitigating, "")
	if err != nil {
		t.Fatalf("list by status: %v", err)
	}
	if len(byStatus) != 1 || byStatus[0].RiskID != opsRisk.RiskID {
		t.Errorf("list by status=mitigating = %+v, want exactly the mitigating risk", byStatus)
	}
}

// TestRiskStore_Register proves the scoring projection: OPEN (non-closed)
// risks ranked by likelihoodRank * impactRank, ties broken by updated_at
// DESC then risk_id, and that a closed risk is excluded.
func TestRiskStore_Register(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "risk-register")

	scoped := tenantScopedPool(t)
	store := NewRiskStore(scoped)
	ctx := assetTestCtx(tn)

	mk := func(title string, l domain.RiskLikelihood, i domain.RiskImpact) *domain.Risk {
		r, err := domain.NewRisk(tn.TenantID, title, "", "", l, i, nil, nil)
		if err != nil {
			t.Fatalf("new risk: %v", err)
		}
		created, err := store.Create(ctx, r)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		return created
	}

	// score 25 (almost_certain x severe).
	highest := mk("critical", domain.RiskLikelihoodAlmostCertain, domain.RiskImpactSevere)
	// score 1 (rare x negligible).
	lowest := mk("trivial", domain.RiskLikelihoodRare, domain.RiskImpactNegligible)
	// score 6 (possible x minor... 3*2=6), used as a mid entry that gets closed.
	toClose := mk("will be closed", domain.RiskLikelihoodPossible, domain.RiskImpactMinor)

	if _, err := store.SetStatus(ctx, toClose.RiskID, 1, domain.RiskClosed); err != nil {
		t.Fatalf("close: %v", err)
	}

	entries, err := store.Register(ctx, 0)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("register = %d entries, want 2 (closed risk excluded): %+v", len(entries), entries)
	}
	if entries[0].Risk.RiskID != highest.RiskID {
		t.Errorf("entries[0] = %+v, want the highest-scored risk first", entries[0])
	}
	if entries[0].Score != 25 || entries[0].Band != domain.RiskSeverityBandCritical {
		t.Errorf("entries[0] score/band = %d/%q, want 25/critical", entries[0].Score, entries[0].Band)
	}
	if entries[1].Risk.RiskID != lowest.RiskID {
		t.Errorf("entries[1] = %+v, want the lowest-scored risk last", entries[1])
	}
	if entries[1].Score != 1 || entries[1].Band != domain.RiskSeverityBandLow {
		t.Errorf("entries[1] score/band = %d/%q, want 1/low", entries[1].Score, entries[1].Band)
	}
}

// TestRiskIsolation_RLSByTenant is the security gate for this story: tenant
// B can never see, list, patch or transition tenant A's risks, and the
// register never shows another tenant's risks — mirrors
// TestVulnFindingIsolation_RLSByTenant.
func TestRiskIsolation_RLSByTenant(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a, err := tenants.Create(adminTestCtx(), newTenant("risk-iso-alpha", "ext-risk-iso-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(adminTestCtx(), newTenant("risk-iso-bravo", "ext-risk-iso-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	scoped := tenantScopedPool(t)
	store := NewRiskStore(scoped)
	ctxA := assetTestCtx(a)
	ctxB := assetTestCtx(b)

	rA, err := domain.NewRisk(a.TenantID, "alpha's risk", "", "",
		domain.RiskLikelihoodAlmostCertain, domain.RiskImpactSevere, nil, nil)
	if err != nil {
		t.Fatalf("new risk a: %v", err)
	}
	createdA, err := store.Create(ctxA, rA)
	if err != nil {
		t.Fatalf("create as tenant a: %v", err)
	}

	rB, err := domain.NewRisk(b.TenantID, "bravo's risk", "", "",
		domain.RiskLikelihoodRare, domain.RiskImpactMinor, nil, nil)
	if err != nil {
		t.Fatalf("new risk b: %v", err)
	}
	createdB, err := store.Create(ctxB, rB)
	if err != nil {
		t.Fatalf("create as tenant b: %v", err)
	}

	// READ: tenant B cannot get tenant A's risk by id, and does not see it
	// in its own list.
	if _, err := store.Get(ctxB, createdA.RiskID); err != domain.ErrNotFound {
		t.Errorf("tenant B read tenant A's risk: err = %v, want ErrNotFound", err)
	}
	listB, err := store.List(ctxB, 0, "", "", "")
	if err != nil {
		t.Fatalf("list as tenant b: %v", err)
	}
	if len(listB) != 1 || listB[0].RiskID != createdB.RiskID {
		t.Errorf("tenant B's list = %+v, want exactly its own risk", listB)
	}

	// WRITE: tenant B cannot patch or transition tenant A's risk, even with
	// the correct row_version.
	newTitle := "hijacked"
	if _, err := store.Update(ctxB, createdA.RiskID, 1, domain.RiskPatch{Title: &newTitle}); err != domain.ErrNotFound {
		t.Errorf("tenant B updated tenant A's risk: err = %v, want ErrNotFound", err)
	}
	if _, err := store.SetStatus(ctxB, createdA.RiskID, 1, domain.RiskClosed); err != domain.ErrNotFound {
		t.Errorf("tenant B transitioned tenant A's risk: err = %v, want ErrNotFound", err)
	}

	// REGISTER: tenant B's register never shows tenant A's (much higher
	// scored) risk — the projection is RLS-scoped exactly like Get/List.
	registerB, err := store.Register(ctxB, 0)
	if err != nil {
		t.Fatalf("register as tenant b: %v", err)
	}
	if len(registerB) != 1 || registerB[0].Risk.RiskID != createdB.RiskID {
		t.Errorf("tenant B's register = %+v, want exactly its own risk", registerB)
	}

	// Tenant A still sees its own, undisturbed risk, at its original
	// row_version.
	stillThere, err := store.Get(ctxA, createdA.RiskID)
	if err != nil {
		t.Fatalf("tenant A lost its own risk: %v", err)
	}
	if stillThere.RowVersion != 1 || stillThere.Title != "alpha's risk" {
		t.Errorf("tenant A's risk was mutated by tenant B's attempt: %+v", stillThere)
	}
	registerA, err := store.Register(ctxA, 0)
	if err != nil {
		t.Fatalf("register as tenant a: %v", err)
	}
	if len(registerA) != 1 || registerA[0].Risk.RiskID != createdA.RiskID {
		t.Errorf("tenant A's register = %+v, want exactly its own risk", registerA)
	}
}
