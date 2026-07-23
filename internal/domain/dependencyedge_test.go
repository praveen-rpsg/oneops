package domain

import "testing"

func TestEdgeKindValid(t *testing.T) {
	valid := []EdgeKind{EdgeKindDepends, EdgeKindExtends, EdgeKindSupersedes}
	for _, k := range valid {
		if !k.Valid() {
			t.Errorf("expected %q to be valid", k)
		}
	}
	invalid := []EdgeKind{"", "Depends", "requires", "references", "supersede", "DEPENDS"}
	for _, k := range invalid {
		if k.Valid() {
			t.Errorf("expected %q to be invalid", k)
		}
	}
}

func TestDependencyEdgeValidate(t *testing.T) {
	tests := []struct {
		name      string
		edge      DependencyEdge
		wantField string // "" means valid
	}{
		{"valid depends", DependencyEdge{FromCfg: "a", ToCfg: "b", EdgeKind: EdgeKindDepends}, ""},
		{"valid extends", DependencyEdge{FromCfg: "a", ToCfg: "b", EdgeKind: EdgeKindExtends}, ""},
		{"valid supersedes", DependencyEdge{FromCfg: "a", ToCfg: "b", EdgeKind: EdgeKindSupersedes}, ""},
		{"empty from", DependencyEdge{FromCfg: " ", ToCfg: "b", EdgeKind: EdgeKindDepends}, "from_cfg"},
		{"empty to", DependencyEdge{FromCfg: "a", ToCfg: "", EdgeKind: EdgeKindDepends}, "to_cfg"},
		{"unknown kind", DependencyEdge{FromCfg: "a", ToCfg: "b", EdgeKind: "requires"}, "edge_kind"},
		{"blank kind", DependencyEdge{FromCfg: "a", ToCfg: "b", EdgeKind: ""}, "edge_kind"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.edge.Validate()
			if tc.wantField == "" {
				if err != nil {
					t.Fatalf("expected valid, got %v", err)
				}
				return
			}
			ve, ok := AsValidation(err)
			if !ok {
				t.Fatalf("expected *ValidationError, got %v", err)
			}
			if ve.Field != tc.wantField {
				t.Errorf("expected field %q, got %q", tc.wantField, ve.Field)
			}
		})
	}
}
