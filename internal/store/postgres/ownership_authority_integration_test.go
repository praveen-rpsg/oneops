//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/oklog/ulid/v2"

	"github.com/rpsg/oneops/internal/domain"
)

// seedChain creates a governed object owned by tenantID and appends one audit
// event to its chain, mirroring what a genuine ratify does: the object and its
// audit rows share an owner. Returns the chain id (== cfg_id).
func seedChain(ctx context.Context, t *testing.T, priv poolExec, tenantID string) string {
	t.Helper()
	cfgID := ulid.Make().String()
	if _, err := priv.Exec(ctx, `
		INSERT INTO configuration_object
			(cfg_id, artifact, version, role, lifecycle, retention_class, tenant_id)
		VALUES ($1, $2, '1.0.0', 'governance', 'draft', 'working_material', $3)`,
		cfgID, "chain-"+cfgID+".md", tenantID); err != nil {
		t.Fatalf("seed object: %v", err)
	}
	appendAuditRow(ctx, t, priv, cfgID, 1, tenantID)
	return cfgID
}

func appendAuditRow(ctx context.Context, t *testing.T, priv poolExec, chainID string, seq int64, tenantID string) {
	t.Helper()
	if _, err := priv.Exec(ctx, `
		INSERT INTO audit_event
			(chain_id, seq, event_id, operation_id, operation, actor,
			 payload_canonical, payload, prev_hash, this_hash, occurred_at, tenant_id)
		VALUES ($1, $2, $3, $4, 'ratification', 'actor', '{}', '{}'::jsonb, $5, $6, now(), $7)`,
		chainID, seq, ulid.Make().String(), ulid.Make().String(),
		make([]byte, 32), append([]byte{byte(seq)}, make([]byte, 31)...), tenantID); err != nil {
		t.Fatalf("append audit row: %v", err)
	}
}

type poolExec interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

// The authoritative resolver returns the governed object's owner for a
// consistent chain.
func TestResolveEventOwner_ConsistentChain(t *testing.T) {
	priv := testPool(t)
	ctx := context.Background()
	store := NewAuditStore(priv)

	tenants := NewTenantStore(priv)
	owner, err := tenants.Create(ctx, newTenant("auth-consistent", "ext-auth-consistent"))
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	chain := seedChain(ctx, t, priv, owner.TenantID)

	got, err := store.ResolveEventOwner(ctx, chain, 1)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != owner.TenantID {
		t.Fatalf("owner = %q, want %q", got, owner.TenantID)
	}
}

// The exploit as a regression: an audit row labelled with a different tenant
// than the governed object it records makes ownership ambiguous, and the
// resolver must refuse rather than trust the label. Against the running service
// this split-brain row delivered a victim's event to an attacker.
func TestResolveEventOwner_SplitBrainIsRefused(t *testing.T) {
	priv := testPool(t)
	ctx := context.Background()
	store := NewAuditStore(priv)

	tenants := NewTenantStore(priv)
	victim, err := tenants.Create(ctx, newTenant("auth-victim", "ext-auth-victim"))
	if err != nil {
		t.Fatalf("create victim: %v", err)
	}
	attacker, err := tenants.Create(ctx, newTenant("auth-attacker", "ext-auth-attacker"))
	if err != nil {
		t.Fatalf("create attacker: %v", err)
	}

	chain := seedChain(ctx, t, priv, victim.TenantID)
	// Corruption: append a second event to the victim's chain labelled attacker.
	appendAuditRow(ctx, t, priv, chain, 2, attacker.TenantID)

	_, err = store.ResolveEventOwner(ctx, chain, 2)
	if !errors.Is(err, domain.ErrOwnershipAmbiguous) {
		t.Fatalf("resolve split-brain = %v, want ErrOwnershipAmbiguous", err)
	}
}

// A chain with no governed object (deleted or never created) is unresolvable,
// not silently trusted.
func TestResolveEventOwner_OrphanChainIsUnresolvable(t *testing.T) {
	priv := testPool(t)
	ctx := context.Background()
	store := NewAuditStore(priv)

	// Audit row with no matching configuration_object.
	orphan := ulid.Make().String()
	appendAuditRow(ctx, t, priv, orphan, 1, domain.SystemTenantID)

	_, err := store.ResolveEventOwner(ctx, orphan, 1)
	if !errors.Is(err, domain.ErrEventNotFound) {
		t.Fatalf("resolve orphan = %v, want ErrEventNotFound", err)
	}
}

// Startup validation counts divergent events and reports an example, so the
// platform can refuse to boot on a corrupted log.
func TestValidateOwnershipConsistency(t *testing.T) {
	priv := testPool(t)
	ctx := context.Background()
	store := NewAuditStore(priv)

	tenants := NewTenantStore(priv)
	victim, _ := tenants.Create(ctx, newTenant("val-victim", "ext-val-victim"))
	attacker, _ := tenants.Create(ctx, newTenant("val-attacker", "ext-val-attacker"))

	// A consistent chain contributes nothing.
	seedChain(ctx, t, priv, victim.TenantID)

	before, _, err := store.ValidateOwnershipConsistency(ctx)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}

	// Introduce one divergence.
	corrupt := seedChain(ctx, t, priv, victim.TenantID)
	appendAuditRow(ctx, t, priv, corrupt, 2, attacker.TenantID)

	after, example, err := store.ValidateOwnershipConsistency(ctx)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if after != before+1 {
		t.Fatalf("divergent count = %d, want %d", after, before+1)
	}
	if example == "" {
		t.Error("validation must report an example chain to locate the corruption")
	}
}
