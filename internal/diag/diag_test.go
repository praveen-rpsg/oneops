package diag

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func baseOptions() Options {
	return Options{
		Service:          "oneops-controlplane",
		Version:          "1.2.3",
		Commit:           "abc123",
		MigrationVersion: "20260723000001_audit",
		StartedAt:        time.Now().Add(-90 * time.Second),
		Config: ConfigSummary{
			Env:         "production",
			HTTPAddr:    ":8080",
			ServiceName: "oneops-controlplane",
			AuthEnabled: true,
			DBMaxConns:  10,
		},
		Modules: map[string]bool{"governance": true, "audit": true},
		DBName:  "postgres",
		DBCheck: func(context.Context) error { return nil },
	}
}

func TestSnapshot_HealthyAllUp(t *testing.T) {
	b := NewBuilder(baseOptions())
	snap := b.Snapshot(context.Background())

	if !snap.Healthy {
		t.Fatalf("expected healthy snapshot: %+v", snap)
	}
	if snap.Version != "1.2.3" || snap.MigrationVersion != "20260723000001_audit" {
		t.Errorf("metadata wrong: %+v", snap)
	}
	if snap.UptimeSeconds < 89 {
		t.Errorf("uptime = %v, want >= ~90", snap.UptimeSeconds)
	}
	if len(snap.Dependencies) != 1 || !snap.Dependencies[0].Up {
		t.Errorf("dependencies = %+v", snap.Dependencies)
	}
}

func TestSnapshot_UnhealthyWhenDependencyDown(t *testing.T) {
	o := baseOptions()
	o.DBCheck = func(context.Context) error { return errors.New("connection refused") }
	snap := NewBuilder(o).Snapshot(context.Background())

	if snap.Healthy {
		t.Fatal("expected unhealthy snapshot when DB is down")
	}
	if snap.Dependencies[0].Up || !strings.Contains(snap.Dependencies[0].Error, "connection refused") {
		t.Errorf("dependency status = %+v", snap.Dependencies[0])
	}
}

func TestSnapshot_UnhealthyWhenSchedulerStalled(t *testing.T) {
	o := baseOptions()
	o.SchedulerStatus = func() SchedulerStatus {
		return SchedulerStatus{Enabled: true, HasRun: true, Stalled: true, LastHealthy: true}
	}
	snap := NewBuilder(o).Snapshot(context.Background())
	if snap.Healthy {
		t.Fatal("expected unhealthy snapshot when scheduler is stalled")
	}
}

func TestSnapshot_UnhealthyWhenIntegrityBroken(t *testing.T) {
	o := baseOptions()
	o.SchedulerStatus = func() SchedulerStatus {
		return SchedulerStatus{Enabled: true, HasRun: true, LastHealthy: false, Failures: 1}
	}
	snap := NewBuilder(o).Snapshot(context.Background())
	if snap.Healthy {
		t.Fatal("expected unhealthy snapshot on integrity break")
	}
}

func TestSnapshot_SchedulerDisabledIsHealthy(t *testing.T) {
	o := baseOptions()
	o.SchedulerStatus = func() SchedulerStatus { return SchedulerStatus{Enabled: false} }
	snap := NewBuilder(o).Snapshot(context.Background())
	if !snap.Healthy {
		t.Fatal("disabled scheduler must not make the platform unhealthy")
	}
}

func TestHandler_ServesJSONNoSecrets(t *testing.T) {
	b := NewBuilder(baseOptions())
	rec := httptest.NewRecorder()
	b.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/internal/diagnostics", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}
	var snap Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if snap.Service != "oneops-controlplane" {
		t.Errorf("service = %q", snap.Service)
	}
	// Redaction: no secret-shaped material may appear anywhere in the payload.
	body := rec.Body.String()
	for _, secret := range []string{"password", "hmac", "secret", "dev-insecure", "postgres://"} {
		if strings.Contains(strings.ToLower(body), secret) {
			t.Errorf("diagnostics payload leaked %q", secret)
		}
	}
}

func TestHandler_Returns503WhenUnhealthy(t *testing.T) {
	o := baseOptions()
	o.DBCheck = func(context.Context) error { return errors.New("down") }
	rec := httptest.NewRecorder()
	NewBuilder(o).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/internal/diagnostics", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestHandler_RejectsNonGET(t *testing.T) {
	rec := httptest.NewRecorder()
	NewBuilder(baseOptions()).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/internal/diagnostics", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}
