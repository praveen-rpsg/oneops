//go:build integration

package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// vulnFindingAssetWithCriticality creates a real asset, then sets its
// criticality — mirrors vulnFindingAsset's fixture shape, extended with the
// dimension Prioritized ranks against.
func vulnFindingAssetWithCriticality(
	t *testing.T, assets *AssetStore, tn *domain.Tenant, name string, crit domain.AssetCriticality,
) *domain.Asset {
	t.Helper()
	a := vulnFindingAsset(t, assets, tn, name)
	updated, err := assets.Update(assetTestCtx(tn), a.AssetID, a.RowVersion, domain.AssetPatch{Criticality: &crit})
	if err != nil {
		t.Fatalf("set asset criticality: %v", err)
	}
	return updated
}

// vulnFindingOpen upserts one open finding and returns its finding_id.
func vulnFindingOpen(
	t *testing.T, store *VulnFindingStore, ctx context.Context,
	tenantID, assetID, vulnID string, sev domain.VulnFindingSeverity,
) string {
	t.Helper()
	f, err := domain.NewVulnFinding(tenantID, assetID, vulnID, "t", sev, "nessus", "")
	if err != nil {
		t.Fatalf("new vuln finding: %v", err)
	}
	results, err := store.Upsert(ctx, []domain.VulnFinding{*f})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if !results[0].Accepted {
		t.Fatalf("upsert rejected: %+v", results[0])
	}
	return results[0].FindingID
}

// TestVulnFindingStore_Prioritized_RanksByScoreDescending is the primary
// ranking proof: five findings spanning the full severity x criticality
// grid rank strictly by score = severityRank * criticalityRank, and a
// REMEDIATED (non-open) finding never appears at all.
func TestVulnFindingStore_Prioritized_RanksByScoreDescending(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "vuln-prio-rank")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	store := NewVulnFindingStore(scoped)
	ctx := assetTestCtx(tn)

	critAsset := vulnFindingAssetWithCriticality(t, assets, tn, "crit-asset", domain.AssetCriticalityCritical)
	highAsset := vulnFindingAssetWithCriticality(t, assets, tn, "high-asset", domain.AssetCriticalityHigh)
	medAsset := vulnFindingAssetWithCriticality(t, assets, tn, "med-asset", domain.AssetCriticalityMedium)
	lowAsset := vulnFindingAssetWithCriticality(t, assets, tn, "low-asset", domain.AssetCriticalityLow)
	unknownAsset := vulnFindingAsset(t, assets, tn, "unknown-asset") // default: unknown

	// score = severityRank * criticalityRank: 25, 16, 9, 4, 1.
	idScore25 := vulnFindingOpen(t, store, ctx, tn.TenantID, critAsset.AssetID, "CVE-SCORE-25", domain.VulnFindingSeverityCritical)
	idScore16 := vulnFindingOpen(t, store, ctx, tn.TenantID, highAsset.AssetID, "CVE-SCORE-16", domain.VulnFindingSeverityHigh)
	idScore9 := vulnFindingOpen(t, store, ctx, tn.TenantID, medAsset.AssetID, "CVE-SCORE-9", domain.VulnFindingSeverityMedium)
	idScore4 := vulnFindingOpen(t, store, ctx, tn.TenantID, lowAsset.AssetID, "CVE-SCORE-4", domain.VulnFindingSeverityLow)
	idScore1 := vulnFindingOpen(t, store, ctx, tn.TenantID, unknownAsset.AssetID, "CVE-SCORE-1", domain.VulnFindingSeverityNone)

	// A remediated finding, however severe, must never appear: Prioritized
	// ranks OPEN findings only.
	remediatedID := vulnFindingOpen(t, store, ctx, tn.TenantID, critAsset.AssetID, "CVE-REMEDIATED", domain.VulnFindingSeverityCritical)
	if _, err := store.SetStatus(ctx, remediatedID, 1, domain.VulnFindingRemediated); err != nil {
		t.Fatalf("set status remediated: %v", err)
	}

	got, err := store.Prioritized(ctx, 0)
	if err != nil {
		t.Fatalf("prioritized: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("prioritized returned %d rows, want 5 (the remediated finding must be excluded): %+v", len(got), got)
	}

	wantOrder := []string{idScore25, idScore16, idScore9, idScore4, idScore1}
	wantScores := []int{25, 16, 9, 4, 1}
	for i, row := range got {
		if row.Finding.FindingID != wantOrder[i] {
			t.Errorf("position %d: finding_id = %q, want %q (score-descending order)", i, row.Finding.FindingID, wantOrder[i])
		}
		if row.Score != wantScores[i] {
			t.Errorf("position %d: score = %d, want %d", i, row.Score, wantScores[i])
		}
	}
	if got[0].Priority != domain.VulnFindingPriorityCritical {
		t.Errorf("top row priority = %q, want critical", got[0].Priority)
	}
	if got[len(got)-1].Priority != domain.VulnFindingPriorityLow {
		t.Errorf("bottom row priority = %q, want low", got[len(got)-1].Priority)
	}
	if got[4].Criticality != domain.AssetCriticalityUnknown {
		t.Errorf("bottom row criticality = %q, want unknown", got[4].Criticality)
	}
}

