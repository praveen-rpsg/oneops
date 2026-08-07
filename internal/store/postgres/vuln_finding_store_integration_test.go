//go:build integration

package postgres

import (
	"errors"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

// vulnFindingAsset creates a real asset on the given tenant-scoped store, for
// a vuln finding test to point findings at — mirrors securityObservationAsset's
// fixture shape exactly.
func vulnFindingAsset(t *testing.T, assets *AssetStore, tn *domain.Tenant, name string) *domain.Asset {
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

// TestVulnFindingStore_IngestNewFindingCreatesOpen proves the fresh-insert
// branch: no existing row -> a new open finding, first_seen == last_seen.
func TestVulnFindingStore_IngestNewFindingCreatesOpen(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "vuln-fresh")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	store := NewVulnFindingStore(scoped)
	ctx := assetTestCtx(tn)

	host := vulnFindingAsset(t, assets, tn, "host-1")

	f, err := domain.NewVulnFinding(tn.TenantID, host.AssetID, "CVE-2024-1234", "OpenSSL buffer overflow",
		domain.VulnFindingSeverityHigh, "nessus", "found on port 443")
	if err != nil {
		t.Fatalf("new vuln finding: %v", err)
	}

	results, err := store.Upsert(ctx, []domain.VulnFinding{*f})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if len(results) != 1 || !results[0].Accepted {
		t.Fatalf("upsert results = %+v, want one accepted result", results)
	}
	if results[0].Status != domain.VulnFindingOpen {
		t.Errorf("Status = %q, want open", results[0].Status)
	}

	got, err := store.Get(ctx, results[0].FindingID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != domain.VulnFindingOpen {
		t.Errorf("got.Status = %q, want open", got.Status)
	}
	if got.RowVersion != 1 {
		t.Errorf("row_version = %d, want 1", got.RowVersion)
	}
	if got.FirstSeen.IsZero() || got.LastSeen.IsZero() {
		t.Fatal("first_seen/last_seen must be store-set, not zero")
	}
	if !got.FirstSeen.Equal(got.LastSeen) {
		t.Errorf("a freshly created finding must have first_seen == last_seen: %+v", got)
	}
	if got.Title != "OpenSSL buffer overflow" || got.Severity != domain.VulnFindingSeverityHigh || got.Scanner != "nessus" {
		t.Errorf("get returned %+v, want the ingested fields", got)
	}
}

// TestVulnFindingStore_IdempotentReingestDoesNotDuplicate proves re-ingesting
// the identical scan a second time reproduces one row, not two — the
// UNIQUE(tenant_id, asset_id, vuln_id) + ON CONFLICT dedup contract.
func TestVulnFindingStore_IdempotentReingestDoesNotDuplicate(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "vuln-idem")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	store := NewVulnFindingStore(scoped)
	ctx := assetTestCtx(tn)

	host := vulnFindingAsset(t, assets, tn, "host-1")
	f, err := domain.NewVulnFinding(tn.TenantID, host.AssetID, "CVE-2024-9999", "Sample finding",
		domain.VulnFindingSeverityMedium, "trivy", "")
	if err != nil {
		t.Fatalf("new vuln finding: %v", err)
	}

	first, err := store.Upsert(ctx, []domain.VulnFinding{*f})
	if err != nil {
		t.Fatalf("upsert 1: %v", err)
	}
	findingID := first[0].FindingID

	// Re-send the identical scan three more times.
	for i := 0; i < 3; i++ {
		f2, err := domain.NewVulnFinding(tn.TenantID, host.AssetID, "CVE-2024-9999", "Sample finding",
			domain.VulnFindingSeverityMedium, "trivy", "")
		if err != nil {
			t.Fatalf("new vuln finding (reingest %d): %v", i, err)
		}
		again, err := store.Upsert(ctx, []domain.VulnFinding{*f2})
		if err != nil {
			t.Fatalf("upsert reingest %d: %v", i, err)
		}
		if !again[0].Accepted || again[0].FindingID != findingID {
			t.Fatalf("reingest %d = %+v, want the same finding_id %q accepted again", i, again[0], findingID)
		}
	}

	list, err := store.List(ctx, 0, "", host.AssetID, "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("re-ingesting the identical scan 4 times produced %d rows, want 1: %+v", len(list), list)
	}
	if list[0].RowVersion != 4 {
		t.Errorf("row_version = %d, want 4 (one INSERT + 3 UPDATEs)", list[0].RowVersion)
	}
}

// TestVulnFindingStore_RemediatedReappearsAsOpen proves the ingestion UPSERT
// rule's central case: a remediated finding that reappears in a later scan
// is automatically reopened — the remediation did not hold.
func TestVulnFindingStore_RemediatedReappearsAsOpen(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "vuln-remreopen")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	store := NewVulnFindingStore(scoped)
	ctx := assetTestCtx(tn)

	host := vulnFindingAsset(t, assets, tn, "host-1")
	f, err := domain.NewVulnFinding(tn.TenantID, host.AssetID, "CVE-2024-5555", "Some finding",
		domain.VulnFindingSeverityLow, "qualys", "")
	if err != nil {
		t.Fatalf("new vuln finding: %v", err)
	}
	first, err := store.Upsert(ctx, []domain.VulnFinding{*f})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	findingID := first[0].FindingID

	remediated, err := store.SetStatus(ctx, findingID, 1, domain.VulnFindingRemediated)
	if err != nil {
		t.Fatalf("set status remediated: %v", err)
	}
	if remediated.Status != domain.VulnFindingRemediated {
		t.Fatalf("status = %q, want remediated", remediated.Status)
	}
	origFirstSeen := remediated.FirstSeen

	// A later scan re-reports the SAME vuln on the SAME asset.
	f2, err := domain.NewVulnFinding(tn.TenantID, host.AssetID, "CVE-2024-5555", "Some finding (updated title)",
		domain.VulnFindingSeverityHigh, "qualys", "still present")
	if err != nil {
		t.Fatalf("new vuln finding 2: %v", err)
	}
	reingested, err := store.Upsert(ctx, []domain.VulnFinding{*f2})
	if err != nil {
		t.Fatalf("upsert reingest: %v", err)
	}
	if !reingested[0].Accepted || reingested[0].FindingID != findingID {
		t.Fatalf("reingest = %+v, want the same finding_id %q accepted", reingested[0], findingID)
	}
	if reingested[0].Status != domain.VulnFindingOpen {
		t.Fatalf("a remediated finding that reappears must transition to open; Status = %q", reingested[0].Status)
	}

	got, err := store.Get(ctx, findingID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != domain.VulnFindingOpen {
		t.Errorf("Status = %q, want open", got.Status)
	}
	if got.Title != "Some finding (updated title)" || got.Severity != domain.VulnFindingSeverityHigh {
		t.Errorf("reappearance must refresh title/severity from the scan: %+v", got)
	}
	if !got.FirstSeen.Equal(origFirstSeen) {
		t.Errorf("first_seen must survive a reappearance unchanged: got %v, want %v", got.FirstSeen, origFirstSeen)
	}
	if !got.LastSeen.After(origFirstSeen) && !got.LastSeen.Equal(origFirstSeen) {
		t.Errorf("last_seen must move forward on reappearance: %+v", got)
	}
}

// TestVulnFindingStore_AcceptedRiskReappearsAsOpen mirrors the remediated
// case for accepted_risk — an accepted risk reappearing is a fresh fact the
// operator must re-triage, not a standing suppression.
func TestVulnFindingStore_AcceptedRiskReappearsAsOpen(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "vuln-arreopen")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	store := NewVulnFindingStore(scoped)
	ctx := assetTestCtx(tn)

	host := vulnFindingAsset(t, assets, tn, "host-1")
	f, err := domain.NewVulnFinding(tn.TenantID, host.AssetID, "CVE-2024-7777", "Accepted risk finding",
		domain.VulnFindingSeverityMedium, "nessus", "")
	if err != nil {
		t.Fatalf("new vuln finding: %v", err)
	}
	first, err := store.Upsert(ctx, []domain.VulnFinding{*f})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	findingID := first[0].FindingID

	if _, err := store.SetStatus(ctx, findingID, 1, domain.VulnFindingAcceptedRisk); err != nil {
		t.Fatalf("set status accepted_risk: %v", err)
	}

	f2, err := domain.NewVulnFinding(tn.TenantID, host.AssetID, "CVE-2024-7777", "Accepted risk finding",
		domain.VulnFindingSeverityMedium, "nessus", "")
	if err != nil {
		t.Fatalf("new vuln finding 2: %v", err)
	}
	reingested, err := store.Upsert(ctx, []domain.VulnFinding{*f2})
	if err != nil {
		t.Fatalf("upsert reingest: %v", err)
	}
	if reingested[0].Status != domain.VulnFindingOpen {
		t.Fatalf("an accepted_risk finding that reappears must transition to open; Status = %q", reingested[0].Status)
	}
}

