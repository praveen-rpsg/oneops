package sdk

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestGovernanceOperationsRouting(t *testing.T) {
	var lastPath, lastMethod string
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastPath, lastMethod = r.URL.Path, r.Method
		writeResult(w)
	}))
	ctx := context.Background()
	opts := WriteOptions{OperationID: "k"}

	ops := []struct {
		call func() (*GovernanceResult, error)
		path string
		verb string
	}{
		{func() (*GovernanceResult, error) { return c.Governance.Ratify(ctx, "c1", opts) }, "/v1/governance/c1/ratify", http.MethodPost},
		{func() (*GovernanceResult, error) { return c.Governance.Approve(ctx, "c1", opts) }, "/v1/governance/c1/approve", http.MethodPost},
		{func() (*GovernanceResult, error) { return c.Governance.Suspend(ctx, "c1", opts) }, "/v1/governance/c1/suspend", http.MethodPost},
		{func() (*GovernanceResult, error) { return c.Governance.Deprecate(ctx, "c1", opts) }, "/v1/governance/c1/deprecate", http.MethodPost},
		{func() (*GovernanceResult, error) { return c.Governance.Withdraw(ctx, "c1", opts) }, "/v1/governance/c1/withdraw", http.MethodPost},
		{func() (*GovernanceResult, error) { return c.Governance.Archive(ctx, "c1", RetentionAuditRecord, opts) }, "/v1/governance/c1/archive", http.MethodPost},
		{func() (*GovernanceResult, error) { return c.Governance.Delete(ctx, "c1", opts) }, "/v1/governance/c1", http.MethodDelete},
	}
	for _, o := range ops {
		if _, err := o.call(); err != nil {
			t.Fatalf("%s: %v", o.path, err)
		}
		if lastPath != o.path || lastMethod != o.verb {
			t.Errorf("got %s %s, want %s %s", lastMethod, lastPath, o.verb, o.path)
		}
	}
}

func TestArchiveBodyAndValidation(t *testing.T) {
	var gotBody map[string]string
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		writeResult(w)
	}))
	if _, err := c.Governance.Archive(context.Background(), "c1", RetentionAuditRecord, WriteOptions{OperationID: "k"}); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if gotBody["target_retention"] != RetentionAuditRecord {
		t.Fatalf("body = %v", gotBody)
	}
	// Missing retention -> client-side error, no request.
	if _, err := c.Governance.Archive(context.Background(), "c1", "", WriteOptions{OperationID: "k"}); err == nil {
		t.Fatal("expected error for empty target retention")
	}
}

func TestQueryPaginationAndFilter(t *testing.T) {
	var gotQuery string
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(HistoryPage{Items: []HistoryItem{{Seq: 1, Operation: "ratification"}}, NextCursor: "1"})
	}))
	page, err := c.Query.History(context.Background(), "c1", PageOptions{Limit: 1, Cursor: "0", Operation: "ratification"})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if page.NextCursor != "1" || len(page.Items) != 1 {
		t.Fatalf("page = %+v", page)
	}
	for _, want := range []string{"limit=1", "cursor=0", "operation=ratification"} {
		if !contains(gotQuery, want) {
			t.Errorf("query %q missing %q", gotQuery, want)
		}
	}
}

func TestEventsOrder(t *testing.T) {
	var gotQuery string
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(EventsPage{Order: "desc"})
	}))
	if _, err := c.Query.Events(context.Background(), "c1", EventsOptions{Order: "desc", Limit: 5}); err != nil {
		t.Fatalf("Events: %v", err)
	}
	if !contains(gotQuery, "order=desc") || !contains(gotQuery, "limit=5") {
		t.Fatalf("query = %q", gotQuery)
	}
}

func TestAdminEndpoints(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/admin/status":
			_ = json.NewEncoder(w).Encode(AdminStatus{Version: "1.2.3", Healthy: true})
		case "/v1/admin/integrity":
			_ = json.NewEncoder(w).Encode(AdminIntegrity{Enabled: true, ChainsOK: 3})
		case "/v1/admin/integrity/run":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			_ = json.NewEncoder(w).Encode(IntegrityRun{ChainsTotal: 3, Healthy: true})
		case "/v1/admin/metrics":
			_ = json.NewEncoder(w).Encode(MetricsSummary{Counters: map[string]float64{"http_requests_total": 5}})
		case "/v1/admin/config":
			_ = json.NewEncoder(w).Encode(AdminConfig{Modules: map[string]bool{"governance": true}})
		case "/v1/admin/report":
			_ = json.NewEncoder(w).Encode(Report{Metrics: map[string]float64{"x": 1}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	ctx := context.Background()

	if s, err := c.Admin.Status(ctx); err != nil || s.Version != "1.2.3" || !s.Healthy {
		t.Fatalf("Status: %+v %v", s, err)
	}
	if i, err := c.Admin.Integrity(ctx); err != nil || !i.Enabled || i.ChainsOK != 3 {
		t.Fatalf("Integrity: %+v %v", i, err)
	}
	if run, err := c.Admin.RunIntegrity(ctx); err != nil || !run.Healthy || run.ChainsTotal != 3 {
		t.Fatalf("RunIntegrity: %+v %v", run, err)
	}
	if m, err := c.Admin.Metrics(ctx); err != nil || m.Counters["http_requests_total"] != 5 {
		t.Fatalf("Metrics: %+v %v", m, err)
	}
	if cfg, err := c.Admin.Configuration(ctx); err != nil || !cfg.Modules["governance"] {
		t.Fatalf("Configuration: %+v %v", cfg, err)
	}
	if rep, err := c.Admin.Report(ctx); err != nil || rep.Metrics["x"] != 1 {
		t.Fatalf("Report: %+v %v", rep, err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