// TestVulnFindingStore_Prioritized_UnknownCriticalityNeverOutranksKnownLow is
// the story's explicit qualitative requirement, proven against the REAL SQL
// ranking (not just the Go-side VulnFindingPriorityOf unit test): at the
// SAME severity, a finding on an unclassified asset must rank BELOW the
// identical finding on a known low-criticality asset.
func TestVulnFindingStore_Prioritized_UnknownCriticalityNeverOutranksKnownLow(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "vuln-prio-unknown")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	store := NewVulnFindingStore(scoped)
	ctx := assetTestCtx(tn)

	unknownAsset := vulnFindingAsset(t, assets, tn, "untiered-asset")
	lowAsset := vulnFindingAssetWithCriticality(t, assets, tn, "known-low-asset", domain.AssetCriticalityLow)

	unknownFindingID := vulnFindingOpen(t, store, ctx, tn.TenantID, unknownAsset.AssetID, "CVE-UNKNOWN", domain.VulnFindingSeverityMedium)
	lowFindingID := vulnFindingOpen(t, store, ctx, tn.TenantID, lowAsset.AssetID, "CVE-KNOWNLOW", domain.VulnFindingSeverityMedium)

	got, err := store.Prioritized(ctx, 0)
	if err != nil {
		t.Fatalf("prioritized: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("prioritized returned %d rows, want 2: %+v", len(got), got)
	}
	if got[0].Finding.FindingID != lowFindingID || got[1].Finding.FindingID != unknownFindingID {
		t.Fatalf("order = [%s, %s], want the known-low finding (%s) ranked ABOVE the untiered one (%s)",
			got[0].Finding.FindingID, got[1].Finding.FindingID, lowFindingID, unknownFindingID)
	}
	if got[0].Score <= got[1].Score {
		t.Errorf("known-low score %d must exceed untiered score %d at the same severity", got[0].Score, got[1].Score)
	}
}

