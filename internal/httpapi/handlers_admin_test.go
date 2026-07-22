package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/rpsg/oneops/internal/auth"
	"github.com/rpsg/oneops/internal/config"
	"github.com/rpsg/oneops/internal/diag"
	"github.com/rpsg/oneops/internal/observability"
)

func fakeSnapshot() diag.Snapshot {
	return diag.Snapshot{
		Service: "oneops-controlplane", Version: "1.2.3", Commit: "abc", Env: "production",
		MigrationVersion: "20260723000001_audit", UptimeSeconds: 42, Healthy: true,
		Modules: map[string]bool{"governance": true, "audit": true},
		Config: diag.ConfigSummary{
			Env: "production", ServiceName: "oneops-controlplane", AuthEnabled: true,
			DBMaxConns: 10, PProfEnabled: false, VerifyIntervalSeconds: 300,
		},
		Scheduler:    diag.SchedulerStatus{Enabled: true, HasRun: true, LastHealthy: true, ChainsTotal: 3, ChainsOK: 3},
		Dependencies: []diag.DependencyStatus{{Name: "postgres", Up: true}},
	}
}

func newAdminAPI(t *testing.T, authEnabled, wire bool) (http.Handler, *int) {
	t.Helper()
	cfg := &config.Config{
		HTTPAddr: ":0", DefaultPageSize: 50, MaxPageSize: 200,
		AuthEnabled: authEnabled, JWTIssuer: tIss, JWTAudience: tAud, JWTHMACKey: tSecret,
	}
	metrics := observability.NewMetrics()
	s := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		newFakeRepo(), newFakeIdem(), auth.NewVerifier(tIss, tAud, tSecret, ""),
		metrics, func(context.Context) error { return nil })

	runs := 0
	if wire {
		s.SetGovernanceQuery(&fakeAuditRead{}, fakeChainVerifier{}, func() SchedulerView {
			return SchedulerView{Enabled: true, HasRun: true, LastHealthy: true, ChainsTotal: 3, ChainsOK: 3}
		})
		s.SetAdmin(
			func(context.Context) diag.Snapshot { return fakeSnapshot() },
			func(context.Context) AdminIntegrityRun {
				runs++
				return AdminIntegrityRun{ChainsTotal: 3, ChainsOK: 3, Healthy: true}
			},
		)
	}
	return s.Router(), &runs
}

func adminHdr(t *testing.T) map[string]string {
	return map[string]string{"Authorization": "Bearer " + mintToken(t, []string{"oneops-admin"})}
}

