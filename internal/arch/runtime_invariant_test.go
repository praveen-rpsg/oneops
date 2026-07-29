package arch

import (
	"os"
	"strings"
	"testing"
)

// A security invariant must be a continuously-held property, not a boot-time
// event.
//
// The schema validator proves that row-level security is enabled and forced,
// that ownership columns are mandatory, and that the audit log's append-only
// guards exist. Every one of those is runtime-mutable: one ALTER, one careless
// migration, one restore from an older dump. Proven live — with row-level
// security disabled after startup, a tenant read another tenant's rows through
// the tenant-scoped pool while the process reported ready and logged nothing.
//
// So the validator must be invoked from a continuous sentinel, not only from the
// startup sequence (ADR-SECURITY-002).
func TestSchemaValidator_IsRunContinuouslyNotOnlyAtStartup(t *testing.T) {
	src := readFile(t, "../../cmd/controlplane/main.go")

	if !strings.Contains(stripComments(src), "ops.NewSentinel(") {
		t.Fatal("main.go constructs no sentinel — the schema invariants would be proven once at " +
			"boot and never re-verified, which is how a disabled RLS policy became a silent, " +
			"total cross-tenant leak (ADR-SECURITY-002)")
	}
	if !strings.Contains(stripComments(src), "schemaSentinel.Run(ctx)") {
		t.Error("the schema sentinel is constructed but never started; a sentinel that does not " +
			"run verifies nothing (ADR-SECURITY-002)")
	}
	// The sentinel must re-run the *same* validator startup uses. Two different
	// checks would let boot and runtime disagree about what "valid" means.
	if !strings.Contains(stripComments(src), "postgres.NewSchemaValidator(pool).Validate(c)") {
		t.Error("the sentinel does not re-run the startup SchemaValidator — boot and runtime must " +
			"enforce one definition of the invariant (ADR-SECURITY-002)")
	}
}

// Detection without refusal is just logging. A breach must close the
// tenant-data surface, exactly as the same problem refuses to boot.
func TestInvariantBreach_FailsClosedOnEveryTenantDataPath(t *testing.T) {
	main := readFile(t, "../../cmd/controlplane/main.go")
	server := readFile(t, "../httpapi/server.go")

	// 1. HTTP: the /v1 group is gated. Checked on non-comment source — a
	//    commented-out `rt.Use(s.invariantGate)` still contains the substring,
	//    and would leave this assertion passing over a disabled gate.
	if !strings.Contains(stripComments(server), "rt.Use(s.invariantGate)") {
		t.Error("the /v1 route group is not gated on the invariant — the tenant-data surface " +
			"would keep serving through a broken isolation boundary (ADR-SECURITY-002)")
	}
	if !strings.Contains(stripComments(main), "srv.SetInvariantGate(") {
		t.Error("main.go never wires the invariant gate, so the gate is inert (ADR-SECURITY-002)")
	}

	// 2. Readiness: a refusing instance must leave the load balancer.
	if !strings.Contains(stripComments(main), "schemaSentinel.Err()") {
		t.Error("readiness does not consult the sentinel — an instance refusing every request " +
			"would still advertise itself as ready (ADR-SECURITY-002)")
	}

	// 3. Workers: the relay, dispatcher and executor touch tenant-owned rows
	//    through the same privileged path, so gating HTTP alone leaves a hole.
	if !strings.Contains(stripComments(main), "ops.RunWhileHealthy(") {
		t.Error("background workers are not gated on the invariant — refusing HTTP while the " +
			"relay keeps fanning out across a broken isolation boundary is a gate with a hole " +
			"in it (ADR-SECURITY-002)")
	}
}

// "Unknown" must read as "do not serve". A sentinel that reports healthy before
// its first check would open the gate on an unverified boundary.
func TestSentinel_TreatsUnverifiedAsUnhealthy(t *testing.T) {
	src := readFile(t, "../ops/sentinel.go")
	if !strings.Contains(src, "if !s.verified {") {
		t.Error("Sentinel.Err does not fail closed while unverified — the gate would open before " +
			"the invariant has ever been checked in this process (ADR-SECURITY-002)")
	}
	// A failed check is not a breach: treating an unreachable database as one
	// would take every replica out of service on a transient blip.
	if !strings.Contains(src, "carrying previous verdict") {
		t.Error("Sentinel does not distinguish a failed check from a breach — a database blip " +
			"would take the fleet down, and an operator would learn to ignore the signal " +
			"(ADR-SECURITY-002)")
	}
}

// stripComments removes // line comments so an assertion cannot be satisfied by
// the very code it is meant to require, commented out.
func stripComments(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// readFile fails the test when the file is missing. A helper that returned ""
// on error would make every strings.Contains assertion below vacuously pass the
// moment a path drifted — the tests would go green precisely when they stopped
// checking anything.
func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (an architecture test cannot assert on a file it cannot read)", path, err)
	}
	return string(raw)
}
