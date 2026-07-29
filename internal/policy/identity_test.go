package policy

import "testing"

// TestExecutionID_DeterministicAndIdempotent mirrors the delivery-id guarantee
// (ADR-CONCURRENCY-003): a re-processed event produces the same execution id and
// collides rather than running the policy action a second time.
func TestExecutionID_DeterministicAndIdempotent(t *testing.T) {
	a := ExecutionID("pol_1", "chain_9", 42)
	if a != ExecutionID("pol_1", "chain_9", 42) {
		t.Fatal("same (policy,chain,seq) produced different execution ids — production is not idempotent")
	}
	if a[:5] != "exec_" {
		t.Fatalf("execution id lacks exec_ prefix: %q", a)
	}
	for _, d := range []struct {
		name    string
		pol, ch string
		seq     int64
	}{
		{"different policy", "pol_2", "chain_9", 42},
		{"different chain", "pol_1", "chain_8", 42},
		{"different seq", "pol_1", "chain_9", 43},
	} {
		if ExecutionID(d.pol, d.ch, d.seq) == a {
			t.Errorf("%s collided with base id — distinct executions must be distinct", d.name)
		}
	}
	if ExecutionID("a", "bc", 1) == ExecutionID("ab", "c", 1) {
		t.Error("field boundary is ambiguous")
	}
}
