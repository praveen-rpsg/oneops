package domain

import "testing"

func TestDirectionValid(t *testing.T) {
	if !DirectionDependencies.Valid() || !DirectionDependents.Valid() {
		t.Fatal("expected known directions to be valid")
	}
	if Direction("sideways").Valid() || Direction("").Valid() {
		t.Fatal("expected unknown directions to be invalid")
	}
}

func TestTraversalResultAccessors(t *testing.T) {
	r := &TraversalResult{
		Root:      "root",
		Direction: DirectionDependencies,
		Nodes: []TraversalNode{
			{CfgID: "a", Depth: 1},
			{CfgID: "b", Depth: 1},
			{CfgID: "c", Depth: 3},
		},
	}
	if r.Count() != 3 {
		t.Errorf("Count = %d, want 3", r.Count())
	}
	if r.MaxDepth() != 3 {
		t.Errorf("MaxDepth = %d, want 3", r.MaxDepth())
	}
	got := r.IDs()
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("IDs len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("IDs[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	empty := &TraversalResult{}
	if empty.Count() != 0 || empty.MaxDepth() != 0 || len(empty.IDs()) != 0 {
		t.Error("empty result accessors should be zero-valued")
	}
}

func TestGraphPath(t *testing.T) {
	p := GraphPath{Nodes: []string{"a", "b", "c"}}
	if p.Len() != 3 {
		t.Errorf("Len = %d, want 3", p.Len())
	}
	if p.String() != "a -> b -> c" {
		t.Errorf("String = %q", p.String())
	}
	if (GraphPath{}).String() != "" {
		t.Error("empty path should render empty")
	}
}
