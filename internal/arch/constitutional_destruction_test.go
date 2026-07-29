package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// Destroying a Configuration Object is a §8 constitutional operation, and the
// Governance Engine is its only door.
//
// The registry exposed a second one. `DELETE /v1/artifacts/{id}` called a
// repository `Delete` that issued a bare `DELETE FROM configuration_object`
// guarded by the protected-role rule alone. Proven live against the running
// service: a ratified, current_baseline object that the engine refuses to delete
// (409) was destroyed through that route (204), the dependents check was
// skipped, its dependency edges were silently cascaded away, and **no audit
// event was written at all** — a governed object erased with no record of who
// did it or when (ADR-GOV-002).
//
// The persistence contract must therefore expose no destructive method. This is
// checked structurally rather than by name-matching a call site: a method that
// does not exist cannot be wired to a new handler by someone who has not read
// this ADR.
func TestConfigObjectRepository_ExposesNoDestructiveMethod(t *testing.T) {
	const file = "../domain/repository.go"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}

	var found bool
	var methods []string
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "ConfigObjectRepository" {
			return true
		}
		iface, ok := ts.Type.(*ast.InterfaceType)
		if !ok {
			return true
		}
		found = true
		for _, m := range iface.Methods.List {
			for _, name := range m.Names {
				methods = append(methods, name.Name)
			}
		}
		return false
	})
	if !found {
		t.Fatalf("%s: interface ConfigObjectRepository not found", file)
	}

	for _, m := range methods {
		switch m {
		case "Delete", "Remove", "Destroy", "Purge", "DeleteObject", "RemoveObject":
			t.Errorf("ConfigObjectRepository exposes a destructive method %q — destroying a "+
				"Configuration Object is a §8 operation owned by the Governance Engine, and a "+
				"destructive method on the persistence contract is a second, unguarded door to "+
				"it. The last one destroyed a ratified object with no audit event "+
				"(ADR-GOV-002).", m)
		}
	}
	t.Logf("ConfigObjectRepository methods: %v", methods)
}

// The transport layer must not reach a destructive persistence method directly.
// Every destructive constitutional effect goes through the engine, which is what
// guarantees the §8 preconditions and the atomic audit append (ADR-AUDIT-005).
func TestHTTPHandlers_DoNotDestroyObjectsDirectly(t *testing.T) {
	for _, file := range []string{
		"../httpapi/handlers_configobject.go",
		"../httpapi/handlers_governance.go",
	} {
		src := stripComments(readFile(t, file))
		for _, banned := range []string{"repo.Delete(", "repo.Remove(", "repo.Purge("} {
			if strings.Contains(src, banned) {
				t.Errorf("%s calls %s — a handler must not destroy a governed object outside the "+
					"Governance Engine; that path skipped the dependents check, the "+
					"working-material rule and the audit append (ADR-GOV-002)", file, banned)
			}
		}
	}

	// And the artifact delete route must delegate to the engine's deletion
	// operation, not to storage.
	src := stripComments(readFile(t, "../httpapi/handlers_configobject.go"))
	if !strings.Contains(src, "s.execGovernance(w, r, domain.OpDeletion)") {
		t.Error("deleteArtifact does not delegate to the Governance Engine's deletion operation — " +
			"DELETE /v1/artifacts/{id} would again be a second door that bypasses §8 " +
			"(ADR-GOV-002)")
	}
}

// The storage implementation must not carry an unguarded destructive method
// either: an exported one is an invitation to wire it up again.
func TestConfigObjectRepo_HasNoUnguardedDelete(t *testing.T) {
	const file = "../store/postgres/configobject_repo.go"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Recv == nil {
			return true
		}
		if fn.Name.Name == "Delete" {
			t.Errorf("%s still defines ConfigObjectRepo.Delete — the unguarded destructive path "+
				"must not exist, so it cannot be wired to a handler again (ADR-GOV-002)", file)
		}
		return true
	})
}