// TestVulnFindingStore_Prioritized_TieBreaksByLastSeenThenFindingID proves
// the deterministic tie-break: equal score settles on last_seen DESC first,
// then finding_id ascending when even last_seen ties.
func TestVulnFindingStore_Prioritized_TieBreaksByLastSeenThenFindingID(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "vuln-prio-tiebreak")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	store := NewVulnFindingStore(scoped)
	ctx := assetTestCtx(tn)

	host := vulnFindingAssetWithCriticality(t, assets, tn, "tie-asset", domain.AssetCriticalityMedium)

	// Equal score (medium severity x medium criticality = 9 for both), but
	// distinct last_seen: the SECOND finding, ingested after a small delay,
	// must rank first.
	earlyID := vulnFindingOpen(t, store, ctx, tn.TenantID, host.AssetID, "CVE-EARLY", domain.VulnFindingSeverityMedium)
	time.Sleep(20 * time.Millisecond)
	lateID := vulnFindingOpen(t, store, ctx, tn.TenantID, host.AssetID, "CVE-LATE", domain.VulnFindingSeverityMedium)

	got, err := store.Prioritized(ctx, 0)
	if err != nil {
		t.Fatalf("prioritized: %v", err)
	}
	if len(got) != 2 || got[0].Score != got[1].Score {
		t.Fatalf("expected 2 equal-score rows, got %+v", got)
	}
	if got[0].Finding.FindingID != lateID || got[1].Finding.FindingID != earlyID {
		t.Fatalf("order = [%s, %s], want the more-recently-seen finding (%s) first", got[0].Finding.FindingID, got[1].Finding.FindingID, lateID)
	}

	// Force IDENTICAL last_seen via the privileged pool, so the tie falls
	// through to the final, total tie-break: finding_id ascending.
	if _, err := priv.Exec(context.Background(),
		`UPDATE vuln_finding SET last_seen = now() WHERE finding_id = ANY($1)`,
		[]string{earlyID, lateID}); err != nil {
		t.Fatalf("force equal last_seen: %v", err)
	}
	wantFirst, wantSecond := earlyID, lateID
	if lateID < earlyID {
		wantFirst, wantSecond = lateID, earlyID
	}

	got2, err := store.Prioritized(ctx, 0)
	if err != nil {
		t.Fatalf("prioritized (2nd): %v", err)
	}
	if len(got2) != 2 {
		t.Fatalf("expected 2 rows, got %+v", got2)
	}
	if got2[0].Finding.FindingID != wantFirst || got2[1].Finding.FindingID != wantSecond {
		t.Errorf("with last_seen tied, order = [%s, %s], want ascending finding_id [%s, %s]",
			got2[0].Finding.FindingID, got2[1].Finding.FindingID, wantFirst, wantSecond)
	}
}

