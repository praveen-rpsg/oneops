//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
)

const invTTL = 48 * time.Hour

// seedOrgForInvitations creates a tenant and organisation to hang invitations
// off, using the privileged pool so the fixture exists regardless of what a
// scoped connection can see.
func seedOrgForInvitations(ctx context.Context, t *testing.T, priv *pgxpool.Pool, suffix string) (tenantID, orgID string) {
	t.Helper()
	tenantID = "tn-inv-" + suffix
	orgID = "org_inv_" + suffix
	if _, err := priv.Exec(ctx,
		`INSERT INTO tenant (tenant_id, slug, name) VALUES ($1, $2, $3)
		 ON CONFLICT (tenant_id) DO NOTHING`,
		tenantID, "inv-"+suffix, "Inv "+suffix); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if _, err := priv.Exec(ctx,
		`INSERT INTO organization (org_id, tenant_id, slug, name) VALUES ($1, $2, $3, $4)
		 ON CONFLICT (org_id) DO NOTHING`,
		orgID, tenantID, "inv-"+suffix, "Inv "+suffix); err != nil {
		t.Fatalf("seed organization: %v", err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = priv.Exec(c, `DELETE FROM invitation WHERE org_id = $1`, orgID)
		_, _ = priv.Exec(c, `DELETE FROM organization WHERE org_id = $1`, orgID)
		_, _ = priv.Exec(c, `DELETE FROM tenant WHERE tenant_id = $1`, tenantID)
	})
	return tenantID, orgID
}

func issue(t *testing.T, ctx context.Context, store *InvitationStore, orgID, tenantID, email string, ttl time.Duration) (*domain.Invitation, string) {
	t.Helper()
	inv, token, err := domain.NewInvitation(orgID, tenantID, email, ttl, time.Now())
	if err != nil {
		t.Fatalf("NewInvitation: %v", err)
	}
	created, err := store.Create(ctx, inv)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return created, token
}

