package sdk

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestPoliciesClient(t *testing.T) {
	var lastMethod, lastPath string
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastMethod, lastPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/admin/policies":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(Policy{ID: "pol_1", Name: "p", Enabled: true, Action: PolicyActionSpec{Type: "http"}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/admin/policies":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []Policy{{ID: "pol_1"}}})
		case r.Method == http.MethodPatch:
			_ = json.NewEncoder(w).Encode(Policy{ID: "pol_1", Enabled: false})
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/v1/admin/policies/pol_1/executions":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []PolicyExecution{{ID: "pex_1", Status: "succeeded"}}})
		case r.URL.Path == "/v1/admin/policies/pol_1/test":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "succeeded"})
		}
	}))
	pc := c.Policies()
	ctx := context.Background()

	created, err := pc.CreatePolicy(ctx, CreatePolicyInput{Name: "p", Action: PolicyActionSpec{Type: "http"},
		Condition: PolicyCondition{Operations: []string{"ratification"}}})
	if err != nil || created.ID != "pol_1" {
		t.Fatalf("CreatePolicy: %+v %v", created, err)
	}
	if list, err := pc.List(ctx); err != nil || len(list) != 1 {
		t.Fatalf("List: %+v %v", list, err)
	}
	if upd, err := pc.UpdatePolicy(ctx, "pol_1", UpdatePolicyInput{Enabled: boolPtr(false)}); err != nil || upd.Enabled {
		t.Fatalf("UpdatePolicy: %+v %v", upd, err)
	}
	if err := pc.DeletePolicy(ctx, "pol_1"); err != nil {
		t.Fatalf("DeletePolicy: %v", err)
	}
	if xs, err := pc.Executions(ctx, "pol_1", 10); err != nil || len(xs) != 1 || xs[0].Status != "succeeded" {
		t.Fatalf("Executions: %+v %v", xs, err)
	}
	status, err := pc.TestPolicy(ctx, "pol_1")
	if err != nil || status != "succeeded" || lastMethod != http.MethodPost || lastPath != "/v1/admin/policies/pol_1/test" {
		t.Fatalf("TestPolicy: %q %v", status, err)
	}
}

func boolPtr(b bool) *bool { return &b }
