//go:build integration

package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/oklog/ulid/v2"

	"github.com/rpsg/oneops/internal/domain"
)

// dropTenantKeepingData removes a tenant from the registry while leaving the
// rows that reference it, exactly as `pg_restore --disable-triggers` of an older
// tenant table does. session_replication_role=replica suspends the foreign-key
// triggers for the delete.
func dropTenantKeepingData(ctx context.Context, t *testing.T, priv poolExec, tenantID string) {
	t.Helper()
	if _, err := priv.Exec(ctx,
		`SET session_replication_role = replica`); err != nil {
		t.Fatalf("disable triggers: %v", err)
	}
	if _, err := priv.Exec(ctx, `DELETE FROM tenant WHERE tenant_id = $1`, tenantID); err != nil {
		t.Fatalf("drop tenant: %v", err)
	}
	if _, err := priv.Exec(ctx, `SET session_replication_role = origin`); err != nil {
		t.Fatalf("re-enable triggers: %v", err)
	}
}

// A governed object whose owning tenant a partial restore has dropped must be
// unresolvable at execution — the platform must not act for a tenant that does
// not exist. Verified against the running service: before this, the relay
// looped forever on a foreign-key error and the platform served regardless.
func TestRestore_MissingOwnerTenantIsUnresolvable(t *testing.T) {
	priv := testPool(t)
	ctx := context.Background()
	store := NewAuditStore(priv)

	tenants := NewTenantStore(priv)
	ghost, err := tenants.Create(ctx, newTenant("restore-ghost", "ext-restore-ghost"))
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	chain := seedChain(ctx, t, priv, ghost.TenantID)

	// Consistent while the tenant exists.
	if _, err := store.ResolveEventOwner(ctx, chain, 1); err != nil {
		t.Fatalf("resolve while tenant present: %v", err)
	}

	dropTenantKeepingData(ctx, t, priv, ghost.TenantID)

	// Now the owner is not a live tenant; ownership cannot be established.
	if _, err := store.ResolveEventOwner(ctx, chain, 1); !errors.Is(err, domain.ErrEventNotFound) {
		t.Fatalf("resolve after tenant dropped = %v, want ErrEventNotFound", err)
	}
}

// Startup validation refuses a database whose data references a missing tenant,
// naming every affected table.
func TestRestore_StartupRefusesDanglingOwnership(t *testing.T) {
	priv := testPool(t)
	ctx := context.Background()
	validator := NewOwnershipValidator(priv)

	tenants := NewTenantStore(priv)
	ghost, _ := tenants.Create(ctx, newTenant("restore-dangle", "ext-restore-dangle"))
	seedChain(ctx, t, priv, ghost.TenantID)

	before, err := validator.Validate(ctx)
	if err != nil {
		t.Fatalf("validate (clean): %v", err)
	}

	dropTenantKeepingData(ctx, t, priv, ghost.TenantID)

	after, err := validator.Validate(ctx)
	if err != nil {
		t.Fatalf("validate (dangling): %v", err)
	}
	if len(after) <= len(before) {
		t.Fatalf("validation did not detect dangling ownership: before=%d after=%d", len(before), len(after))
	}
	// configuration_object and audit_event both dangle for the dropped tenant.
	joined := ""
	for _, p := range after {
		joined += p + "\n"
	}
	for _, want := range []string{"configuration_object", "audit_event"} {
		if !strings.Contains(joined, want) {
			t.Errorf("validation did not name %s among problems:\n%s", want, joined)
		}
	}
}

// Audit-only restore: an audit chain with no governed object cannot establish
// ownership and is refused at execution (queue-only restore is the same shape:
// a delivery whose event is not in the log).
func TestRestore_AuditWithoutObjectIsUnresolvable(t *testing.T) {
	priv := testPool(t)
	ctx := context.Background()
	store := NewAuditStore(priv)

	orphanChain := ulid.Make().String()
	appendAuditRow(ctx, t, priv, orphanChain, 1, domain.SystemTenantID)

	if _, err := store.ResolveEventOwner(ctx, orphanChain, 1); !errors.Is(err, domain.ErrEventNotFound) {
		t.Fatalf("resolve audit-only = %v, want ErrEventNotFound", err)
	}
}
