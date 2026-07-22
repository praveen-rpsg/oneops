package sdk

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestComplianceClient(t *testing.T) {
	var lastPath string
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/admin/compliance/c1":
			_ = json.NewEncoder(w).Encode(ComplianceSummary{GovernanceID: "c1", Compliant: true, ChecksPassed: 6, ChecksTotal: 6})
		case "/v1/admin/compliance/c1/evidence":
			_ = json.NewEncoder(w).Encode(Evidence{GovernanceID: "c1", Compliant: true, CorrelationIDs: []string{"evt_1"}})
		case "/v1/admin/compliance/c1/checks":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []ComplianceCheck{{ID: "audit-chain-verified", Passed: true}}})
		case "/v1/admin/compliance/reports":
			_ = json.NewEncoder(w).Encode(ComplianceReportPage{Items: []ComplianceSummary{{GovernanceID: "c1", Compliant: true}}, NextCursor: "c2"})
		}
	}))
	cc := c.Compliance()
	ctx := context.Background()

	if sum, err := cc.Summary(ctx, "c1"); err != nil || !sum.Compliant || sum.ChecksTotal != 6 {
		t.Fatalf("Summary: %+v %v", sum, err)
	}
	if ev, err := cc.Evidence(ctx, "c1"); err != nil || !ev.Compliant || len(ev.CorrelationIDs) != 1 {
		t.Fatalf("Evidence: %+v %v", ev, err)
	}
	if checks, err := cc.Checks(ctx, "c1"); err != nil || len(checks) != 1 || !checks[0].Passed {
		t.Fatalf("Checks: %+v %v", checks, err)
	}
	rep, err := cc.Reports(ctx, "", 10)
	if err != nil || len(rep.Items) != 1 || rep.NextCursor != "c2" || lastPath != "/v1/admin/compliance/reports" {
		t.Fatalf("Reports: %+v %v path=%q", rep, err, lastPath)
	}
}