func TestAdminStatus(t *testing.T) {
	h, _ := newAdminAPI(t, false, true)
	rec := do(h, http.MethodGet, "/v1/admin/status", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp adminStatusResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Version != "1.2.3" || resp.MigrationVersion != "20260723000001_audit" ||
		!resp.Scheduler.Enabled || !resp.VerifierHealthy || !resp.Healthy {
		t.Fatalf("status = %+v", resp)
	}
	if len(resp.Dependencies) != 1 || !resp.Dependencies[0].Up {
		t.Fatalf("dependencies = %+v", resp.Dependencies)
	}
}

func TestAdminIntegrityAndRun(t *testing.T) {
	h, runs := newAdminAPI(t, false, true)

	g := do(h, http.MethodGet, "/v1/admin/integrity", nil, nil)
	if g.Code != http.StatusOK {
		t.Fatalf("integrity GET status = %d", g.Code)
	}
	var summary adminIntegrityResponse
	_ = json.Unmarshal(g.Body.Bytes(), &summary)
	if !summary.Enabled || summary.ChainsTotal != 3 || summary.ChainsOK != 3 {
		t.Fatalf("integrity summary = %+v", summary)
	}

	p := do(h, http.MethodPost, "/v1/admin/integrity/run", nil, nil)
	if p.Code != http.StatusOK {
		t.Fatalf("integrity run status = %d", p.Code)
	}
	if *runs != 1 {
		t.Fatalf("scheduler sweep invoked %d times, want 1", *runs)
	}
	var run AdminIntegrityRun
	_ = json.Unmarshal(p.Body.Bytes(), &run)
	if !run.Healthy || run.ChainsTotal != 3 {
		t.Fatalf("run result = %+v", run)
	}
}

func TestAdminConfigRedaction(t *testing.T) {
	h, _ := newAdminAPI(t, false, true)
	rec := do(h, http.MethodGet, "/v1/admin/config", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp adminConfigResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if !resp.Modules["governance"] || !resp.Config.AuthEnabled {
		t.Fatalf("config = %+v", resp)
	}
	// No secret-shaped material anywhere.
	body := strings.ToLower(rec.Body.String())
	for _, secret := range []string{"password", "hmac", "secret", "postgres://", "dev-insecure"} {
		if strings.Contains(body, secret) {
			t.Errorf("admin config leaked %q", secret)
		}
	}
}

func TestAdminMetricsSummary(t *testing.T) {
	h, _ := newAdminAPI(t, false, true)
	// Generate one request so http_requests_total is non-zero.
	do(h, http.MethodGet, "/v1/admin/status", nil, nil)

	rec := do(h, http.MethodGet, "/v1/admin/metrics", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp struct {
		Counters map[string]float64 `json:"counters"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if _, ok := resp.Counters["http_requests_total"]; !ok {
		t.Fatalf("metrics summary missing http_requests_total: %+v", resp.Counters)
	}
}

func TestAdminReport(t *testing.T) {
	h, _ := newAdminAPI(t, false, true)
	rec := do(h, http.MethodGet, "/v1/admin/report", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp adminReportResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("report not valid JSON: %v", err)
	}
	if resp.Diagnostics.Version != "1.2.3" || resp.Metrics == nil {
		t.Fatalf("report = %+v", resp)
	}
}

func TestAdminRBAC(t *testing.T) {
	h, _ := newAdminAPI(t, true, true)
	// No token -> 401.
	if rec := do(h, http.MethodGet, "/v1/admin/status", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d, want 401", rec.Code)
	}
	// Editor (no admin perm) -> 403.
	editor := map[string]string{"Authorization": "Bearer " + mintToken(t, []string{"oneops-editor"})}
	if rec := do(h, http.MethodGet, "/v1/admin/status", nil, editor); rec.Code != http.StatusForbidden {
		t.Fatalf("editor: status = %d, want 403", rec.Code)
	}
	// Admin -> 200.
	if rec := do(h, http.MethodGet, "/v1/admin/status", nil, adminHdr(t)); rec.Code != http.StatusOK {
		t.Fatalf("admin: status = %d, want 200", rec.Code)
	}
	// The write-ish endpoint also requires admin.
	if rec := do(h, http.MethodPost, "/v1/admin/integrity/run", nil, editor); rec.Code != http.StatusForbidden {
		t.Fatalf("editor run: status = %d, want 403", rec.Code)
	}
}

func TestAdminUnwiredReturns500(t *testing.T) {
	h, _ := newAdminAPI(t, false, false)
	if rec := do(h, http.MethodGet, "/v1/admin/status", nil, nil); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if rec := do(h, http.MethodPost, "/v1/admin/integrity/run", nil, nil); rec.Code != http.StatusInternalServerError {
		t.Fatalf("run status = %d, want 500", rec.Code)
	}
}

func TestAdminOpenAPICoverage(t *testing.T) {
	h, _ := newAdminAPI(t, false, true)
	spec := do(h, http.MethodGet, "/openapi.yaml", nil, nil).Body.String()
	for _, want := range []string{
		"adminStatus", "adminIntegrity", "adminIntegrityRun", "adminMetrics",
		"adminConfig", "adminReport", "AdminStatusResponse", "AdminReportResponse",
	} {
		if !strings.Contains(spec, want) {
			t.Errorf("openapi.yaml missing %q", want)
		}
	}
}
