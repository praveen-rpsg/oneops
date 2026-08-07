//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// securityObservationAsset creates a real asset on the given tenant-scoped
// store, for a security observation test to point observations at — mirrors
// telemetryAsset's fixture shape exactly.
func securityObservationAsset(t *testing.T, assets *AssetStore, ctx context.Context, tenantID, name string) *domain.Asset {
	t.Helper()
	a, err := domain.NewAsset(tenantID, "server", name, nil)
	if err != nil {
		t.Fatalf("new asset: %v", err)
	}
	created, err := assets.Create(ctx, a)
	if err != nil {
		t.Fatalf("create asset: %v", err)
	}
	return created
}

func TestSecurityObservationStore_IngestQueryRoundTrip(t *testing.T) {
	testPool(t)
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "secobs-roundtrip")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	store := NewSecurityObservationStore(scoped)
	ctx := assetTestCtx(tn)

	host := securityObservationAsset(t, assets, ctx, tn.TenantID, "host-1")

	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	observations := []domain.SecurityObservation{
		{TenantID: tn.TenantID, AssetID: host.AssetID, ObservationType: "port_scan", Source: "wazuh",
			Severity: domain.ObservationSeverityHigh, ObservedAt: base, Attributes: map[string]string{"actor": "203.0.113.5"}},
		{TenantID: tn.TenantID, AssetID: host.AssetID, ObservationType: "port_scan", Source: "wazuh",
			Severity: domain.ObservationSeverityHigh, ObservedAt: base.Add(1 * time.Minute)},
		{TenantID: tn.TenantID, AssetID: host.AssetID, ObservationType: "port_scan", Source: "wazuh",
			Severity: domain.ObservationSeverityHigh, ObservedAt: base.Add(2 * time.Minute)},
		// A different observation_type on the same asset must not appear in a
		// port_scan range query.
		{TenantID: tn.TenantID, AssetID: host.AssetID, ObservationType: "malware_detected", Source: "wazuh",
			Severity: domain.ObservationSeverityCritical, ObservedAt: base},
	}
	results, err := store.WriteObservations(ctx, observations)
	if err != nil {
		t.Fatalf("write observations: %v", err)
	}
	if len(results) != len(observations) {
		t.Fatalf("results = %d, want %d", len(results), len(observations))
	}
	for i, r := range results {
		if !r.Accepted {
			t.Errorf("observation %d rejected: %s", i, r.Reason)
		}
	}

	got, err := store.QueryRange(ctx, host.AssetID, "port_scan", base, base.Add(10*time.Minute), 0, time.Time{})
	if err != nil {
		t.Fatalf("query range: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d observations, want 3: %+v", len(got), got)
	}
	if got[0].Attributes["actor"] != "203.0.113.5" {
		t.Errorf("attributes did not round-trip: %+v", got[0].Attributes)
	}
	if got[0].Severity != domain.ObservationSeverityHigh {
		t.Errorf("severity did not round-trip: %+v", got[0])
	}

	// Re-sending the identical batch is idempotent: still reported accepted,
	// and no new rows appear.
	if _, err := store.WriteObservations(ctx, observations[:3]); err != nil {
		t.Fatalf("re-write observations: %v", err)
	}
	again, err := store.QueryRange(ctx, host.AssetID, "port_scan", base, base.Add(10*time.Minute), 0, time.Time{})
	if err != nil {
		t.Fatalf("query range after re-write: %v", err)
	}
	if len(again) != 3 {
		t.Fatalf("re-ingest created duplicate rows: got %d, want 3", len(again))
	}

	// Keyset pagination: after the first observation's timestamp, only the
	// remaining two are returned.
	page, err := store.QueryRange(ctx, host.AssetID, "port_scan", base, base.Add(10*time.Minute), 0, base)
	if err != nil {
		t.Fatalf("query range with after: %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("paginated query = %d observations, want 2: %+v", len(page), page)
	}
}

