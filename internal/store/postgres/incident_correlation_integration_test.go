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

// correlationFixture wires one tenant, one asset and a privileged-pool
// IncidentStore — the shape internal/alerting.Evaluator actually uses in
// production (postgres.NewIncidentStore(pool), not appPool).
type correlationFixture struct {
	tn   *domain.Tenant
	host *domain.Asset
	priv *IncidentStore // built over the PRIVILEGED pool — the correlator under test
}

func newCorrelationFixture(t *testing.T, slug string) *correlationFixture {
	t.Helper()
	testPool(t) // ensures migrations are applied before anything else touches the schema
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), slug)

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	host := telemetryAsset(t, assets, assetTestCtx(tn), tn.TenantID, slug+"-host")

	return &correlationFixture{tn: tn, host: host, priv: NewIncidentStore(priv)}
}

// TestIncidentStore_FindOrCreateOpenAlertIncident_CreatesThenLinks is the
// no-duplicate proof at the store layer: the first call creates an incident;
// a second call for the SAME (tenant, asset) links to it instead of creating
// a second one, and a third call after the first is resolved creates a NEW
// one (a resolved incident is no longer "open").
func TestIncidentStore_FindOrCreateOpenAlertIncident_CreatesThenLinks(t *testing.T) {
	f := newCorrelationFixture(t, "corr-create-link")
	ctx := context.Background()

	want1, err := domain.NewAlertIncident(f.tn.TenantID, "cpu_utilization firing", "", domain.IncidentSeverityHigh, f.host.AssetID)
	if err != nil {
		t.Fatalf("new alert incident: %v", err)
	}
	id1, err := f.priv.FindOrCreateOpenAlertIncident(ctx, want1, "system:alerting", "alert cpu_utilization fired")
	if err != nil {
		t.Fatalf("first find-or-create: %v", err)
	}

	want2, err := domain.NewAlertIncident(f.tn.TenantID, "disk_free_bytes firing", "", domain.IncidentSeverityHigh, f.host.AssetID)
	if err != nil {
		t.Fatalf("new alert incident 2: %v", err)
	}
	id2, err := f.priv.FindOrCreateOpenAlertIncident(ctx, want2, "system:alerting", "alert disk_free_bytes fired")
	if err != nil {
		t.Fatalf("second find-or-create: %v", err)
	}
	if id2 != id1 {
		t.Fatalf("second firing on the same asset created %q, want it to link to the first %q", id2, id1)
	}

	// Exactly one incident row exists for this asset.
	rows, err := NewIncidentStore(tenantScopedPool(t)).List(assetTestCtx(f.tn), 0, "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	count := 0
	for _, r := range rows {
		if r.AssetID != nil && *r.AssetID == f.host.AssetID {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("incidents on asset %s = %d, want exactly 1 (no duplicate)", f.host.AssetID, count)
	}

	// The timeline recorded one "created" row and one "alert_note" row.
	timeline, err := NewIncidentStore(tenantScopedPool(t)).Timeline(assetTestCtx(f.tn), id1, 0, "")
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	var created, notes int
	for _, e := range timeline {
		switch e.Kind {
		case domain.IncidentEventCreated:
			created++
		case domain.IncidentEventAlertNote:
			notes++
			if e.Actor != "system:alerting" {
				t.Errorf("alert_note actor = %q, want system:alerting", e.Actor)
			}
		}
	}
	if created != 1 || notes != 1 {
		t.Fatalf("timeline created=%d alert_note=%d, want 1 and 1", created, notes)
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

	want3, err := domain.NewAlertIncident(f.tn.TenantID, "cpu_utilization firing again", "", domain.IncidentSeverityHigh, f.host.AssetID)
	if err != nil {
		t.Fatalf("new alert incident 3: %v", err)
	}
	id3, err := f.priv.FindOrCreateOpenAlertIncident(ctx, want3, "system:alerting", "alert cpu_utilization fired")
	if err != nil {
		t.Fatalf("third find-or-create: %v", err)
	}
	if id3 == id1 {
		t.Fatalf("a firing after the prior incident was closed must create a new one, not reuse the closed %q", id1)
	}
}

// TestIncidentStore_FindOrCreateOpenAlertIncident_ManualIncidentNotLinked
// proves Source gates correlation at the store layer: a manually-filed OPEN
// incident on the same asset is never discovered by FindOrCreateOpenAlertIncident
// — mutation-verified below by loosening the unique index's own predicate.
func TestIncidentStore_FindOrCreateOpenAlertIncident_ManualIncidentNotLinked(t *testing.T) {
	f := newCorrelationFixture(t, "corr-manual")
	scoped := NewIncidentStore(tenantScopedPool(t))
	manual, err := domain.NewIncident(f.tn.TenantID, "operator filed this", "", domain.IncidentSeverityHigh, &f.host.AssetID, nil)
	if err != nil {
		t.Fatalf("new manual incident: %v", err)
	}
	created, err := scoped.Create(assetTestCtx(f.tn), manual)
	if err != nil {
		t.Fatalf("create manual incident: %v", err)
	}

	want, err := domain.NewAlertIncident(f.tn.TenantID, "cpu_utilization firing", "", domain.IncidentSeverityHigh, f.host.AssetID)
	if err != nil {
		t.Fatalf("new alert incident: %v", err)
	}
	id, err := f.priv.FindOrCreateOpenAlertIncident(context.Background(), want, "system:alerting", "alert cpu_utilization fired")
	if err != nil {
		t.Fatalf("find-or-create: %v", err)
	}
	if id == created.IncidentID {
		t.Fatalf("correlation linked to the manual incident %q — source must gate it", created.IncidentID)
	}
}

// TestIncidentStore_FindOrCreateOpenAlertIncident_ConcurrentFiringsNoDuplicate
// is the mutation-tested proof of E4.1's DB-level dedup: many goroutines
// racing FindOrCreateOpenAlertIncident for the SAME (tenant, asset) — the
// true-concurrency shape two evaluator goroutines firing different rules on
// one CI in the same tick actually produce — must settle on exactly one
// incident, never more.
func TestIncidentStore_FindOrCreateOpenAlertIncident_ConcurrentFiringsNoDuplicate(t *testing.T) {
	f := newCorrelationFixture(t, "corr-concurrent")

	const n = 20
	ids := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			want, err := domain.NewAlertIncident(f.tn.TenantID, fmt.Sprintf("metric-%d firing", i), "", domain.IncidentSeverityHigh, f.host.AssetID)
			if err != nil {
				errs[i] = err
				return
			}
			id, err := f.priv.FindOrCreateOpenAlertIncident(context.Background(), want, "system:alerting", fmt.Sprintf("alert metric-%d fired", i))
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
		if r.AssetID != nil && *r.AssetID == f.host.AssetID {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("incidents on asset %s after %d concurrent firings = %d, want exactly 1", f.host.AssetID, n, count)
	}
}

// TestIncidentStore_AppendAlertNote_TenantIsolation is the make-or-break
// mutation-tested proof for ADR-TENANCY-012 on the write side: tenant B's
// tenantID must never be able to append a note to tenant A's incident, even
// though this store runs on the PRIVILEGED pool (RLS switched off) and holds
// tenant A's own real incident_id.
func TestIncidentStore_AppendAlertNote_TenantIsolation(t *testing.T) {
	priv := testPool(t)
	tnA := assetTenant(t, NewTenantStore(priv), "corr-tenant-a")
	tnB := assetTenant(t, NewTenantStore(priv), "corr-tenant-b")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	hostA := telemetryAsset(t, assets, assetTestCtx(tnA), tnA.TenantID, "corr-tenant-a-host")

	corr := NewIncidentStore(priv)
	wantA, err := domain.NewAlertIncident(tnA.TenantID, "cpu_utilization firing", "", domain.IncidentSeverityHigh, hostA.AssetID)
	if err != nil {
		t.Fatalf("new alert incident: %v", err)
	}
	incidentA, err := corr.FindOrCreateOpenAlertIncident(context.Background(), wantA, "system:alerting", "alert cpu_utilization fired")
	if err != nil {
		t.Fatalf("create tenant A's incident: %v", err)
	}

	// Tenant B, given tenant A's real incident id, must be refused.
	err = corr.AppendAlertNote(context.Background(), tnB.TenantID, incidentA, "cross-tenant note", "system:alerting")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-tenant AppendAlertNote err = %v, want ErrNotFound — tenant B must never touch tenant A's incident", err)
	}

	// The legitimate owner can still append.
	if err := corr.AppendAlertNote(context.Background(), tnA.TenantID, incidentA, "alert cpu_utilization recovered", "system:alerting"); err != nil {
		t.Fatalf("tenant A's own AppendAlertNote: %v", err)
	}

	timeline, err := NewIncidentStore(tenantScopedPool(t)).Timeline(assetTestCtx(tnA), incidentA, 0, "")
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	notes := 0
	for _, e := range timeline {
		if e.Kind == domain.IncidentEventAlertNote {
			notes++
		}
	}
	if notes != 1 {
		t.Fatalf("alert_note rows on tenant A's incident = %d, want exactly 1 (the refused cross-tenant call must not have written one)", notes)
	}
}
