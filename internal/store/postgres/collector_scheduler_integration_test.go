//go:build integration

package postgres

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/collector"
	"github.com/rpsg/oneops/internal/domain"
)

// TestCollectorScheduler_EndToEnd_RunsADueCheckAndWritesSamples wires the real
// leader-gated scheduler over the real database — CollectorCheckStore's
// due-scan/record-run built on the PRIVILEGED pool, TelemetryStore built on
// the PRIVILEGED pool (exactly as cmd/controlplane/main.go wires them) — and
// proves a due "http" check against a real local server produces the
// expected up/latency_ms/status_code samples, tenant-isolated and readable
// back through the tenant-scoped TelemetryStore.
func TestCollectorScheduler_EndToEnd_RunsADueCheckAndWritesSamples(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "collector-e2e")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	scopedChecks := NewCollectorCheckStore(scoped)
	scopedTelemetry := NewTelemetryStore(scoped)
	ctx := assetTestCtx(tn)
	host := telemetryAsset(t, assets, ctx, tn.TenantID, "e2e-host")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := domain.NewCollectorCheck(tn.TenantID, host.AssetID, domain.CollectorCheckTypeHTTP, srv.URL, 30, "e2e", "")
	if err != nil {
		t.Fatalf("new collector check: %v", err)
	}
	created, err := scopedChecks.Create(ctx, c)
	if err != nil {
		t.Fatalf("create check: %v", err)
	}

	privChecks := NewCollectorCheckStore(priv)
	privTelemetry := NewTelemetryStore(priv)
	sched := collector.NewScheduler(privChecks, privTelemetry, http.DefaultClient, nil, quietRollupLogger(), collector.Config{})

	sched.RunOnce(context.Background())

	base := time.Now().UTC()
	got, err := scopedTelemetry.QueryRange(ctx, host.AssetID, "e2e_up", base.Add(-time.Hour), base.Add(time.Hour), 0, time.Time{}, domain.ResolutionRaw)
	if err != nil {
		t.Fatalf("query e2e_up: %v", err)
	}
	if len(got) != 1 || got[0].Value != 1 {
		t.Fatalf("e2e_up samples = %+v, want exactly one with value 1", got)
	}

	gotStatus, err := scopedTelemetry.QueryRange(ctx, host.AssetID, "e2e_status_code", base.Add(-time.Hour), base.Add(time.Hour), 0, time.Time{}, domain.ResolutionRaw)
	if err != nil {
		t.Fatalf("query e2e_status_code: %v", err)
	}
	if len(gotStatus) != 1 || gotStatus[0].Value != 200 {
		t.Fatalf("e2e_status_code samples = %+v, want exactly one with value 200", gotStatus)
	}

	gotLatency, err := scopedTelemetry.QueryRange(ctx, host.AssetID, "e2e_latency_ms", base.Add(-time.Hour), base.Add(time.Hour), 0, time.Time{}, domain.ResolutionRaw)
	if err != nil {
		t.Fatalf("query e2e_latency_ms: %v", err)
	}
	if len(gotLatency) != 1 {
		t.Fatalf("e2e_latency_ms samples = %+v, want exactly one", gotLatency)
	}

	updated, err := scopedChecks.Get(ctx, created.CheckID)
	if err != nil {
		t.Fatalf("get after run: %v", err)
	}
	if updated.LastStatus != domain.CollectorCheckStatusOK {
		t.Errorf("last_status = %q, want ok", updated.LastStatus)
	}
	if updated.LastRunAt == nil {
		t.Error("last_run_at was not set")
	}
	if updated.RowVersion != created.RowVersion {
		t.Errorf("row_version changed from %d to %d after the scheduler ran, want unchanged", created.RowVersion, updated.RowVersion)
	}

	// The scheduler must not double-poll a check that already ran within its
	// interval: a second pass immediately after the first finds nothing due.
	due, err := privChecks.DueChecks(context.Background(), time.Now().UTC(), 100)
	if err != nil {
		t.Fatalf("due checks after run: %v", err)
	}
	for _, d := range due {
		if d.CheckID == created.CheckID {
			t.Errorf("the just-run check is still due: %+v", d)
		}
	}
}

// TestCollectorScheduler_EndToEnd_DownTargetRecordsUpZero proves the
// "failed/timed-out check records up=0, not an error that drops the sample"
// requirement against the real database and a target nothing listens on.
func TestCollectorScheduler_EndToEnd_DownTargetRecordsUpZero(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "collector-e2e-down")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	scopedChecks := NewCollectorCheckStore(scoped)
	scopedTelemetry := NewTelemetryStore(scoped)
	ctx := assetTestCtx(tn)
	host := telemetryAsset(t, assets, ctx, tn.TenantID, "e2e-down-host")

	c, err := domain.NewCollectorCheck(tn.TenantID, host.AssetID, domain.CollectorCheckTypeHTTP, "http://127.0.0.1:1", 30, "e2edown", "")
	if err != nil {
		t.Fatalf("new collector check: %v", err)
	}
	if _, err := scopedChecks.Create(ctx, c); err != nil {
		t.Fatalf("create check: %v", err)
	}

	privChecks := NewCollectorCheckStore(priv)
	privTelemetry := NewTelemetryStore(priv)
	sched := collector.NewScheduler(privChecks, privTelemetry, http.DefaultClient, nil, quietRollupLogger(), collector.Config{CheckTimeout: 2 * time.Second})

	sched.RunOnce(context.Background())

	base := time.Now().UTC()
	got, err := scopedTelemetry.QueryRange(ctx, host.AssetID, "e2edown_up", base.Add(-time.Hour), base.Add(time.Hour), 0, time.Time{}, domain.ResolutionRaw)
	if err != nil {
		t.Fatalf("query e2edown_up: %v", err)
	}
	if len(got) != 1 || got[0].Value != 0 {
		t.Fatalf("e2edown_up samples = %+v, want exactly one with value 0", got)
	}

	statusSamples, err := scopedTelemetry.QueryRange(ctx, host.AssetID, "e2edown_status_code", base.Add(-time.Hour), base.Add(time.Hour), 0, time.Time{}, domain.ResolutionRaw)
	if err != nil {
		t.Fatalf("query e2edown_status_code: %v", err)
	}
	if len(statusSamples) != 0 {
		t.Errorf("e2edown_status_code samples = %+v, want none — no response was ever received", statusSamples)
	}
}