// TestVulnFindingStore_FalsePositiveIsNotReopenedByIngestion is the
// story's central "MUST NOT" proof: a scan re-reporting the same alarm must
// NOT overturn an operator's false_positive determination, and must not
// clobber the fields the operator evaluated it against — only last_seen
// moves.
func TestVulnFindingStore_FalsePositiveIsNotReopenedByIngestion(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "vuln-fp")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	store := NewVulnFindingStore(scoped)
	ctx := assetTestCtx(tn)

	host := vulnFindingAsset(t, assets, tn, "host-1")
	f, err := domain.NewVulnFinding(tn.TenantID, host.AssetID, "CVE-2024-0001", "Original title",
		domain.VulnFindingSeverityCritical, "nessus", "original description")
	if err != nil {
		t.Fatalf("new vuln finding: %v", err)
	}
	first, err := store.Upsert(ctx, []domain.VulnFinding{*f})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	findingID := first[0].FindingID

	fp, err := store.SetStatus(ctx, findingID, 1, domain.VulnFindingFalsePositive)
	if err != nil {
		t.Fatalf("set status false_positive: %v", err)
	}
	origLastSeen := fp.LastSeen

	// A later scan re-reports the SAME vuln, with DIFFERENT title/severity/
	// scanner/description — an adversarial payload deliberately chosen to be
	// detectable if any of it leaks through.
	f2, err := domain.NewVulnFinding(tn.TenantID, host.AssetID, "CVE-2024-0001", "OVERWRITTEN TITLE",
		domain.VulnFindingSeverityLow, "different-scanner", "OVERWRITTEN DESCRIPTION")
	if err != nil {
		t.Fatalf("new vuln finding 2: %v", err)
	}
	reingested, err := store.Upsert(ctx, []domain.VulnFinding{*f2})
	if err != nil {
		t.Fatalf("upsert reingest: %v", err)
	}
	if !reingested[0].Accepted || reingested[0].FindingID != findingID {
		t.Fatalf("reingest = %+v, want the same finding_id %q accepted", reingested[0], findingID)
	}
	if reingested[0].Status != domain.VulnFindingFalsePositive {
		t.Fatalf("Upsert must NOT reopen a false_positive finding; Status = %q", reingested[0].Status)
	}

	got, err := store.Get(ctx, findingID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != domain.VulnFindingFalsePositive {
		t.Errorf("Status = %q, want false_positive (unchanged)", got.Status)
	}
	if got.Title != "Original title" {
		t.Errorf("Title = %q, want the operator's original (a scan must not overturn a false_positive judgment)", got.Title)
	}
	if got.Severity != domain.VulnFindingSeverityCritical {
		t.Errorf("Severity = %q, want the original critical", got.Severity)
	}
	if got.Scanner != "nessus" {
		t.Errorf("Scanner = %q, want the original nessus", got.Scanner)
	}
	if got.Description != "original description" {
		t.Errorf("Description = %q, want the original", got.Description)
	}
	if !got.LastSeen.After(origLastSeen) && !got.LastSeen.Equal(origLastSeen) {
		t.Errorf("last_seen must still move forward even on a false_positive row: got %v, want >= %v", got.LastSeen, origLastSeen)
	}

	// Only a manual SetStatus can move it back to open — and that path DOES
	// work, proving false_positive is not accidentally terminal.
	reopened, err := store.SetStatus(ctx, findingID, got.RowVersion, domain.VulnFindingOpen)
	if err != nil {
		t.Fatalf("manual re-triage to open: %v", err)
	}
	if reopened.Status != domain.VulnFindingOpen {
		t.Errorf("Status after manual re-triage = %q, want open", reopened.Status)
	}
}

