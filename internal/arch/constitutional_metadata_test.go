package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// §9.1 constitutional inputs (`responsibilities`, `citations`, `coverage`) are
// carried in the metadata map but are not descriptive data: they decide computed
// Authority and the four-part Replacement Test. The descriptive-metadata channel
// must therefore be unable to write them — and unable to destroy them.
//
// The guard used to live in one DTO conversion (`toPatch`), by convention. The
// writes live somewhere else entirely, and three surfaces reached them:
//
//   - CREATE accepted the keys outright (proven live: a successor seeded with
//     crafted `responsibilities` turned a Replacement the engine refused, 409,
//     into one it granted, 200, against a ratified current_baseline artifact);
//   - BULK CREATE, identically;
//   - PATCH refused to *set* them while the same request *erased* them (proven
//     live: patching an unrelated key returned 200 and removed
//     `responsibilities`, which makes allResponsibilities(old) empty and the
//     Replacement Test vacuously satisfied).
//
// A guard that is not where the write happens is not a guard. These tests pin
// the enforcement to the storage chokepoint, which every present and future
// caller passes through (ADR-GOV-003).
func TestMetadataWrites_AreGuardedAtTheStorageChokepoint(t *testing.T) {
	const file = "../store/postgres/configobject_repo.go"
	src := readFile(t, file)

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}

	writers := 0
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			return true
		}
		body := src[fset.Position(fn.Pos()).Offset:fset.Position(fn.End()).Offset]
		if !strings.Contains(body, "INSERT INTO configuration_metadata") {
			return true
		}
		writers++
		if !strings.Contains(body, "IsConstitutionalMetadataKey") {
			t.Errorf("%s.%s writes configuration_metadata without refusing §9.1 constitutional "+
				"inputs — the descriptive channel must not be able to set them (ADR-GOV-003)",
				file, fn.Name.Name)
		}
		return false
	})
	if writers == 0 {
		t.Fatalf("%s: found no configuration_metadata writer to check — the assertion would be "+
			"vacuous", file)
	}
	t.Logf("guarded configuration_metadata writers: %d", writers)
}

// Refusing to set a constitutional key while allowing the same request to delete
// it is not protection. Any wholesale clear of the metadata map must spare them.
func TestMetadataClear_PreservesConstitutionalInputs(t *testing.T) {
	const file = "../store/postgres/configobject_repo.go"
	src := stripComments(readFile(t, file))

	idx := strings.Index(src, "DELETE FROM configuration_metadata")
	if idx < 0 {
		t.Skip("no wholesale metadata clear in the repository")
	}
	for idx >= 0 {
		// Look at the statement containing this DELETE.
		end := strings.Index(src[idx:], "`")
		stmt := src[idx:]
		if end > 0 {
			stmt = src[idx : idx+end]
		}
		if !strings.Contains(stmt, "key <> ALL") {
			t.Errorf("%s clears configuration_metadata without excluding the §9.1 constitutional "+
				"inputs — a descriptive patch would erase them, and an erased "+
				"`responsibilities` makes the Replacement Test vacuously satisfied "+
				"(ADR-GOV-003); statement: %q", file, stmt)
		}
		next := strings.Index(src[idx+1:], "DELETE FROM configuration_metadata")
		if next < 0 {
			break
		}
		idx = idx + 1 + next
	}
}

// The ingress boundary must refuse them too, so callers get a clear 422 rather
// than a storage error. ValidateInception is the one boundary both the single
// and bulk create paths pass through.
func TestInception_RefusesConstitutionalMetadata(t *testing.T) {
	src := stripComments(readFile(t, "../domain/configobject.go"))

	i := strings.Index(src, "func (c *ConfigObject) ValidateInception()")
	if i < 0 {
		t.Fatal("ValidateInception not found in domain/configobject.go")
	}
	body := src[i:]
	if j := strings.Index(body[1:], "\nfunc "); j > 0 {
		body = body[:j]
	}
	if !strings.Contains(body, "IsConstitutionalMetadataKey") {
		t.Error("ValidateInception does not refuse §9.1 constitutional inputs — creation was the " +
			"door that let a client decide a Replacement Test verdict with an unaudited field " +
			"(ADR-GOV-003)")
	}
}

// The set of constitutional keys must have exactly one definition. Two lists
// would drift, and a key missing from one of them is an open door.
func TestConstitutionalMetadataKeys_HaveOneDefinition(t *testing.T) {
	for _, file := range []string{
		"../httpapi/dto.go",
		"../store/postgres/configobject_repo.go",
		"../domain/configobject.go",
	} {
		src := stripComments(readFile(t, file))
		for _, literal := range []string{`"responsibilities"`, `"citations"`, `"coverage"`} {
			if strings.Contains(src, literal) {
				t.Errorf("%s hard-codes the constitutional metadata key %s instead of using "+
					"domain.IsConstitutionalMetadataKey / ConstitutionalMetadataKeys — a second "+
					"list drifts, and a key missing from one copy is an open door (ADR-GOV-003)",
					file, literal)
			}
		}
	}
}