// TestVulnFindingStore_Prioritized_TenantIsolation is the security gate for
// the projection: tenant B's prioritized view never shows tenant A's
// findings, and vice versa, even though neither query carries an explicit
// tenant_id predicate (RLS alone confines it).
func TestVulnFindingStore_Prioritized_TenantIsolation(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a, err := tenants.Create(adminTestCtx(), newTenant("vuln-prio-iso-alpha", "ext-vuln-prio-iso-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(adminTestCtx(), newTenant("vuln-prio-iso-bravo", "ext-vuln-prio-iso-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	store := NewVulnFindingStore(scoped)
	ctxA := assetTestCtx(a)
	ctxB := assetTestCtx(b)

	hostA := vulnFindingAssetWithCriticality(t, assets, a, "iso-host-a", domain.AssetCriticalityCritical)
	hostB := vulnFindingAssetWithCriticality(t, assets, b, "iso-host-b", domain.AssetCriticalityCritical)

	idA := vulnFindingOpen(t, store, ctxA, a.TenantID, hostA.AssetID, "CVE-ISO-A", domain.VulnFindingSeverityCritical)
	idB := vulnFindingOpen(t, store, ctxB, b.TenantID, hostB.AssetID, "CVE-ISO-B", domain.VulnFindingSeverityCritical)

	gotA, err := store.Prioritized(ctxA, 0)
	if err != nil {
		t.Fatalf("prioritized as tenant a: %v", err)
	}
	if len(gotA) != 1 || gotA[0].Finding.FindingID != idA {
		t.Errorf("tenant A's prioritized view = %+v, want exactly its own finding %q", gotA, idA)
	}

	gotB, err := store.Prioritized(ctxB, 0)
	if err != nil {
		t.Fatalf("prioritized as tenant b: %v", err)
	}
	if len(gotB) != 1 || gotB[0].Finding.FindingID != idB {
		t.Errorf("tenant B's prioritized view = %+v, want exactly its own finding %q", gotB, idB)
	}
}

// TestVulnFindingStore_Remediate_CreatesAndLinksIncident is the store-level
// happy path: Remediate opens a vuln-sourced Incident, links it via
// RemediationIncidentID, and bumps row_version exactly once.
func TestVulnFindingStore_Remediate_CreatesAndLinksIncident(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "vuln-remediate-create")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	store := NewVulnFindingStore(scoped)
	ctx := assetTestCtx(tn)

	host := vulnFindingAsset(t, assets, tn, "remediate-host")
	findingID := vulnFindingOpen(t, store, ctx, tn.TenantID, host.AssetID, "CVE-REMEDIATE-1", domain.VulnFindingSeverityHigh)

	finding, incident, err := store.Remediate(ctx, findingID, 1)
	if err != nil {
		t.Fatalf("remediate: %v", err)
	}
	if incident.Source != domain.IncidentSourceVuln {
		t.Errorf("incident.Source = %q, want vuln", incident.Source)
	}
	if incident.Status != domain.IncidentOpen {
		t.Errorf("incident.Status = %q, want open", incident.Status)
	}
	if incident.AssetID == nil || *incident.AssetID != host.AssetID {
		t.Errorf("incident.AssetID = %v, want %q", incident.AssetID, host.AssetID)
	}
	if finding.RemediationIncidentID == nil || *finding.RemediationIncidentID != incident.IncidentID {
		t.Errorf("finding.RemediationIncidentID = %v, want %q", finding.RemediationIncidentID, incident.IncidentID)
	}
	if finding.RowVersion != 2 {
		t.Errorf("finding.RowVersion = %d, want 2 (one link write)", finding.RowVersion)
	}

	// The incident's own timeline recorded exactly one "created" row.
	timeline, err := NewIncidentStore(scoped).Timeline(ctx, incident.IncidentID, 0, "")
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	created := 0
	for _, e := range timeline {
		if e.Kind == domain.IncidentEventCreated {
			created++
		}
	}
	if created != 1 {
		t.Errorf("created timeline rows = %d, want 1: %+v", created, timeline)
	}
}

// TestVulnFindingStore_Remediate_IsIdempotentOnOpenLink proves the SEQUENTIAL
// idempotency contract: calling Remediate again, with the row_version the
// first call returned, returns the SAME incident unchanged — no second
// incident, no further row_version movement.
func TestVulnFindingStore_Remediate_IsIdempotentOnOpenLink(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "vuln-remediate-idem")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	store := NewVulnFindingStore(scoped)
	ctx := assetTestCtx(tn)

	host := vulnFindingAsset(t, assets, tn, "idem-host")
	findingID := vulnFindingOpen(t, store, ctx, tn.TenantID, host.AssetID, "CVE-REMEDIATE-IDEM", domain.VulnFindingSeverityHigh)

	first, firstIncident, err := store.Remediate(ctx, findingID, 1)
	if err != nil {
		t.Fatalf("first remediate: %v", err)
	}

	second, secondIncident, err := store.Remediate(ctx, findingID, first.RowVersion)
	if err != nil {
		t.Fatalf("second remediate: %v", err)
	}
	if secondIncident.IncidentID != firstIncident.IncidentID {
		t.Fatalf("second remediate created a DIFFERENT incident: %q vs %q — must be idempotent", secondIncident.IncidentID, firstIncident.IncidentID)
	}
	if second.RowVersion != first.RowVersion {
		t.Errorf("row_version moved on an idempotent replay: %d -> %d", first.RowVersion, second.RowVersion)
	}

	// Exactly one vuln-sourced incident exists for this asset.
	rows, err := NewIncidentStore(scoped).List(ctx, 0, "", "")
	if err != nil {
		t.Fatalf("list incidents: %v", err)
	}
	count := 0
	for _, r := range rows {
		if r.AssetID != nil && *r.AssetID == host.AssetID && r.Source == domain.IncidentSourceVuln {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("vuln-sourced incidents on asset %s = %d, want exactly 1 (no duplicate)", host.AssetID, count)
	}
}

// TestVulnFindingStore_Remediate_OpensNewIncidentAfterPriorOneClosed proves
// Remediate does NOT reuse a CLOSED remediation incident: closing it and
// calling Remediate again opens a fresh one.
func TestVulnFindingStore_Remediate_OpensNewIncidentAfterPriorOneClosed(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "vuln-remediate-reopen")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	store := NewVulnFindingStore(scoped)
	incidents := NewIncidentStore(scoped)
	ctx := assetTestCtx(tn)

	host := vulnFindingAsset(t, assets, tn, "reopen-host")
	findingID := vulnFindingOpen(t, store, ctx, tn.TenantID, host.AssetID, "CVE-REMEDIATE-REOPEN", domain.VulnFindingSeverityHigh)

	finding, incident, err := store.Remediate(ctx, findingID, 1)
	if err != nil {
		t.Fatalf("remediate: %v", err)
	}

	// Close the incident through its normal lifecycle.
	current := incident
	for _, next := range []domain.IncidentStatus{
		domain.IncidentAcknowledged, domain.IncidentInvestigating, domain.IncidentResolved, domain.IncidentClosed,
	} {
		current, err = incidents.SetStatus(ctx, current.IncidentID, current.RowVersion, next)
		if err != nil {
			t.Fatalf("set status %s: %v", next, err)
		}
	}

	second, secondIncident, err := store.Remediate(ctx, findingID, finding.RowVersion)
	if err != nil {
		t.Fatalf("second remediate (after close): %v", err)
	}
	if secondIncident.IncidentID == incident.IncidentID {
		t.Fatalf("remediate reused the CLOSED incident %q — a closed link must not be treated as open", incident.IncidentID)
	}
	if second.RemediationIncidentID == nil || *second.RemediationIncidentID != secondIncident.IncidentID {
		t.Errorf("finding's link = %v, want the fresh incident %q", second.RemediationIncidentID, secondIncident.IncidentID)
	}
}