// TestVulnFindingStore_UpsertRejectsCrossTenantOrNonexistentAsset mirrors
// TestSecurityObservationStore_RejectsCrossTenantOrNonexistentAsset.
func TestVulnFindingStore_UpsertRejectsCrossTenantOrNonexistentAsset(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a, err := tenants.Create(adminTestCtx(), newTenant("vuln-rej-alpha", "ext-vuln-rej-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(adminTestCtx(), newTenant("vuln-rej-bravo", "ext-vuln-rej-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	store := NewVulnFindingStore(scoped)
	ctxB := assetTestCtx(b)

	victim := vulnFindingAsset(t, assets, a, "victim-host")

	crossTenant, err := domain.NewVulnFinding(b.TenantID, victim.AssetID, "CVE-2024-1111", "x",
		domain.VulnFindingSeverityHigh, "nessus", "")
	if err != nil {
		t.Fatalf("new vuln finding: %v", err)
	}
	unknown, err := domain.NewVulnFinding(b.TenantID, "no-such-asset", "CVE-2024-2222", "x",
		domain.VulnFindingSeverityHigh, "nessus", "")
	if err != nil {
		t.Fatalf("new vuln finding: %v", err)
	}

	results, err := store.Upsert(ctxB, []domain.VulnFinding{*crossTenant, *unknown})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	for i, r := range results {
		if r.Accepted {
			t.Errorf("finding %d was accepted, want rejected: %+v", i, r)
		}
		if r.Reason == "" {
			t.Errorf("finding %d rejected with no reason", i)
		}
	}

	// No row was written naming the victim's asset — checked from tenant A's
	// own connection, where it would be visible if it existed.
	ctxA := assetTestCtx(a)
	listA, err := store.List(ctxA, 0, "", victim.AssetID, "", "")
	if err != nil {
		t.Fatalf("list as victim tenant: %v", err)
	}
	if len(listA) != 0 {
		t.Fatalf("a cross-tenant finding was written: %+v", listA)
	}
}

// TestVulnFindingStore_BatchOneBadFindingDoesNotAbortTheOthers proves the
// per-index independence contract: one invalid finding in a batch does not
// stop the well-formed ones from being written.
func TestVulnFindingStore_BatchOneBadFindingDoesNotAbortTheOthers(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "vuln-batch")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	store := NewVulnFindingStore(scoped)
	ctx := assetTestCtx(tn)

	host := vulnFindingAsset(t, assets, tn, "host-1")

	good, err := domain.NewVulnFinding(tn.TenantID, host.AssetID, "CVE-2024-3333", "Good finding",
		domain.VulnFindingSeverityHigh, "nessus", "")
	if err != nil {
		t.Fatalf("new vuln finding: %v", err)
	}
	// A finding with an invalid severity fails Validate inside Upsert itself
	// (not the constructor, so it can be queued as-is).
	bad := domain.VulnFinding{
		FindingID: domain.NewID(), TenantID: tn.TenantID, AssetID: host.AssetID,
		VulnID: "CVE-2024-4444", Title: "Bad finding", Severity: "bogus",
		Status: domain.VulnFindingOpen, Scanner: "nessus",
	}

	results, err := store.Upsert(ctx, []domain.VulnFinding{bad, *good})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if results[0].Accepted {
		t.Errorf("the bad finding was accepted: %+v", results[0])
	}
	if !results[1].Accepted {
		t.Errorf("the good finding was rejected: %+v", results[1])
	}
}

// TestVulnFindingStore_SetStatusTransitions proves the legal edges write
// through the store and illegal edges are refused with the transition error,
// and that a stale row_version is refused with ErrVersionMismatch.
func TestVulnFindingStore_SetStatusTransitions(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "vuln-transitions")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	store := NewVulnFindingStore(scoped)
	ctx := assetTestCtx(tn)

	host := vulnFindingAsset(t, assets, tn, "host-1")
	f, err := domain.NewVulnFinding(tn.TenantID, host.AssetID, "CVE-2024-8888", "x",
		domain.VulnFindingSeverityHigh, "nessus", "")
	if err != nil {
		t.Fatalf("new vuln finding: %v", err)
	}
	first, err := store.Upsert(ctx, []domain.VulnFinding{*f})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	id := first[0].FindingID

	// Illegal: open -> open is a self-transition, refused.
	if _, err := store.SetStatus(ctx, id, 1, domain.VulnFindingOpen); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("open->open err = %v, want ErrInvalidTransition", err)
	}

	// Legal: open -> accepted_risk.
	updated, err := store.SetStatus(ctx, id, 1, domain.VulnFindingAcceptedRisk)
	if err != nil {
		t.Fatalf("open->accepted_risk: %v", err)
	}
	if updated.RowVersion != 2 {
		t.Errorf("row_version = %d, want 2", updated.RowVersion)
	}

	// Illegal: accepted_risk -> false_positive is not a permitted edge (must
	// go back through open first).
	if _, err := store.SetStatus(ctx, id, 2, domain.VulnFindingFalsePositive); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("accepted_risk->false_positive err = %v, want ErrInvalidTransition", err)
	}

	// Stale row_version.
	if _, err := store.SetStatus(ctx, id, 1, domain.VulnFindingOpen); err != domain.ErrVersionMismatch {
		t.Errorf("stale row_version err = %v, want ErrVersionMismatch", err)
	}

	// Not found.
	if _, err := store.SetStatus(ctx, "no-such-finding", 1, domain.VulnFindingOpen); err != domain.ErrNotFound {
		t.Errorf("unknown finding_id err = %v, want ErrNotFound", err)
	}
}