func TestSecurityObservationStore_RejectsCrossTenantOrNonexistentAsset(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a, err := tenants.Create(adminTestCtx(), newTenant("secobs-rej-alpha", "ext-secobs-rej-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(adminTestCtx(), newTenant("secobs-rej-bravo", "ext-secobs-rej-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	store := NewSecurityObservationStore(scoped)
	ctxA := assetTestCtx(a)
	ctxB := assetTestCtx(b)

	victim := securityObservationAsset(t, assets, ctxA, a.TenantID, "victim-host")

	ts := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	results, err := store.WriteObservations(ctxB, []domain.SecurityObservation{
		// Tenant B names tenant A's real asset.
		{TenantID: b.TenantID, AssetID: victim.AssetID, ObservationType: "port_scan", Source: "wazuh",
			Severity: domain.ObservationSeverityLow, ObservedAt: ts},
		// Tenant B names an asset id that does not exist anywhere.
		{TenantID: b.TenantID, AssetID: "no-such-asset", ObservationType: "port_scan", Source: "wazuh",
			Severity: domain.ObservationSeverityLow, ObservedAt: ts},
	})
	if err != nil {
		t.Fatalf("write observations: %v", err)
	}
	for i, r := range results {
		if r.Accepted {
			t.Errorf("observation %d was accepted, want rejected: %+v", i, r)
		}
		if r.Reason == "" {
			t.Errorf("observation %d rejected with no reason", i)
		}
	}

	// No row was written naming the victim's asset — checked from tenant A's
	// own connection, where it would be visible if it existed.
	gotA, err := store.QueryRange(ctxA, victim.AssetID, "port_scan", ts.Add(-time.Hour), ts.Add(time.Hour), 0, time.Time{})
	if err != nil {
		t.Fatalf("query range as victim tenant: %v", err)
	}
	if len(gotA) != 0 {
		t.Fatalf("a cross-tenant observation was written: %+v", gotA)
	}
}

// The two-tenant isolation test the whole story exists to make pass: tenant
// B's query must never return tenant A's observations, and this proves it via
// row-level security, not an application-level filter — mirrors
// TestTelemetryIsolation_RLSByTenant exactly.
func TestSecurityObservationIsolation_RLSByTenant(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a, err := tenants.Create(adminTestCtx(), newTenant("secobs-iso-alpha", "ext-secobs-iso-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(adminTestCtx(), newTenant("secobs-iso-bravo", "ext-secobs-iso-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	store := NewSecurityObservationStore(scoped)
	ctxA := assetTestCtx(a)
	ctxB := assetTestCtx(b)

	hostA := securityObservationAsset(t, assets, ctxA, a.TenantID, "host-a")
	hostB := securityObservationAsset(t, assets, ctxB, b.TenantID, "host-b")

	ts := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	if _, err := store.WriteObservations(ctxA, []domain.SecurityObservation{
		{TenantID: a.TenantID, AssetID: hostA.AssetID, ObservationType: "port_scan", Source: "wazuh",
			Severity: domain.ObservationSeverityHigh, ObservedAt: ts, Attributes: map[string]string{"actor": "alpha-secret"}},
	}); err != nil {
		t.Fatalf("write observation as tenant a: %v", err)
	}
	if _, err := store.WriteObservations(ctxB, []domain.SecurityObservation{
		{TenantID: b.TenantID, AssetID: hostB.AssetID, ObservationType: "port_scan", Source: "wazuh",
			Severity: domain.ObservationSeverityLow, ObservedAt: ts, Attributes: map[string]string{"actor": "bravo-secret"}},
	}); err != nil {
		t.Fatalf("write observation as tenant b: %v", err)
	}

	// Tenant B can never see tenant A's observation, even naming tenant A's
	// own asset_id and observation_type directly — row-level security filters
	// on tenant_id, not on the caller's chosen filters.
	gotB, err := store.QueryRange(ctxB, hostA.AssetID, "port_scan", ts.Add(-time.Hour), ts.Add(time.Hour), 0, time.Time{})
	if err != nil {
		t.Fatalf("query range as tenant b: %v", err)
	}
	if len(gotB) != 0 {
		t.Fatalf("tenant B saw tenant A's security observation: %+v", gotB)
	}

	gotA, err := store.QueryRange(ctxA, hostA.AssetID, "port_scan", ts.Add(-time.Hour), ts.Add(time.Hour), 0, time.Time{})
	if err != nil {
		t.Fatalf("query range as tenant a: %v", err)
	}
	if len(gotA) != 1 || gotA[0].Attributes["actor"] != "alpha-secret" {
		t.Fatalf("tenant A did not see its own security observation: %+v", gotA)
	}

	// The privileged, tenant-predicated counterpart must also confine itself
	// to the tenant it is explicitly asked about — the read a future SOC
	// detection worker (E8.1b) would use.
	privStore := NewSecurityObservationStore(priv)
	gotAViaPrivileged, err := privStore.QueryRangeForTenant(adminTestCtx(), a.TenantID, hostA.AssetID, "port_scan", ts.Add(-time.Hour), ts.Add(time.Hour))
	if err != nil {
		t.Fatalf("query range for tenant a via privileged pool: %v", err)
	}
	if len(gotAViaPrivileged) != 1 || gotAViaPrivileged[0].Attributes["actor"] != "alpha-secret" {
		t.Fatalf("QueryRangeForTenant(a) = %+v, want tenant a's own observation", gotAViaPrivileged)
	}
	gotBViaPrivileged, err := privStore.QueryRangeForTenant(adminTestCtx(), b.TenantID, hostA.AssetID, "port_scan", ts.Add(-time.Hour), ts.Add(time.Hour))
	if err != nil {
		t.Fatalf("query range for tenant b via privileged pool: %v", err)
	}
	if len(gotBViaPrivileged) != 0 {
		t.Fatalf("QueryRangeForTenant(b) returned tenant A's observation when scoped to asset A but tenant B: %+v", gotBViaPrivileged)
	}
}

// TestSecurityObservation_IsARealHypertable proves the storage decision
// landed, not merely a plain table with the right name: security_observation
// must be registered in TimescaleDB's own catalogue — mirrors
// TestTelemetrySample_IsARealHypertable exactly.
func TestSecurityObservation_IsARealHypertable(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()

	var dimensions int
	err := pool.QueryRow(ctx, `
		SELECT num_dimensions FROM timescaledb_information.hypertables
		 WHERE hypertable_name = 'security_observation' AND hypertable_schema = current_schema()`,
	).Scan(&dimensions)
	if err != nil {
		t.Fatalf("security_observation is not registered as a TimescaleDB hypertable: %v", err)
	}
	if dimensions < 1 {
		t.Errorf("security_observation hypertable has %d dimensions, want at least 1", dimensions)
	}
}
