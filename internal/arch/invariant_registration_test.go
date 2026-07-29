package arch

import (
	"regexp"
	"strings"
	"testing"
)

// Every platform invariant is enforced at both points, from one definition.
//
// ADR-SECURITY-002 established that a boundary verified only at boot is assumed
// rather than enforced, and put the schema check under a sentinel. The ownership
// check was left at startup only, so the platform treated an identical condition
// as fatal at 09:00 and invisible at 09:01 — proven live: a database with a
// divergent ownership graph served `/v1` with `readyz=200`, while the same
// database refused to boot with *"refusing to start: the ownership graph is
// inconsistent"*.
//
// The class was not eliminated by adding a second sentinel call; it is
// eliminated by making both enforcement points read one registry, so a new
// invariant cannot be wired to one and not the other (ADR-SECURITY-003).
func TestPlatformInvariants_AreEnforcedAtBothPoints(t *testing.T) {
	src := stripComments(readFile(t, "../../cmd/controlplane/main.go"))

	if !strings.Contains(src, "platformInvariants := []ops.Invariant{") {
		t.Fatal("main.go declares no platform invariant registry — startup and the sentinel would " +
			"each carry their own list and drift (ADR-SECURITY-003)")
	}
	// Startup gate reads the registry.
	if !strings.Contains(src, "ops.CheckAll(rootCtx, platformInvariants)") {
		t.Error("the startup gate does not evaluate the invariant registry, so an invariant could " +
			"be re-verified at runtime but not refuse a boot (ADR-SECURITY-003)")
	}
	// Sentinel reads the same registry.
	if !strings.Contains(src, "ops.CheckAll(c, platformInvariants)") {
		t.Error("the sentinel does not evaluate the invariant registry, so an invariant could " +
			"refuse a boot but never be re-verified afterwards — the defect this closes " +
			"(ADR-SECURITY-003)")
	}
	// And nothing may be validated outside it.
	if strings.Contains(src, ".Validate(rootCtx)") {
		t.Error("main.go validates something directly at startup instead of registering it as a " +
			"platform invariant — that check would be boot-only, which is the eliminated class " +
			"(ADR-SECURITY-003)")
	}
}

// Every validator the platform defines must be registered. A validator that
// exists but is not in the registry is a boundary nobody checks — the same class
// arriving by omission rather than by design.
func TestEveryValidator_IsRegisteredAsAnInvariant(t *testing.T) {
	main := stripComments(readFile(t, "../../cmd/controlplane/main.go"))

	ctor := regexp.MustCompile(`func (New\w*Validator)\(`)
	found := 0
	for _, file := range []string{
		"../store/postgres/schema_validation.go",
		"../store/postgres/ownership_validation.go",
	} {
		for _, m := range ctor.FindAllStringSubmatch(readFile(t, file), -1) {
			name := m[1]
			found++
			if !strings.Contains(main, "postgres."+name+"(") {
				t.Errorf("%s is defined but never registered as a platform invariant — the boundary "+
					"it validates would be checked by nothing (ADR-SECURITY-003)", name)
			}
		}
	}
	if found == 0 {
		t.Fatal("no validator constructors found; the assertion would be vacuous")
	}
	t.Logf("validators registered as platform invariants: %d", found)
}

// CheckAll must preserve order and stop at the first failure. The ownership
// check queries columns the schema check proves exist, so running it against a
// schema already known broken yields a database error instead of a finding and
// buries the real problem.
func TestCheckAll_ShortCircuitsInOrder(t *testing.T) {
	src := stripComments(readFile(t, "../ops/sentinel.go"))
	body := src[strings.Index(src, "func CheckAll("):]

	if !strings.Contains(body, "for _, inv := range invariants {") {
		t.Fatal("CheckAll does not iterate the registry in order (ADR-SECURITY-003)")
	}
	if !strings.Contains(body, "if len(problems) > 0 {") || !strings.Contains(body, "return out, nil") {
		t.Error("CheckAll does not stop at the first failing invariant — a broken schema would let " +
			"the ownership check run against columns it cannot rely on (ADR-SECURITY-003)")
	}
	if !strings.Contains(body, "inv.Name + \": \" + p") {
		t.Error("CheckAll does not name the failing invariant in its problems, so a breach report " +
			"would not say which boundary failed (ADR-SECURITY-003)")
	}
}