// TestVulnFindingStore_Remediate_RowVersionMismatch proves the optimistic
// lock: a stale row_version is refused before anything is written.
func TestVulnFindingStore_Remediate_RowVersionMismatch(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "vuln-remediate-stale")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	store := NewVulnFindingStore(scoped)
	ctx := assetTestCtx(tn)

	host := vulnFindingAsset(t, assets, tn, "stale-host")
	findingID := vulnFindingOpen(t, store, ctx, tn.TenantID, host.AssetID, "CVE-REMEDIATE-STALE", domain.VulnFindingSeverityHigh)

	if _, _, err := store.Remediate(ctx, findingID, 99); !errors.Is(err, domain.ErrVersionMismatch) {
		t.Errorf("err = %v, want ErrVersionMismatch", err)
	}

	// Nothing was written: no incident exists for this asset.
	rows, err := NewIncidentStore(scoped).List(ctx, 0, "", "")
	if err != nil {
		t.Fatalf("list incidents: %v", err)
	}
	for _, r := range rows {
		if r.AssetID != nil && *r.AssetID == host.AssetID {
			t.Fatalf("a rejected remediate call still created an incident: %+v", r)
		}
	}
}

// TestVulnFindingStore_Remediate_RequiresOpenFinding proves a
// remediated/accepted_risk/false_positive finding refuses Remediate.
func TestVulnFindingStore_Remediate_RequiresOpenFinding(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "vuln-remediate-notopen")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	store := NewVulnFindingStore(scoped)
	ctx := assetTestCtx(tn)

	host := vulnFindingAsset(t, assets, tn, "notopen-host")
	findingID := vulnFindingOpen(t, store, ctx, tn.TenantID, host.AssetID, "CVE-REMEDIATE-NOTOPEN", domain.VulnFindingSeverityHigh)

	if _, err := store.SetStatus(ctx, findingID, 1, domain.VulnFindingAcceptedRisk); err != nil {
		t.Fatalf("set status accepted_risk: %v", err)
	}

	if _, _, err := store.Remediate(ctx, findingID, 2); !errors.Is(err, domain.ErrVulnFindingNotOpen) {
		t.Errorf("err = %v, want ErrVulnFindingNotOpen", err)
	}
}