// The acceptance criterion, proven against the table rather than the type: the
// plaintext token must appear in no column of the row it created.
//
// Every column is read back as text and searched, so a token that leaked into
// some field other than token_hash — a name, a note added later — is caught too.
func TestInvitationStore_TokenIsUnrecoverableFromTheTable(t *testing.T) {
	priv := testPool(t)
	ctx := context.Background()
	tenantID, orgID := seedOrgForInvitations(ctx, t, priv, "unrecoverable")
	store := NewInvitationStore(priv)

	created, token := issue(t, ctx, store, orgID, tenantID, "unrecoverable@example.com", invTTL)

	rows, err := priv.Query(ctx, `SELECT to_jsonb(i)::text FROM invitation i WHERE invitation_id = $1`,
		created.InvitationID)
	if err != nil {
		t.Fatalf("read row as json: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("the invitation was not stored")
	}
	var whole string
	if err := rows.Scan(&whole); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if strings.Contains(whole, token) {
		t.Fatalf("the plaintext token appears in the stored row: %s", whole)
	}
	if !strings.Contains(whole, domain.HashInvitationToken(token)) {
		t.Error("the row does not carry the hash of the issued token")
	}
}

// Single use, proven by replay: the second redemption of the same token must
// fail, and must fail the same way an unknown token does.
func TestInvitationStore_ConsumeIsSingleUse(t *testing.T) {
	priv := testPool(t)
	ctx := context.Background()
	tenantID, orgID := seedOrgForInvitations(ctx, t, priv, "single-use")
	store := NewInvitationStore(priv)

	created, token := issue(t, ctx, store, orgID, tenantID, "single@example.com", invTTL)

	first, err := store.Consume(ctx, token)
	if err != nil {
		t.Fatalf("first Consume: %v", err)
	}
	if first.InvitationID != created.InvitationID {
		t.Errorf("consumed %s, want %s", first.InvitationID, created.InvitationID)
	}
	if first.Status != domain.InvitationRedeemed {
		t.Errorf("status after Consume is %q, want redeemed", first.Status)
	}
	if first.RedeemedAt == nil {
		t.Error("redeemed_at was not set")
	}

	// The replay.
	if _, err := store.Consume(ctx, token); !errors.Is(err, domain.ErrTokenNotRedeemable) {
		t.Fatalf("replay: got %v, want ErrTokenNotRedeemable", err)
	}
	// And it is indistinguishable from a token that never existed.
	_, unknown, err := domain.NewInvitation(orgID, tenantID, "nobody@example.com", invTTL, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Consume(ctx, unknown); !errors.Is(err, domain.ErrTokenNotRedeemable) {
		t.Fatalf("unknown token: got %v, want the same ErrTokenNotRedeemable as a replay", err)
	}
}

// Concurrent redemption of one token must produce exactly one winner.
//
// This is the assertion a sequential replay test cannot make. A read-then-write
// implementation passes the replay test and fails here, because both callers
// observe `pending` before either writes.
func TestInvitationStore_ConcurrentConsumeHasExactlyOneWinner(t *testing.T) {
	priv := testPool(t)
	ctx := context.Background()
	tenantID, orgID := seedOrgForInvitations(ctx, t, priv, "race")
	store := NewInvitationStore(priv)

	const racers = 8
	_, token := issue(t, ctx, store, orgID, tenantID, "race@example.com", invTTL)

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners int
		others  []error
	)
	start := make(chan struct{})
	for n := 0; n < racers; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := store.Consume(ctx, token)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				winners++
			case errors.Is(err, domain.ErrTokenNotRedeemable):
			default:
				others = append(others, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if winners != 1 {
		t.Errorf("%d of %d concurrent redemptions succeeded, want exactly 1 — the token is "+
			"not single-use under concurrency", winners, racers)
	}
	if len(others) != 0 {
		t.Errorf("unexpected errors from losing racers: %v", others)
	}
}

// An expired invitation is refused, and the refusal is the database's
// comparison rather than a Go-side check that could be skipped.
func TestInvitationStore_ConsumeRefusesExpired(t *testing.T) {
	priv := testPool(t)
	ctx := context.Background()
	tenantID, orgID := seedOrgForInvitations(ctx, t, priv, "expired")
	store := NewInvitationStore(priv)

	// Issued valid, then aged past its window directly in the table — the only
	// way to reach the boundary without waiting for it.
	created, token := issue(t, ctx, store, orgID, tenantID, "expired@example.com", invTTL)
	if _, err := priv.Exec(ctx,
		`UPDATE invitation SET expires_at = now() - interval '1 second' WHERE invitation_id = $1`,
		created.InvitationID); err != nil {
		t.Fatalf("age the invitation: %v", err)
	}

	if _, err := store.Consume(ctx, token); !errors.Is(err, domain.ErrTokenNotRedeemable) {
		t.Fatalf("expired token: got %v, want ErrTokenNotRedeemable", err)
	}

	// It must remain pending, not be silently marked redeemed by a failed
	// attempt: a refused redemption consumes nothing.
	after, err := store.Get(ctx, created.InvitationID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if after.Status != domain.InvitationPending {
		t.Errorf("status after a refused redemption is %q, want pending", after.Status)
	}
	if after.RedeemedAt != nil {
		t.Error("a refused redemption set redeemed_at")
	}
}

func TestInvitationStore_ConsumeRefusesRevoked(t *testing.T) {
	priv := testPool(t)
	ctx := context.Background()
	tenantID, orgID := seedOrgForInvitations(ctx, t, priv, "revoked")
	store := NewInvitationStore(priv)

	created, token := issue(t, ctx, store, orgID, tenantID, "revoked@example.com", invTTL)

	revoked, err := store.Revoke(ctx, created.InvitationID)
	if err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if revoked.Status != domain.InvitationRevoked {
		t.Errorf("status after Revoke is %q, want revoked", revoked.Status)
	}
	if _, err := store.Consume(ctx, token); !errors.Is(err, domain.ErrTokenNotRedeemable) {
		t.Fatalf("revoked token: got %v, want ErrTokenNotRedeemable", err)
	}
}

// Revoking twice, or revoking something already redeemed, must not quietly
// rewrite history.
func TestInvitationStore_RevokeIsConditionalOnPending(t *testing.T) {
	priv := testPool(t)
	ctx := context.Background()
	tenantID, orgID := seedOrgForInvitations(ctx, t, priv, "revoke-twice")
	store := NewInvitationStore(priv)

	created, token := issue(t, ctx, store, orgID, tenantID, "twice@example.com", invTTL)
	if _, err := store.Revoke(ctx, created.InvitationID); err != nil {
		t.Fatalf("first Revoke: %v", err)
	}
	if _, err := store.Revoke(ctx, created.InvitationID); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("second Revoke: got %v, want ErrConflict", err)
	}

	// A redeemed invitation may not be revoked back out of its redemption.
	other, otherToken := issue(t, ctx, store, orgID, tenantID, "redeemed-then@example.com", invTTL)
	if _, err := store.Consume(ctx, otherToken); err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if _, err := store.Revoke(ctx, other.InvitationID); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("revoking a redeemed invitation: got %v, want ErrConflict", err)
	}
	after, err := store.Get(ctx, other.InvitationID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != domain.InvitationRedeemed {
		t.Errorf("status is %q after a refused revoke, want redeemed", after.Status)
	}
	_ = token
}

func TestInvitationStore_RevokeUnknownIsNotFound(t *testing.T) {
	priv := testPool(t)
	ctx := context.Background()
	store := NewInvitationStore(priv)

	if _, err := store.Revoke(ctx, domain.NewID()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("revoke unknown: got %v, want ErrNotFound", err)
	}
	if _, err := store.Get(ctx, domain.NewID()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("get unknown: got %v, want ErrNotFound", err)
	}
}

func TestInvitationStore_ListByOrgPagesAndScopes(t *testing.T) {
	priv := testPool(t)
	ctx := context.Background()
	tenantA, orgA := seedOrgForInvitations(ctx, t, priv, "list-a")
	tenantB, orgB := seedOrgForInvitations(ctx, t, priv, "list-b")
	store := NewInvitationStore(priv)

	for n := 0; n < 5; n++ {
		issue(t, ctx, store, orgA, tenantA, fmt.Sprintf("a%d@example.com", n), invTTL)
	}
	issue(t, ctx, store, orgB, tenantB, "b0@example.com", invTTL)

	all, err := store.ListByOrg(ctx, orgA, 0, "")
	if err != nil {
		t.Fatalf("ListByOrg: %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("ListByOrg returned %d, want 5 — another organisation's invitations leaked in", len(all))
	}
	for _, i := range all {
		if i.OrgID != orgA {
			t.Errorf("invitation %s belongs to %s, not %s", i.InvitationID, i.OrgID, orgA)
		}
	}

	page, err := store.ListByOrg(ctx, orgA, 2, "")
	if err != nil {
		t.Fatalf("ListByOrg page: %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("limit=2 returned %d", len(page))
	}
	next, err := store.ListByOrg(ctx, orgA, 2, page[1].InvitationID)
	if err != nil {
		t.Fatalf("ListByOrg next: %v", err)
	}
	if len(next) != 2 {
		t.Fatalf("second page returned %d, want 2", len(next))
	}
	if next[0].InvitationID <= page[1].InvitationID {
		t.Error("the keyset cursor did not advance")
	}
}

// The page cap, proven by exceeding it.
//
// Asserting `len <= cap` against a fixture smaller than the cap proves nothing:
// removing the clamp entirely left that assertion passing. The rows have to
// outnumber the cap before the clamp is observable at all.
func TestInvitationStore_ListByOrgCapsAnUnboundedRequest(t *testing.T) {
	priv := testPool(t)
	ctx := context.Background()
	tenantID, orgID := seedOrgForInvitations(ctx, t, priv, "cap")

	// Inserted directly and in one statement: this exercises the read cap, and
	// issuing 501 tokens through the aggregate would test nothing extra while
	// taking far longer. seedOrgForInvitations already deletes them.
	const over = maxInvitationPageSize + 1
	if _, err := priv.Exec(ctx, `
		INSERT INTO invitation (invitation_id, tenant_id, org_id, email, token_hash, status, expires_at)
		SELECT 'inv_cap_' || lpad(n::text, 6, '0'), $1, $2,
		       'cap' || n || '@example.com', encode(sha256(('cap' || n)::bytea), 'hex'),
		       'pending', now() + interval '48 hours'
		  FROM generate_series(1, $3) AS n`, tenantID, orgID, over); err != nil {
		t.Fatalf("seed %d invitations: %v", over, err)
	}

	store := NewInvitationStore(priv)

	capped, err := store.ListByOrg(ctx, orgID, maxInvitationPageSize+1000, "")
	if err != nil {
		t.Fatalf("ListByOrg over the cap: %v", err)
	}
	if len(capped) != maxInvitationPageSize {
		t.Errorf("asked for %d with %d rows available: got %d, want the cap of %d",
			maxInvitationPageSize+1000, over, len(capped), maxInvitationPageSize)
	}

	// An unspecified limit takes the default, which is also below what exists.
	defaulted, err := store.ListByOrg(ctx, orgID, 0, "")
	if err != nil {
		t.Fatalf("ListByOrg default: %v", err)
	}
	if len(defaulted) != defaultInvitationPageSize {
		t.Errorf("unbounded request returned %d, want the default of %d",
			len(defaulted), defaultInvitationPageSize)
	}
}

// invitation is tenant-scoped and under forced RLS, and until now nothing in Go
// had exercised that policy. Administrative reads must see one boundary only.
func TestInvitationStore_AdministrativeReadsAreTenantScoped(t *testing.T) {
	priv := testPool(t)
	ctx := context.Background()
	tenantA, orgA := seedOrgForInvitations(ctx, t, priv, "rls-a")
	tenantB, orgB := seedOrgForInvitations(ctx, t, priv, "rls-b")

	privStore := NewInvitationStore(priv)
	invA, _ := issue(t, ctx, privStore, orgA, tenantA, "rls-a@example.com", invTTL)
	invB, _ := issue(t, ctx, privStore, orgB, tenantB, "rls-b@example.com", invTTL)

	scoped := NewInvitationStore(tenantScopedPool(t))
	asA := domain.WithTenant(ctx, &domain.Tenant{TenantID: tenantA})

	if _, err := scoped.Get(asA, invA.InvitationID); err != nil {
		t.Fatalf("tenant A cannot read its own invitation: %v", err)
	}
	if _, err := scoped.Get(asA, invB.InvitationID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("tenant A read tenant B's invitation: got %v, want ErrNotFound — row-level "+
			"security is not confining the administrative read path", err)
	}
	list, err := scoped.ListByOrg(asA, orgB, 0, "")
	if err != nil {
		t.Fatalf("ListByOrg across tenants: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("tenant A listed %d of tenant B's invitations, want 0", len(list))
	}
}

// Consume must NOT be tenant-scoped: an invitee holds no membership, so there
// is no tenant to bind before the invitation is found (ADR-TENANCY-001 §4).
//
// This test states the constraint as an executable fact, so a later change that
// binds the redeem path to the scoped pool fails here rather than in production
// as "every redemption is refused".
func TestInvitationStore_ConsumeCannotRunOnTheScopedPool(t *testing.T) {
	priv := testPool(t)
	ctx := context.Background()
	tenantID, orgID := seedOrgForInvitations(ctx, t, priv, "consume-scope")

	privStore := NewInvitationStore(priv)
	_, token := issue(t, ctx, privStore, orgID, tenantID, "scope@example.com", invTTL)

	// No tenant bound, which is the invitee's actual situation.
	scoped := NewInvitationStore(tenantScopedPool(t))
	if _, err := scoped.Consume(ctx, token); !errors.Is(err, domain.ErrTokenNotRedeemable) {
		t.Fatalf("scoped Consume with no tenant bound: got %v, want ErrTokenNotRedeemable — "+
			"if this ever succeeds, row-level security is not confining the table", err)
	}

	// The privileged pool is where redemption belongs, and the token is still
	// unused: the refused attempt above consumed nothing.
	if _, err := privStore.Consume(ctx, token); err != nil {
		t.Fatalf("privileged Consume after a scoped attempt: %v — the scoped attempt must not "+
			"have consumed the invitation", err)
	}
}

func TestInvitationStore_CreateRejectsInvalidAggregate(t *testing.T) {
	priv := testPool(t)
	ctx := context.Background()
	tenantID, orgID := seedOrgForInvitations(ctx, t, priv, "invalid")
	store := NewInvitationStore(priv)

	valid, token := issue(t, ctx, store, orgID, tenantID, "valid@example.com", invTTL)

	// A plaintext token in the hash column is the failure the width check
	// exists to catch, and Create must not be a way around Validate.
	bad := *valid
	bad.InvitationID = domain.NewID()
	bad.TokenHash = token
	if _, err := store.Create(ctx, &bad); err == nil {
		t.Error("Create stored a plaintext token in token_hash")
	}

	bad2 := *valid
	bad2.InvitationID = domain.NewID()
	bad2.Email = "not-an-email"
	if _, err := store.Create(ctx, &bad2); err == nil {
		t.Error("Create stored a malformed email")
	}
}

// The email is stored citext and read back as ::text. This pins the round trip,
// which is where the user registry's case-folding defect actually lived.
func TestInvitationStore_EmailRoundTripsNormalised(t *testing.T) {
	priv := testPool(t)
	ctx := context.Background()
	tenantID, orgID := seedOrgForInvitations(ctx, t, priv, "email")
	store := NewInvitationStore(priv)

	created, _ := issue(t, ctx, store, orgID, tenantID, "  MiXeD@Example.COM  ", invTTL)
	if created.Email != "mixed@example.com" {
		t.Errorf("Create returned email %q, want the normalised form", created.Email)
	}
	got, err := store.Get(ctx, created.InvitationID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Email != "mixed@example.com" {
		t.Errorf("Get returned email %q, want the normalised form", got.Email)
	}
}
