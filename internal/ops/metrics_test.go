package ops

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/rpsg/oneops/internal/domain"
)

func TestPromMetrics_ExposedAndCounted(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewPromMetrics(reg)

	m.ChainVerified("a", domain.VerifyResult{OK: true}, 10*time.Millisecond)
	m.ChainVerified("b", domain.VerifyResult{OK: false}, 20*time.Millisecond) // a break
	m.ChainError("c", 5*time.Millisecond)
	m.RunCompleted(IntegrityReport{Failures: []ChainFailure{{ChainID: "b"}}})

	if got := testutil.ToFloat64(m.chains); got != 2 {
		t.Errorf("chains_verified_total = %v, want 2", got)
	}
	if got := testutil.ToFloat64(m.failures); got != 1 {
		t.Errorf("verification_failures_total = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.errors); got != 1 {
		t.Errorf("verification_errors_total = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.runs); got != 1 {
		t.Errorf("verification_runs_total = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.integrityOK); got != 0 {
		t.Errorf("integrity_ok = %v, want 0 (unhealthy run)", got)
	}

	// A healthy run flips the gauge back to 1.
	m.RunCompleted(IntegrityReport{})
	if got := testutil.ToFloat64(m.integrityOK); got != 1 {
		t.Errorf("integrity_ok = %v, want 1 (healthy run)", got)
	}

	// All seven collectors are registered and exposed.
	if n := testutil.CollectAndCount(reg); n < 7 {
		t.Errorf("collectors exposed = %d, want >= 7", n)
	}
}