// TestVulnFindingStore_Remediate_NotFound proves an unknown finding_id
// refuses cleanly.
func TestVulnFindingStore_Remediate_NotFound(t *testing.T) {
	tn := assetTenant(t, NewTenantStore(testPool(t)), "vuln-remediate-notfound")
	scoped := tenantScopedPool(t)
	store := NewVulnFindingStore(scoped)
	ctx := assetTestCtx(tn)

	if _, _, err := store.Remediate(ctx, "no-such-finding", 1); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// TestVulnFindingStore_Remediate_TenantIsolation proves tenant B can never
// remediate tenant A's finding, and never lands a cross-tenant incident.
func TestVulnFindingStore_Remediate_TenantIsolation(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a, err := tenants.Create(adminTestCtx(), newTenant("vuln-rem-iso-alpha", "ext-vuln-rem-iso-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(adminTestCtx(), newTenant("vuln-rem-iso-bravo", "ext-vuln-rem-iso-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	store := NewVulnFindingStore(scoped)
	ctxA := assetTestCtx(a)
	ctxB := assetTestCtx(b)

	hostA := vulnFindingAsset(t, assets, a, "rem-iso-host-a")
	findingID := vulnFindingOpen(t, store, ctxA, a.TenantID, hostA.AssetID, "CVE-REM-ISO", domain.VulnFindingSeverityHigh)

	if _, _, err := store.Remediate(ctxB, findingID, 1); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("tenant B remediated tenant A's finding: err = %v, want ErrNotFound", err)
	}

	// Tenant A's finding is undisturbed, and tenant A itself can still
	// remediate it normally.
	stillOpen, err := store.Get(ctxA, findingID)
	if err != nil {
		t.Fatalf("tenant A lost its own finding: %v", err)
	}
	if stillOpen.RowVersion != 1 || stillOpen.RemediationIncidentID != nil {
		t.Errorf("tenant A's finding was mutated by tenant B's attempt: %+v", stillOpen)
	}
	if _, _, err := store.Remediate(ctxA, findingID, 1); err != nil {
		t.Fatalf("tenant A's own remediate: %v", err)
	}
}

// TestVulnFindingStore_Remediate_ConcurrentCallsCreateOnlyOneIncident is the
// mutation-tested concurrency proof: many goroutines racing Remediate on the
// SAME finding with the SAME (now-stale-to-all-but-one) row_version must
// settle on exactly one success and exactly one incident — every other
// goroutine fails closed with ErrVersionMismatch, never a duplicate.
func TestVulnFindingStore_Remediate_ConcurrentCallsCreateOnlyOneIncident(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "vuln-remediate-concurrent")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	store := NewVulnFindingStore(scoped)
	ctx := assetTestCtx(tn)

	host := vulnFindingAsset(t, assets, tn, "concurrent-host")
	findingID := vulnFindingOpen(t, store, ctx, tn.TenantID, host.AssetID, "CVE-REMEDIATE-CONCURRENT", domain.VulnFindingSeverityCritical)

	const n = 10
	incidentIDs := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, inc, err := store.Remediate(ctx, findingID, 1)
			errs[i] = err
			if inc != nil {
				incidentIDs[i] = inc.IncidentID
			}
		}(i)
	}
	wg.Wait()

	successes := 0
	for i, err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, domain.ErrVersionMismatch):
			// Expected: the loser saw a fresh row_version after the winner's
			// FOR UPDATE-serialised transaction committed.
		default:
			t.Fatalf("goroutine %d unexpected error: %v", i, err)
		}
	}
	if successes != 1 {
		t.Fatalf("successes = %d, want exactly 1 (every other caller must fail closed with ErrVersionMismatch, never silently create a second incident)", successes)
	}

	rows, err := NewIncidentStore(scoped).List(ctx, 0, "", "")
	if err != nil {
		t.Fatalf("list incidents: %v", err)
	}
	count := 0
	for _, r := range rows {
		if r.AssetID != nil && *r.AssetID == host.AssetID && r.Source == domain.IncidentSourceVuln {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("vuln-sourced incidents on asset %s after %d concurrent Remediate calls = %d, want exactly 1", host.AssetID, n, count)
	}

	finalFinding, err := store.Get(ctx, findingID)
	if err != nil {
		t.Fatalf("get finding: %v", err)
	}
	if finalFinding.RemediationIncidentID == nil {
		t.Fatal("finding has no remediation_incident_id linked after a successful concurrent remediate")
	}
}
