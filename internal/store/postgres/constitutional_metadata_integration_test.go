//go:build integration

package postgres

import (
	"fmt"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// §9.1 constitutional inputs must not be writable through the descriptive
// metadata channel, on any surface (ADR-GOV-003).
//
// Proven live before this: creating an object with
// `metadata:{"responsibilities":"r1,r2,r3"}` stored it, and a successor seeded
// that way turned a Replacement the engine refused (409) into one it granted
// (200) against a ratified current_baseline artifact. The client was deciding a
// constitutional verdict with an unaudited field.
func TestConstitutionalMetadata_CannotBeWrittenOnCreate(t *testing.T) {
	repo := NewConfigObjectRepo(testPool(t))
	ctx := adminTestCtx()

	for _, key := range domain.ConstitutionalMetadataKeys() {
		t.Run(key, func(t *testing.T) {
			o := sample(fmt.Sprintf("cm-create-%s-%d.md", key, time.Now().UnixNano()), "1.0.0")
			o.Role = domain.RoleReference
			o.Metadata = map[string]string{key: "forged"}

			_, err := repo.Create(ctx, o)
			if err == nil {
				t.Errorf("storage accepted the §9.1 input %q as descriptive metadata — a client "+
					"could seed the data that decides the Replacement Test verdict", key)
			}
		})
	}
}

// Refusing to *set* a constitutional key while allowing the same request to
// *erase* it is not a guard. Proven live: a patch of an unrelated descriptive key
// returned 200 and removed `responsibilities`, which makes
// allResponsibilities(old) empty and the Replacement Test vacuously satisfied.
func TestConstitutionalMetadata_SurvivesADescriptivePatch(t *testing.T) {
	pool := testPool(t)
	repo := NewConfigObjectRepo(pool)
	ctx := adminTestCtx()

	o := sample(fmt.Sprintf("cm-patch-%d.md", time.Now().UnixNano()), "1.0.0")
	o.Role = domain.RoleReference
	o.Metadata = map[string]string{"owner": "team-a"}
	created, err := repo.Create(ctx, o)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Seed the constitutional input the way the platform's own history did —
	// directly, since no API surface may write it.
	if _, err := pool.Exec(ctx,
		`INSERT INTO configuration_metadata (cfg_id, key, value) VALUES ($1,'responsibilities','r1,r2,r3')`,
		created.CfgID); err != nil {
		t.Fatalf("seed responsibilities: %v", err)
	}

	// A descriptive patch of an unrelated key.
	newOwner := "team-b"
	if _, err := repo.Update(ctx, created.CfgID, created.RowVersion, &domain.Patch{
		Metadata: map[string]string{"owner": newOwner},
	}); err != nil {
		t.Fatalf("descriptive patch: %v", err)
	}

	var got string
	err = pool.QueryRow(ctx,
		`SELECT COALESCE(value,'') FROM configuration_metadata WHERE cfg_id=$1 AND key='responsibilities'`,
		created.CfgID).Scan(&got)
	if err != nil || got != "r1,r2,r3" {
		t.Errorf("a descriptive patch destroyed the §9.1 input: responsibilities=%q err=%v — an "+
			"erased `responsibilities` makes the Replacement Test vacuously satisfied", got, err)
	}

	// And the descriptive part of the patch must still have applied.
	var owner string
	if err := pool.QueryRow(ctx,
		`SELECT value FROM configuration_metadata WHERE cfg_id=$1 AND key='owner'`,
		created.CfgID).Scan(&owner); err != nil || owner != newOwner {
		t.Errorf("descriptive metadata was not updated: owner=%q err=%v", owner, err)
	}
}

// The storage layer must refuse an attempt to set a constitutional key through
// the patch path too, not merely preserve the existing one.
func TestConstitutionalMetadata_CannotBeSetOnPatch(t *testing.T) {
	repo := NewConfigObjectRepo(testPool(t))
	ctx := adminTestCtx()

	o := sample(fmt.Sprintf("cm-patchset-%d.md", time.Now().UnixNano()), "1.0.0")
	o.Role = domain.RoleReference
	created, err := repo.Create(ctx, o)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := repo.Update(ctx, created.CfgID, created.RowVersion, &domain.Patch{
		Metadata: map[string]string{"responsibilities": "forged"},
	}); err == nil {
		t.Error("storage accepted a §9.1 input through the patch path — the transport check alone " +
			"is not the enforcement boundary (ADR-GOV-003)")
	}
}
