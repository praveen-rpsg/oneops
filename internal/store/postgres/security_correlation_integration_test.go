//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

// securityCorrelationFixture wires one tenant, one asset and a
// privileged-pool IncidentStore — the shape internal/security.SecurityDetector
// actually uses in production (postgres.NewIncidentStore(pool), not appPool).
// Mirrors correlationFixture (incident_correlation_integration_test.go)
// exactly.
type securityCorrelationFixture struct {
	tn   *domain.Tenant
	host *domain.Asset
	priv *IncidentStore // built over the PRIVILEGED pool — the correlator under test
}

func newSecurityCorrelationFixture(t *testing.T, slug string) *securityCorrelationFixture {
	t.Helper()
	testPool(t) // ensures migrations are applied before anything else touches the schema
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), slug)

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	host := telemetryAsset(t, assets, assetTestCtx(tn), tn.TenantID, slug+"-host")

	return &securityCorrelationFixture{tn: tn, host: host, priv: NewIncidentStore(priv)}
}

// TestIncidentStore_FindOrCreateOpenSecurityIncident_CreatesThenLinks is the
// no-duplicate proof at the store layer — the security analog of
// TestIncidentStore_FindOrCreateOpenAlertIncident_CreatesThenLinks: the first
// call creates an incident; a second call for the SAME (tenant, asset) links
// to it instead of creating a second one, and a third call after the first
// is resolved creates a NEW one.
func TestIncidentStore_FindOrCreateOpenSecurityIncident_CreatesThenLinks(t *testing.T) {
	f := newSecurityCorrelationFixture(t, "seccorr-create-link")
	ctx := context.Background()

	want1, err := domain.NewSecurityIncident(f.tn.TenantID, "port_scan detected", "", domain.IncidentSeverityHigh, f.host.AssetID)
	if err != nil {
		t.Fatalf("new security incident: %v", err)
	}
	id1, err := f.priv.FindOrCreateOpenSecurityIncident(ctx, want1, "system:security", "security rule port_scan fired")
	if err != nil {
		t.Fatalf("first find-or-create: %v", err)
	}

	want2, err := domain.NewSecurityIncident(f.tn.TenantID, "malware_detected detected", "", domain.IncidentSeverityHigh, f.host.AssetID)
	if err != nil {
		t.Fatalf("new security incident 2: %v", err)
	}
	id2, err := f.priv.FindOrCreateOpenSecurityIncident(ctx, want2, "system:security", "security rule malware_detected fired")
	if err != nil {
		t.Fatalf("second find-or-create: %v", err)
	}
	if id2 != id1 {
		t.Fatalf("second firing on the same asset created %q, want it to link to the first %q", id2, id1)
	}

	// Exactly one security-sourced incident row exists for this asset.
	rows, err := NewIncidentStore(tenantScopedPool(t)).List(assetTestCtx(f.tn), 0, "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	count := 0
	for _, r := range rows {
		if r.AssetID != nil && *r.AssetID == f.host.AssetID && r.Source == domain.IncidentSourceSecurity {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("security incidents on asset %s = %d, want exactly 1 (no duplicate)", f.host.AssetID, count)
	}

	// The timeline recorded one "created" row and one "security_note" row.
	timeline, err := NewIncidentStore(tenantScopedPool(t)).Timeline(assetTestCtx(f.tn), id1, 0, "")
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	var created, notes int
	for _, e := range timeline {
		switch e.Kind {
		case domain.IncidentEventCreated:
			created++
		case domain.IncidentEventSecurityNote:
			notes++
			if e.Actor != "system:security" {
				t.Errorf("security_note actor = %q, want system:security", e.Actor)
			}
		}
	}
	if created != 1 || notes != 1 {
		t.Fatalf("timeline created=%d security_note=%d, want 1 and 1", created, notes)
	}

	// Resolve then close the incident; a THIRD firing must create a new one —
	// the unique index's predicate excludes resolved/closed rows.
	scoped := NewIncidentStore(tenantScopedPool(t))
	current, err := scoped.Get(assetTestCtx(f.tn), id1)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	for _, next := range []domain.IncidentStatus{domain.IncidentAcknowledged, domain.IncidentInvestigating, domain.IncidentResolved, domain.IncidentClosed} {
		current, err = scoped.SetStatus(assetTestCtx(f.tn), id1, current.RowVersion, next)
		if err != nil {
			t.Fatalf("set status %s: %v", next, err)
		}
	}

	want3, err := domain.NewSecurityIncident(f.tn.TenantID, "port_scan detected again", "", domain.IncidentSeverityHigh, f.host.AssetID)
	if err != nil {
		t.Fatalf("new security incident 3: %v", err)
	}
	id3, err := f.priv.FindOrCreateOpenSecurityIncident(ctx, want3, "system:security", "security rule port_scan fired")
	if err != nil {
		t.Fatalf("third find-or-create: %v", err)
	}
	if id3 == id1 {
		t.Fatalf("a firing after the prior incident was closed must create a new one, not reuse the closed %q", id1)
	}
}

// TestIncidentStore_FindOrCreateOpenSecurityIncident_ManualIncidentNotLinked
// proves Source gates correlation at the store layer: a manually-filed OPEN
// incident on the same asset is never discovered by
// FindOrCreateOpenSecurityIncident.
func TestIncidentStore_FindOrCreateOpenSecurityIncident_ManualIncidentNotLinked(t *testing.T) {
	f := newSecurityCorrelationFixture(t, "seccorr-manual")
	scoped := NewIncidentStore(tenantScopedPool(t))
	manual, err := domain.NewIncident(f.tn.TenantID, "operator filed this", "", domain.IncidentSeverityHigh, &f.host.AssetID, nil)
	if err != nil {
		t.Fatalf("new manual incident: %v", err)
	}
	created, err := scoped.Create(assetTestCtx(f.tn), manual)
	if err != nil {
		t.Fatalf("create manual incident: %v", err)
	}

	want, err := domain.NewSecurityIncident(f.tn.TenantID, "port_scan detected", "", domain.IncidentSeverityHigh, f.host.AssetID)
	if err != nil {
		t.Fatalf("new security incident: %v", err)
	}
	id, err := f.priv.FindOrCreateOpenSecurityIncident(context.Background(), want, "system:security", "security rule port_scan fired")
	if err != nil {
		t.Fatalf("find-or-create: %v", err)
	}
	if id == created.IncidentID {
		t.Fatalf("correlation linked to the manual incident %q — source must gate it", created.IncidentID)
	}
}

// TestIncidentStore_AlertAndSecurityIncidentsCoexistOnSameAsset is the
// coexistence proof the whole story requires: an OPEN alert-sourced incident
// and an OPEN security-sourced incident on the exact same (tenant, asset)
// are two SEPARATE rows — ux_incident_open_alert_per_asset and
// ux_incident_open_security_per_asset are independent, source-scoped
// indexes, so neither correlation path's find-or-create ever discovers,
// links to, or is blocked by, the other's incident.
func TestIncidentStore_AlertAndSecurityIncidentsCoexistOnSameAsset(t *testing.T) {
	f := newSecurityCorrelationFixture(t, "seccorr-coexist")
	ctx := context.Background()

	wantAlert, err := domain.NewAlertIncident(f.tn.TenantID, "cpu_utilization firing", "", domain.IncidentSeverityHigh, f.host.AssetID)
	if err != nil {
		t.Fatalf("new alert incident: %v", err)
	}
	alertID, err := f.priv.FindOrCreateOpenAlertIncident(ctx, wantAlert, "system:alerting", "alert cpu_utilization fired")
	if err != nil {
		t.Fatalf("find-or-create alert incident: %v", err)
	}

	wantSecurity, err := domain.NewSecurityIncident(f.tn.TenantID, "port_scan detected", "", domain.IncidentSeverityHigh, f.host.AssetID)
	if err != nil {
		t.Fatalf("new security incident: %v", err)
	}
	securityID, err := f.priv.FindOrCreateOpenSecurityIncident(ctx, wantSecurity, "system:security", "security rule port_scan fired")
	if err != nil {
		t.Fatalf("find-or-create security incident: %v", err)
	}

	if alertID == securityID {
		t.Fatalf("alert and security incidents on the same asset collapsed into one row: %q", alertID)
	}

	// A second alert firing links to the alert incident, never the security
	// one; a second security firing links to the security incident, never
	// the alert one.
	wantAlert2, err := domain.NewAlertIncident(f.tn.TenantID, "disk_free_bytes firing", "", domain.IncidentSeverityHigh, f.host.AssetID)
	if err != nil {
		t.Fatalf("new alert incident 2: %v", err)
	}
	alertID2, err := f.priv.FindOrCreateOpenAlertIncident(ctx, wantAlert2, "system:alerting", "alert disk_free_bytes fired")
	if err != nil {
		t.Fatalf("second alert find-or-create: %v", err)
	}
	if alertID2 != alertID {
		t.Fatalf("second alert firing created/linked %q, want the original alert incident %q", alertID2, alertID)
	}

	wantSecurity2, err := domain.NewSecurityIncident(f.tn.TenantID, "malware_detected detected", "", domain.IncidentSeverityHigh, f.host.AssetID)
	if err != nil {
		t.Fatalf("new security incident 2: %v", err)
	}
	securityID2, err := f.priv.FindOrCreateOpenSecurityIncident(ctx, wantSecurity2, "system:security", "security rule malware_detected fired")
	if err != nil {
		t.Fatalf("second security find-or-create: %v", err)
	}
	if securityID2 != securityID {
		t.Fatalf("second security firing created/linked %q, want the original security incident %q", securityID2, securityID)
	}

	// Both rows are visible, distinct, and correctly sourced.
	scoped := NewIncidentStore(tenantScopedPool(t))
	gotAlert, err := scoped.Get(assetTestCtx(f.tn), alertID)
	if err != nil {
		t.Fatalf("get alert incident: %v", err)
	}
	if gotAlert.Source != domain.IncidentSourceAlert {
		t.Errorf("alert incident source = %q, want alert", gotAlert.Source)
	}
	gotSecurity, err := scoped.Get(assetTestCtx(f.tn), securityID)
	if err != nil {
		t.Fatalf("get security incident: %v", err)
	}
	if gotSecurity.Source != domain.IncidentSourceSecurity {
		t.Errorf("security incident source = %q, want security", gotSecurity.Source)
	}
}

// TestIncidentStore_FindOrCreateOpenSecurityIncident_ConcurrentFiringsNoDuplicate
// is the mutation-tested proof of the no-duplicate DB constraint under TRUE
// concurrency — the security analog of
// TestIncidentStore_FindOrCreateOpenAlertIncident_ConcurrentFiringsNoDuplicate:
// many goroutines racing FindOrCreateOpenSecurityIncident for the SAME
// (tenant, asset) must settle on exactly one incident, never more.
func TestIncidentStore_FindOrCreateOpenSecurityIncident_ConcurrentFiringsNoDuplicate(t *testing.T) {
	f := newSecurityCorrelationFixture(t, "seccorr-concurrent")

	const n = 20
	ids := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			want, err := domain.NewSecurityIncident(f.tn.TenantID, fmt.Sprintf("observation-type-%d detected", i), "", domain.IncidentSeverityHigh, f.host.AssetID)
			if err != nil {
				errs[i] = err
				return
			}
			id, err := f.priv.FindOrCreateOpenSecurityIncident(context.Background(), want, "system:security", fmt.Sprintf("security rule observation-type-%d fired", i))
			ids[i] = id
			errs[i] = err
		}(i)
	}
	wg.Wait()

	first := ""
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
		if first == "" {
			first = ids[i]
		} else if ids[i] != first {
			t.Fatalf("goroutine %d returned incident %q, want the same %q every other goroutine converged on", i, ids[i], first)
		}
	}

	rows, err := NewIncidentStore(tenantScopedPool(t)).List(assetTestCtx(f.tn), 0, "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	count := 0
	for _, r := range rows {
		if r.AssetID != nil && *r.AssetID == f.host.AssetID && r.Source == domain.IncidentSourceSecurity {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("security incidents on asset %s after %d concurrent firings = %d, want exactly 1", f.host.AssetID, n, count)
	}
}

// TestIncidentStore_AppendSecurityNote_TenantIsolation is the make-or-break
// mutation-tested proof for ADR-TENANCY-012 on the write side — the security
// analog of TestIncidentStore_AppendAlertNote_TenantIsolation: tenant B's
// tenantID must never be able to append a note to tenant A's incident, even
// though this store runs on the PRIVILEGED pool and holds tenant A's own
// real incident_id.
func TestIncidentStore_AppendSecurityNote_TenantIsolation(t *testing.T) {
	priv := testPool(t)
	tnA := assetTenant(t, NewTenantStore(priv), "seccorr-tenant-a")
	tnB := assetTenant(t, NewTenantStore(priv), "seccorr-tenant-b")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	hostA := telemetryAsset(t, assets, assetTestCtx(tnA), tnA.TenantID, "seccorr-tenant-a-host")

	corr := NewIncidentStore(priv)
	wantA, err := domain.NewSecurityIncident(tnA.TenantID, "port_scan detected", "", domain.IncidentSeverityHigh, hostA.AssetID)
	if err != nil {
		t.Fatalf("new security incident: %v", err)
	}
	incidentA, err := corr.FindOrCreateOpenSecurityIncident(context.Background(), wantA, "system:security", "security rule port_scan fired")
	if err != nil {
		t.Fatalf("create tenant A's incident: %v", err)
	}

	// Tenant B, given tenant A's real incident id, must be refused.
	err = corr.AppendSecurityNote(context.Background(), tnB.TenantID, incidentA, "cross-tenant note", "system:security")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-tenant AppendSecurityNote err = %v, want ErrNotFound — tenant B must never touch tenant A's incident", err)
	}

	// The legitimate owner can still append.
	if err := corr.AppendSecurityNote(context.Background(), tnA.TenantID, incidentA, "security rule port_scan recovered", "system:security"); err != nil {
		t.Fatalf("tenant A's own AppendSecurityNote: %v", err)
	}

	timeline, err := NewIncidentStore(tenantScopedPool(t)).Timeline(assetTestCtx(tnA), incidentA, 0, "")
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	notes := 0
	for _, e := range timeline {
		if e.Kind == domain.IncidentEventSecurityNote {
			notes++
		}
	}
	if notes != 1 {
		t.Fatalf("security_note rows on tenant A's incident = %d, want exactly 1 (the refused cross-tenant call must not have written one)", notes)
	}
}
