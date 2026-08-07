//go:build integration

package postgres

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/security"
)

// TestIOCMatcherStore_TenantsWithEnabledIOCsAndEnabledIOCsForTenant proves
// the E8.2b privileged read surface: TenantsWithEnabledIOCs only names a
// tenant holding at least one ENABLED entry, and EnabledIOCsForTenant is
// tenant-explicit and excludes disabled entries — with an explicit
// tenant_id predicate (ADR-TENANCY-012), even though this store runs on the
// privileged, RLS-bypassing pool.
func TestIOCMatcherStore_TenantsWithEnabledIOCsAndEnabledIOCsForTenant(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a, err := tenants.Create(adminTestCtx(), newTenant("iocmatch-store-alpha", "ext-iocmatch-store-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(adminTestCtx(), newTenant("iocmatch-store-bravo", "ext-iocmatch-store-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	scoped := tenantScopedPool(t)
	iocStore := NewIOCStore(scoped)
	ctxA := assetTestCtx(a)

	enabled, err := domain.NewIOC(a.TenantID, domain.IOCIndicatorTypeIP, "203.0.113.9", domain.IncidentSeverityHigh)
	if err != nil {
		t.Fatalf("new enabled ioc: %v", err)
	}
	if _, err := iocStore.Create(ctxA, enabled); err != nil {
		t.Fatalf("create enabled ioc: %v", err)
	}
	disabled, err := domain.NewIOC(a.TenantID, domain.IOCIndicatorTypeDomain, "disabled.example.com", domain.IncidentSeverityLow)
	if err != nil {
		t.Fatalf("new disabled ioc: %v", err)
	}
	createdDisabled, err := iocStore.Create(ctxA, disabled)
	if err != nil {
		t.Fatalf("create disabled ioc: %v", err)
	}
	isEnabled := false
	if _, err := iocStore.Update(ctxA, createdDisabled.IOCID, createdDisabled.RowVersion, domain.IOCPatch{Enabled: &isEnabled}); err != nil {
		t.Fatalf("disable ioc: %v", err)
	}

	matcherStore := NewIOCMatcherStore(priv)

	tenantsWithWork, err := matcherStore.TenantsWithEnabledIOCs(context.Background())
	if err != nil {
		t.Fatalf("tenants with enabled iocs: %v", err)
	}
	found, foundB := false, false
	for _, tn := range tenantsWithWork {
		if tn == a.TenantID {
			found = true
		}
		if tn == b.TenantID {
			foundB = true
		}
	}
	if !found {
		t.Errorf("tenant A (has an enabled ioc) not in TenantsWithEnabledIOCs: %v", tenantsWithWork)
	}
	if foundB {
		t.Errorf("tenant B (no iocs at all) appeared in TenantsWithEnabledIOCs: %v", tenantsWithWork)
	}

	gotA, err := matcherStore.EnabledIOCsForTenant(context.Background(), a.TenantID, 0, "")
	if err != nil {
		t.Fatalf("enabled iocs for tenant a: %v", err)
	}
	if len(gotA) != 1 || gotA[0].IOCID != enabled.IOCID {
		t.Fatalf("enabled iocs for tenant a = %+v, want exactly the one enabled entry", gotA)
	}

	// Tenant B, which owns none, gets an empty page — never tenant A's.
	gotB, err := matcherStore.EnabledIOCsForTenant(context.Background(), b.TenantID, 0, "")
	if err != nil {
		t.Fatalf("enabled iocs for tenant b: %v", err)
	}
	if len(gotB) != 0 {
		t.Fatalf("enabled iocs for tenant b = %+v, want empty", gotB)
	}
}

// TestSecurityObservationCounterStore_RecentForTenant_TenantIsolationWindowAndPaging
// proves the E8.2b privileged read: tenant-explicit (two tenants sharing the
// SAME observation_type never see each other's rows), the window is
// EXCLUSIVE of `from` and INCLUSIVE of `to` (so consecutive tiled windows
// never double-return the boundary row), and keyset pagination over the
// natural key returns every row exactly once across pages, in order.
func TestSecurityObservationCounterStore_RecentForTenant_TenantIsolationWindowAndPaging(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a, err := tenants.Create(adminTestCtx(), newTenant("iocmatch-recent-alpha", "ext-iocmatch-recent-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(adminTestCtx(), newTenant("iocmatch-recent-bravo", "ext-iocmatch-recent-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	writeStore := NewSecurityObservationStore(scoped)
	ctxA := assetTestCtx(a)
	ctxB := assetTestCtx(b)

	hostA := securityObservationAsset(t, assets, ctxA, a.TenantID, "recent-host-a")
	hostB := securityObservationAsset(t, assets, ctxB, b.TenantID, "recent-host-b")

	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	// Tenant A: 5 observations spread across the window, one exactly AT the
	// window's lower bound (must be excluded) and one exactly AT the upper
	// bound (must be included).
	aObs := []domain.SecurityObservation{
		{TenantID: a.TenantID, AssetID: hostA.AssetID, ObservationType: "port_scan", Source: "wazuh",
			Severity: domain.ObservationSeverityHigh, ObservedAt: base, Attributes: map[string]string{"n": "at-lower-bound-excluded"}},
		{TenantID: a.TenantID, AssetID: hostA.AssetID, ObservationType: "port_scan", Source: "wazuh",
			Severity: domain.ObservationSeverityHigh, ObservedAt: base.Add(1 * time.Minute), Attributes: map[string]string{"n": "1"}},
		{TenantID: a.TenantID, AssetID: hostA.AssetID, ObservationType: "port_scan", Source: "wazuh",
			Severity: domain.ObservationSeverityHigh, ObservedAt: base.Add(2 * time.Minute), Attributes: map[string]string{"n": "2"}},
		{TenantID: a.TenantID, AssetID: hostA.AssetID, ObservationType: "port_scan", Source: "wazuh",
			Severity: domain.ObservationSeverityHigh, ObservedAt: base.Add(3 * time.Minute), Attributes: map[string]string{"n": "3"}},
		{TenantID: a.TenantID, AssetID: hostA.AssetID, ObservationType: "port_scan", Source: "wazuh",
			Severity: domain.ObservationSeverityHigh, ObservedAt: base.Add(5 * time.Minute), Attributes: map[string]string{"n": "at-upper-bound-included"}},
	}
	if _, err := writeStore.WriteObservations(ctxA, aObs); err != nil {
		t.Fatalf("write observations as tenant a: %v", err)
	}
	// Tenant B: same observation_type/source, same window, on its OWN asset —
	// must never appear in tenant A's read.
	if _, err := writeStore.WriteObservations(ctxB, []domain.SecurityObservation{
		{TenantID: b.TenantID, AssetID: hostB.AssetID, ObservationType: "port_scan", Source: "wazuh",
			Severity: domain.ObservationSeverityHigh, ObservedAt: base.Add(2 * time.Minute), Attributes: map[string]string{"n": "tenant-b-only"}},
	}); err != nil {
		t.Fatalf("write observations as tenant b: %v", err)
	}

	counter := NewSecurityObservationCounterStore(priv)
	from, to := base, base.Add(5*time.Minute)

	// First page, limit 2: excludes the at-lower-bound row, returns the next
	// two in order.
	page1, err := counter.RecentForTenant(context.Background(), a.TenantID, from, to, 2, nil)
	if err != nil {
		t.Fatalf("recent for tenant a, page 1: %v", err)
	}
	if len(page1) != 2 || page1[0].Attributes["n"] != "1" || page1[1].Attributes["n"] != "2" {
		t.Fatalf("page 1 = %+v, want [n=1, n=2] (lower bound excluded)", page1)
	}

	page2, err := counter.RecentForTenant(context.Background(), a.TenantID, from, to, 2, &page1[len(page1)-1])
	if err != nil {
		t.Fatalf("recent for tenant a, page 2: %v", err)
	}
	if len(page2) != 2 || page2[0].Attributes["n"] != "3" || page2[1].Attributes["n"] != "at-upper-bound-included" {
		t.Fatalf("page 2 = %+v, want [n=3, n=at-upper-bound-included] (upper bound included)", page2)
	}

	page3, err := counter.RecentForTenant(context.Background(), a.TenantID, from, to, 2, &page2[len(page2)-1])
	if err != nil {
		t.Fatalf("recent for tenant a, page 3: %v", err)
	}
	if len(page3) != 0 {
		t.Fatalf("page 3 = %+v, want empty (exactly 4 rows in-window, already consumed)", page3)
	}

	// Tenant B's own read, same window: sees only its own row, never tenant
	// A's five.
	gotB, err := counter.RecentForTenant(context.Background(), b.TenantID, from, to, 0, nil)
	if err != nil {
		t.Fatalf("recent for tenant b: %v", err)
	}
	if len(gotB) != 1 || gotB[0].Attributes["n"] != "tenant-b-only" {
		t.Fatalf("recent for tenant b = %+v, want exactly its own row", gotB)
	}
}

// iocMatcherFixture wires a real security.IOCMatcher over the privileged
// pool's IOCMatcherStore/SecurityObservationCounterStore and the privileged
// IncidentStore correlator — the exact shape cmd/controlplane/main.go wires
// in production.
type iocMatcherFixture struct {
	tn    *domain.Tenant
	host  *domain.Asset
	iocs  *IOCStore // tenant-scoped admin API, to seed the watchlist
	obs   *SecurityObservationStore
	incs  *IncidentStore // tenant-scoped, to read back the resulting incident
	inner *security.IOCMatcher
	now   time.Time
}

func newIOCMatcherFixture(t *testing.T, slug string, interval time.Duration) *iocMatcherFixture {
	t.Helper()
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), slug)

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	host := securityObservationAsset(t, assets, assetTestCtx(tn), tn.TenantID, slug+"-host")

	matcherStore := NewIOCMatcherStore(priv)
	obsCounter := NewSecurityObservationCounterStore(priv)
	correlator := NewIncidentStore(priv)

	quiet := slog.New(slog.NewTextHandler(logDiscard{}, nil))
	now := time.Now().UTC()
	m := security.NewIOCMatcher(matcherStore, obsCounter, correlator, security.NopIOCMatcherMetrics{}, quiet, security.IOCMatcherConfig{Interval: interval})

	return &iocMatcherFixture{
		tn: tn, host: host,
		iocs: NewIOCStore(scoped), obs: NewSecurityObservationStore(scoped), incs: NewIncidentStore(scoped),
		inner: m, now: now,
	}
}

func (f *iocMatcherFixture) seedIOC(t *testing.T, indicatorType domain.IOCIndicatorType, value string, severity domain.IncidentSeverity, enabled bool) *domain.IOC {
	t.Helper()
	i, err := domain.NewIOC(f.tn.TenantID, indicatorType, value, severity)
	if err != nil {
		t.Fatalf("new ioc: %v", err)
	}
	created, err := f.iocs.Create(assetTestCtx(f.tn), i)
	if err != nil {
		t.Fatalf("create ioc: %v", err)
	}
	if !enabled {
		isEnabled := false
		created, err = f.iocs.Update(assetTestCtx(f.tn), created.IOCID, created.RowVersion, domain.IOCPatch{Enabled: &isEnabled})
		if err != nil {
			t.Fatalf("disable ioc: %v", err)
		}
	}
	return created
}

func (f *iocMatcherFixture) writeObservation(t *testing.T, observedAt time.Time, attrs map[string]string) {
	t.Helper()
	results, err := f.obs.WriteObservations(assetTestCtx(f.tn), []domain.SecurityObservation{
		{TenantID: f.tn.TenantID, AssetID: f.host.AssetID, ObservationType: "outbound_connection", Source: "wazuh",
			Severity: domain.ObservationSeverityLow, ObservedAt: observedAt, Attributes: attrs},
	})
	if err != nil {
		t.Fatalf("write observation: %v", err)
	}
	if !results[0].Accepted {
		t.Fatalf("observation rejected: %s", results[0].Reason)
	}
}

func (f *iocMatcherFixture) openSecurityIncidents(t *testing.T) []*domain.Incident {
	t.Helper()
	rows, err := f.incs.List(assetTestCtx(f.tn), 0, "", "")
	if err != nil {
		t.Fatalf("list incidents: %v", err)
	}
	var out []*domain.Incident
	for _, r := range rows {
		if r.Source == domain.IncidentSourceSecurity && r.AssetID != nil && *r.AssetID == f.host.AssetID {
			out = append(out, r)
		}
	}
	return out
}

// TestIOCMatcher_EndToEnd_MatchRaisesIncidentAtIOCSeverityWithNote is the
// real-database, real-worker proof of E8.2b's core payoff: an observation
// whose attribute value matches an enabled ioc raises exactly one open
// security incident at the ioc's own severity.
func TestIOCMatcher_EndToEnd_MatchRaisesIncidentAtIOCSeverityWithNote(t *testing.T) {
	f := newIOCMatcherFixture(t, "iocmatch-e2e-match", time.Hour)
	f.seedIOC(t, domain.IOCIndicatorTypeIP, "203.0.113.5", domain.IncidentSeverityCritical, true)
	f.writeObservation(t, f.now.Add(-time.Minute), map[string]string{"dest_ip": "203.0.113.5"})

	f.inner.RunOnce(context.Background())

	incidents := f.openSecurityIncidents(t)
	if len(incidents) != 1 {
		t.Fatalf("open security incidents on the asset = %d, want exactly 1: %+v", len(incidents), incidents)
	}
	if incidents[0].Severity != domain.IncidentSeverityCritical {
		t.Errorf("incident severity = %q, want the ioc's own %q", incidents[0].Severity, domain.IncidentSeverityCritical)
	}
	timeline, err := f.incs.Timeline(assetTestCtx(f.tn), incidents[0].IncidentID, 0, "")
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	var created int
	for _, e := range timeline {
		if e.Kind == domain.IncidentEventCreated {
			created++
			if e.Actor != "system:ioc-matcher" {
				t.Errorf("created event actor = %q, want system:ioc-matcher", e.Actor)
			}
		}
	}
	if created != 1 {
		t.Errorf("created timeline rows = %d, want 1", created)
	}
}

// TestIOCMatcher_EndToEnd_NonMatchRaisesNothing proves a real observation
// whose attribute values never equal any enabled ioc raises no incident.
func TestIOCMatcher_EndToEnd_NonMatchRaisesNothing(t *testing.T) {
	f := newIOCMatcherFixture(t, "iocmatch-e2e-nomatch", time.Hour)
	f.seedIOC(t, domain.IOCIndicatorTypeIP, "203.0.113.5", domain.IncidentSeverityHigh, true)
	f.writeObservation(t, f.now.Add(-time.Minute), map[string]string{"dest_ip": "198.51.100.9"})

	f.inner.RunOnce(context.Background())

	if got := f.openSecurityIncidents(t); len(got) != 0 {
		t.Fatalf("open security incidents for a non-matching observation = %d, want 0: %+v", len(got), got)
	}
}

// TestIOCMatcher_EndToEnd_DisabledIOCDoesNotMatch proves a disabled ioc is
// never matched against, end to end.
func TestIOCMatcher_EndToEnd_DisabledIOCDoesNotMatch(t *testing.T) {
	f := newIOCMatcherFixture(t, "iocmatch-e2e-disabled", time.Hour)
	f.seedIOC(t, domain.IOCIndicatorTypeIP, "203.0.113.5", domain.IncidentSeverityHigh, false)
	f.writeObservation(t, f.now.Add(-time.Minute), map[string]string{"dest_ip": "203.0.113.5"})

	f.inner.RunOnce(context.Background())

	if got := f.openSecurityIncidents(t); len(got) != 0 {
		t.Fatalf("open security incidents with only a disabled ioc = %d, want 0: %+v", len(got), got)
	}
}

// TestIOCMatcher_EndToEnd_DomainNormalizationCaseInsensitive proves a
// differently-cased domain attribute value matches a lower-cased domain ioc,
// end to end against the real database.
func TestIOCMatcher_EndToEnd_DomainNormalizationCaseInsensitive(t *testing.T) {
	f := newIOCMatcherFixture(t, "iocmatch-e2e-norm", time.Hour)
	f.seedIOC(t, domain.IOCIndicatorTypeDomain, "evil.example.com", domain.IncidentSeverityMedium, true)
	f.writeObservation(t, f.now.Add(-time.Minute), map[string]string{"queried_domain": "EVIL.EXAMPLE.COM"})

	f.inner.RunOnce(context.Background())

	if got := f.openSecurityIncidents(t); len(got) != 1 {
		t.Fatalf("open security incidents for a case-differing domain match = %d, want exactly 1: %+v", len(got), got)
	}
}

// TestIOCMatcher_EndToEnd_NoDuplicateAcrossRepeatedPasses proves re-running a
// pass over the SAME already-matched observation (identical window) does not
// spam a second incident — the DB-level no-duplicate guarantee
// (ux_incident_open_security_per_asset) FindOrCreateOpenSecurityIncident
// already provides, exercised through the real worker.
func TestIOCMatcher_EndToEnd_NoDuplicateAcrossRepeatedPasses(t *testing.T) {
	f := newIOCMatcherFixture(t, "iocmatch-e2e-nodup", time.Hour)
	f.seedIOC(t, domain.IOCIndicatorTypeIP, "203.0.113.5", domain.IncidentSeverityHigh, true)
	f.writeObservation(t, f.now.Add(-time.Minute), map[string]string{"dest_ip": "203.0.113.5"})

	f.inner.RunOnce(context.Background())
	f.inner.RunOnce(context.Background())
	f.inner.RunOnce(context.Background())

	incidents := f.openSecurityIncidents(t)
	if len(incidents) != 1 {
		t.Fatalf("open security incidents after 3 repeated passes = %d, want exactly 1 (no duplicate)", len(incidents))
	}
}

// TestIOCMatcher_EndToEnd_TwoTenantsSharingIndicatorValueIsolated is the
// real-database tenant-isolation proof (ADR-TENANCY-012): two tenants each
// independently register an ioc for the SAME indicator_value (allowed —
// ADR-SOC-004's uniqueness is tenant-scoped) and each has its own asset and
// observation matching it. Each tenant must get exactly its OWN incident, on
// its OWN asset — never linked to, or influenced by, the other tenant's ioc
// or observation.
func TestIOCMatcher_EndToEnd_TwoTenantsSharingIndicatorValueIsolated(t *testing.T) {
	priv := testPool(t)
	a := assetTenant(t, NewTenantStore(priv), "iocmatch-e2e-iso-alpha")
	b := assetTenant(t, NewTenantStore(priv), "iocmatch-e2e-iso-bravo")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	hostA := securityObservationAsset(t, assets, assetTestCtx(a), a.TenantID, "iso-host-a")
	hostB := securityObservationAsset(t, assets, assetTestCtx(b), b.TenantID, "iso-host-b")

	iocsA := NewIOCStore(scoped)
	iocsB := NewIOCStore(scoped)
	const sharedValue = "203.0.113.77"
	iA, err := domain.NewIOC(a.TenantID, domain.IOCIndicatorTypeIP, sharedValue, domain.IncidentSeverityHigh)
	if err != nil {
		t.Fatalf("new ioc a: %v", err)
	}
	if _, err := iocsA.Create(assetTestCtx(a), iA); err != nil {
		t.Fatalf("create ioc a: %v", err)
	}
	iB, err := domain.NewIOC(b.TenantID, domain.IOCIndicatorTypeIP, sharedValue, domain.IncidentSeverityLow)
	if err != nil {
		t.Fatalf("new ioc b: %v", err)
	}
	if _, err := iocsB.Create(assetTestCtx(b), iB); err != nil {
		t.Fatalf("create ioc b: %v", err)
	}

	obsStore := NewSecurityObservationStore(scoped)
	now := time.Now().UTC()
	if _, err := obsStore.WriteObservations(assetTestCtx(a), []domain.SecurityObservation{
		{TenantID: a.TenantID, AssetID: hostA.AssetID, ObservationType: "outbound_connection", Source: "wazuh",
			Severity: domain.ObservationSeverityLow, ObservedAt: now.Add(-time.Minute), Attributes: map[string]string{"dest_ip": sharedValue}},
	}); err != nil {
		t.Fatalf("write observation as tenant a: %v", err)
	}
	if _, err := obsStore.WriteObservations(assetTestCtx(b), []domain.SecurityObservation{
		{TenantID: b.TenantID, AssetID: hostB.AssetID, ObservationType: "outbound_connection", Source: "wazuh",
			Severity: domain.ObservationSeverityLow, ObservedAt: now.Add(-time.Minute), Attributes: map[string]string{"dest_ip": sharedValue}},
	}); err != nil {
		t.Fatalf("write observation as tenant b: %v", err)
	}

	quiet := slog.New(slog.NewTextHandler(logDiscard{}, nil))
	m := security.NewIOCMatcher(
		NewIOCMatcherStore(priv), NewSecurityObservationCounterStore(priv), NewIncidentStore(priv),
		security.NopIOCMatcherMetrics{}, quiet, security.IOCMatcherConfig{Interval: time.Hour})
	m.RunOnce(context.Background())

	incsA := NewIncidentStore(scoped)
	rowsA, err := incsA.List(assetTestCtx(a), 0, "", "")
	if err != nil {
		t.Fatalf("list tenant a incidents: %v", err)
	}
	var matchedA []*domain.Incident
	for _, r := range rowsA {
		if r.Source == domain.IncidentSourceSecurity {
			matchedA = append(matchedA, r)
		}
	}
	if len(matchedA) != 1 {
		t.Fatalf("tenant a security incidents = %d, want exactly 1: %+v", len(matchedA), matchedA)
	}
	if matchedA[0].AssetID == nil || *matchedA[0].AssetID != hostA.AssetID {
		t.Fatalf("tenant a's incident asset_id = %v, want its own %q", matchedA[0].AssetID, hostA.AssetID)
	}
	if matchedA[0].Severity != domain.IncidentSeverityHigh {
		t.Errorf("tenant a's incident severity = %q, want its own ioc's %q (not tenant b's low)", matchedA[0].Severity, domain.IncidentSeverityHigh)
	}

	incsB := NewIncidentStore(scoped)
	rowsB, err := incsB.List(assetTestCtx(b), 0, "", "")
	if err != nil {
		t.Fatalf("list tenant b incidents: %v", err)
	}
	var matchedB []*domain.Incident
	for _, r := range rowsB {
		if r.Source == domain.IncidentSourceSecurity {
			matchedB = append(matchedB, r)
		}
	}
	if len(matchedB) != 1 {
		t.Fatalf("tenant b security incidents = %d, want exactly 1: %+v", len(matchedB), matchedB)
	}
	if matchedB[0].AssetID == nil || *matchedB[0].AssetID != hostB.AssetID {
		t.Fatalf("tenant b's incident asset_id = %v, want its own %q", matchedB[0].AssetID, hostB.AssetID)
	}
	if matchedB[0].Severity != domain.IncidentSeverityLow {
		t.Errorf("tenant b's incident severity = %q, want its own ioc's %q (not tenant a's high)", matchedB[0].Severity, domain.IncidentSeverityLow)
	}
	if matchedA[0].IncidentID == matchedB[0].IncidentID {
		t.Fatalf("tenant a and tenant b were correlated to the SAME incident %q — cross-tenant leak", matchedA[0].IncidentID)
	}
}