// TestVulnFindingStore_ListFilters proves the asset/status/severity filters
// each narrow the page independently.
func TestVulnFindingStore_ListFilters(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "vuln-listfilter")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	store := NewVulnFindingStore(scoped)
	ctx := assetTestCtx(tn)

	hostA := vulnFindingAsset(t, assets, tn, "filter-host-a")
	hostB := vulnFindingAsset(t, assets, tn, "filter-host-b")

	mk := func(assetID, vulnID string, sev domain.VulnFindingSeverity) domain.VulnFinding {
		f, err := domain.NewVulnFinding(tn.TenantID, assetID, vulnID, "t", sev, "nessus", "")
		if err != nil {
			t.Fatalf("new vuln finding: %v", err)
		}
		return *f
	}

	results, err := store.Upsert(ctx, []domain.VulnFinding{
		mk(hostA.AssetID, "CVE-A-1", domain.VulnFindingSeverityHigh),
		mk(hostA.AssetID, "CVE-A-2", domain.VulnFindingSeverityLow),
		mk(hostB.AssetID, "CVE-B-1", domain.VulnFindingSeverityHigh),
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	for i, r := range results {
		if !r.Accepted {
			t.Fatalf("finding %d rejected: %+v", i, r)
		}
	}
	// Mark hostA's high finding remediated so the status filter has
	// something to narrow.
	if _, err := store.SetStatus(ctx, results[0].FindingID, 1, domain.VulnFindingRemediated); err != nil {
		t.Fatalf("set status: %v", err)
	}

	byAsset, err := store.List(ctx, 0, "", hostA.AssetID, "", "")
	if err != nil {
		t.Fatalf("list by asset: %v", err)
	}
	if len(byAsset) != 2 {
		t.Errorf("list by asset = %d, want 2", len(byAsset))
	}

	bySeverity, err := store.List(ctx, 0, "", "", "", domain.VulnFindingSeverityHigh)
	if err != nil {
		t.Fatalf("list by severity: %v", err)
	}
	if len(bySeverity) != 2 {
		t.Errorf("list by severity=high = %d, want 2", len(bySeverity))
	}

	byStatus, err := store.List(ctx, 0, "", "", domain.VulnFindingRemediated, "")
	if err != nil {
		t.Fatalf("list by status: %v", err)
	}
	if len(byStatus) != 1 || byStatus[0].FindingID != results[0].FindingID {
		t.Errorf("list by status=remediated = %+v, want exactly the remediated finding", byStatus)
	}
}

// TestVulnFindingIsolation_RLSByTenant is the security gate for this story:
// tenant B can never see, list or transition tenant A's findings, and the
// same (asset,vuln) pair is independent across tenants even when both
// tenants happen to name it identically — mirrors TestIOCIsolation_RLSByTenant
// and TestSecurityObservationIsolation_RLSByTenant.
func TestVulnFindingIsolation_RLSByTenant(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a, err := tenants.Create(adminTestCtx(), newTenant("vuln-iso-alpha", "ext-vuln-iso-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(adminTestCtx(), newTenant("vuln-iso-bravo", "ext-vuln-iso-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	store := NewVulnFindingStore(scoped)
	ctxA := assetTestCtx(a)
	ctxB := assetTestCtx(b)

	hostA := vulnFindingAsset(t, assets, a, "iso-host-a")
	hostB := vulnFindingAsset(t, assets, b, "iso-host-b")

	fA, err := domain.NewVulnFinding(a.TenantID, hostA.AssetID, "CVE-SHARED-1", "alpha's finding",
		domain.VulnFindingSeverityCritical, "nessus", "")
	if err != nil {
		t.Fatalf("new vuln finding a: %v", err)
	}
	resA, err := store.Upsert(ctxA, []domain.VulnFinding{*fA})
	if err != nil {
		t.Fatalf("upsert as tenant a: %v", err)
	}
	if !resA[0].Accepted {
		t.Fatalf("tenant a upsert rejected: %+v", resA[0])
	}

	// Tenant B independently reports the IDENTICAL vuln_id on its OWN asset —
	// this must succeed as a completely separate row, proving the dedup key
	// is tenant-scoped, not global.
	fB, err := domain.NewVulnFinding(b.TenantID, hostB.AssetID, "CVE-SHARED-1", "bravo's finding",
		domain.VulnFindingSeverityLow, "trivy", "")
	if err != nil {
		t.Fatalf("new vuln finding b: %v", err)
	}
	resB, err := store.Upsert(ctxB, []domain.VulnFinding{*fB})
	if err != nil {
		t.Fatalf("upsert as tenant b: %v", err)
	}
	if !resB[0].Accepted {
		t.Fatalf("tenant b upsert rejected: %+v", resB[0])
	}
	if resB[0].FindingID == resA[0].FindingID {
		t.Fatalf("tenant b's finding shares tenant a's finding_id: %+v vs %+v", resB[0], resA[0])
	}

	// READ: tenant B cannot get tenant A's finding by id, and does not see it
	// in its own list.
	if _, err := store.Get(ctxB, resA[0].FindingID); err != domain.ErrNotFound {
		t.Errorf("tenant B read tenant A's finding: err = %v, want ErrNotFound", err)
	}
	listB, err := store.List(ctxB, 0, "", "", "", "")
	if err != nil {
		t.Fatalf("list as tenant b: %v", err)
	}
	if len(listB) != 1 || listB[0].FindingID != resB[0].FindingID {
		t.Errorf("tenant B's list = %+v, want exactly its own finding", listB)
	}

	// WRITE: tenant B cannot transition tenant A's finding, even with the
	// correct row_version.
	if _, err := store.SetStatus(ctxB, resA[0].FindingID, 1, domain.VulnFindingRemediated); err != domain.ErrNotFound {
		t.Errorf("tenant B transitioned tenant A's finding: err = %v, want ErrNotFound", err)
	}

	// Tenant A still sees its own, undisturbed finding.
	stillThere, err := store.Get(ctxA, resA[0].FindingID)
	if err != nil {
		t.Fatalf("tenant A lost its own finding: %v", err)
	}
	if stillThere.Status != domain.VulnFindingOpen || stillThere.RowVersion != 1 {
		t.Errorf("tenant A's finding was mutated by tenant B's attempt: %+v", stillThere)
	}
	if stillThere.Title != "alpha's finding" {
		t.Errorf("tenant A's finding title changed: %+v", stillThere)
	}
}
