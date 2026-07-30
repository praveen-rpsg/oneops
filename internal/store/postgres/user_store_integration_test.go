//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

// newUser builds and persists an active user with a unique address, returning
// the stored row. Tests that need a user but are not testing creation use this.
func newStoredUser(ctx context.Context, t *testing.T, s *UserStore, local string) *domain.User {
	t.Helper()
	u, err := domain.NewUser(local+"@example.com", "Test "+local)
	if err != nil {
		t.Fatalf("build user: %v", err)
	}
	created, err := s.Create(ctx, u)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return created
}

func TestUserStore_CreateAndGet(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	s := NewUserStore(pool)

	created := newStoredUser(ctx, t, s, "createget")
	if created.Status != domain.UserInvited {
		t.Errorf("status = %q, want invited", created.Status)
	}
	if created.RowVersion != 1 {
		t.Errorf("row version = %d, want 1 on insert", created.RowVersion)
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Error("timestamps were not populated by the database")
	}

	got, err := s.Get(ctx, created.UserID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Email != created.Email {
		t.Errorf("email round-tripped as %q, want %q", got.Email, created.Email)
	}
}

func TestUserStore_GetUnknownIsNotFound(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	s := NewUserStore(pool)

	if _, err := s.Get(ctx, "usr_does_not_exist"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

// A duplicate address is a conflict the caller can act on, not a 500. It must
// be detected regardless of casing, because uq_user_email is on a citext column
// and the same person must not end up with two accounts.
func TestUserStore_DuplicateEmailIsConflict(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	s := NewUserStore(pool)

	newStoredUser(ctx, t, s, "dupe")

	same, err := domain.NewUser("dupe@example.com", "Same Address")
	if err != nil {
		t.Fatalf("build user: %v", err)
	}
	if _, err := s.Create(ctx, same); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("identical address: got %v, want ErrConflict", err)
	}

	// Different casing, same person.
	cased := &domain.User{
		UserID: domain.NewID(),
		Email:  "DUPE@Example.COM",
		Status: domain.UserInvited,
	}
	if _, err := s.Create(ctx, cased); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("differently cased address: got %v, want ErrConflict — the same person "+
			"can hold two accounts", err)
	}
}

// GetByEmail must fold case. It is written against lower(email::text) rather
// than citext's operator, because that operator is resolved through search_path
// and silently degrades to case-sensitive text equality when the extension's
// schema is not in the path.
func TestUserStore_GetByEmailIsCaseInsensitive(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	s := NewUserStore(pool)

	created := newStoredUser(ctx, t, s, "casefold")

	for _, probe := range []string{
		"casefold@example.com",
		"CaseFold@Example.COM",
		"  CASEFOLD@EXAMPLE.COM  ",
	} {
		got, err := s.GetByEmail(ctx, probe)
		if err != nil {
			t.Fatalf("GetByEmail(%q): %v", probe, err)
		}
		if got.UserID != created.UserID {
			t.Errorf("GetByEmail(%q) returned %q, want %q", probe, got.UserID, created.UserID)
		}
	}

	// Create normalises on write, so every row it stores is already lowercase and
	// a query without a fold would still match. This row is inserted directly,
	// with its casing intact, so the fold in GetByEmail is actually exercised —
	// without it this lookup misses and the mutation that removes lower() passes.
	raw := domain.NewID()
	if _, err := pool.Exec(ctx,
		`INSERT INTO app_user (user_id, email) VALUES ($1, $2)`,
		raw, "NotNormalised@Example.COM"); err != nil {
		t.Fatalf("insert unnormalised row: %v", err)
	}
	got, err := s.GetByEmail(ctx, "notnormalised@example.com")
	if err != nil {
		t.Fatalf("GetByEmail did not fold the case of a stored mixed-case address: %v", err)
	}
	if got.UserID != raw {
		t.Errorf("got %q, want %q", got.UserID, raw)
	}

	if _, err := s.GetByEmail(ctx, "nobody@example.com"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown address: got %v, want ErrNotFound", err)
	}
	// An empty address must not match a row; it is a caller bug, not a wildcard.
	if _, err := s.GetByEmail(ctx, "   "); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("empty address: got %v, want ErrNotFound", err)
	}
}

func TestUserStore_UpdateProfile(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	s := NewUserStore(pool)

	created := newStoredUser(ctx, t, s, "profile")

	updated, err := s.UpdateProfile(ctx, created.UserID, created.RowVersion, "New Name")
	if err != nil {
		t.Fatalf("update profile: %v", err)
	}
	if updated.DisplayName != "New Name" {
		t.Errorf("display name = %q, want %q", updated.DisplayName, "New Name")
	}
	if updated.RowVersion != created.RowVersion+1 {
		t.Errorf("row version = %d, want %d", updated.RowVersion, created.RowVersion+1)
	}
	if updated.Status != created.Status {
		t.Errorf("status changed to %q during a profile update; lifecycle moves belong "+
			"to SetStatus alone", updated.Status)
	}

	// The stale version must be refused, or a lost update is silent.
	if _, err := s.UpdateProfile(ctx, created.UserID, created.RowVersion, "Third Name"); !errors.Is(err, domain.ErrVersionMismatch) {
		t.Errorf("stale row version: got %v, want ErrVersionMismatch", err)
	}

	// A missing user is 404, not a version conflict.
	if _, err := s.UpdateProfile(ctx, "usr_missing", 1, "Nobody"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown user: got %v, want ErrNotFound", err)
	}
}

func TestUserStore_UpdateProfileValidatesLength(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	s := NewUserStore(pool)

	created := newStoredUser(ctx, t, s, "toolong")

	long := make([]byte, domain.MaxDisplayNameLength+1)
	for i := range long {
		long[i] = 'x'
	}
	_, err := s.UpdateProfile(ctx, created.UserID, created.RowVersion, string(long))
	if _, ok := domain.AsValidation(err); !ok {
		t.Errorf("got %v (%T), want a ValidationError — an over-long name must fail as a "+
			"422 rather than reaching the database", err, err)
	}
}

// The lifecycle is enforced at the store, not merely described in the domain:
// a transition the state machine forbids must not reach the table.
func TestUserStore_SetStatusFollowsTheLifecycle(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	s := NewUserStore(pool)

	u := newStoredUser(ctx, t, s, "lifecycle")

	// invited -> active
	active, err := s.SetStatus(ctx, u.UserID, u.RowVersion, domain.UserActive)
	if err != nil {
		t.Fatalf("invited -> active: %v", err)
	}
	if active.Status != domain.UserActive {
		t.Fatalf("status = %q, want active", active.Status)
	}

	// active -> suspended -> active
	susp, err := s.SetStatus(ctx, active.UserID, active.RowVersion, domain.UserSuspended)
	if err != nil {
		t.Fatalf("active -> suspended: %v", err)
	}
	revived, err := s.SetStatus(ctx, susp.UserID, susp.RowVersion, domain.UserActive)
	if err != nil {
		t.Fatalf("suspended -> active: %v", err)
	}

	// -> deactivated, which is terminal
	dead, err := s.SetStatus(ctx, revived.UserID, revived.RowVersion, domain.UserDeactivated)
	if err != nil {
		t.Fatalf("active -> deactivated: %v", err)
	}
	for _, to := range []domain.UserStatus{domain.UserActive, domain.UserInvited, domain.UserSuspended} {
		_, err := s.SetStatus(ctx, dead.UserID, dead.RowVersion, to)
		if !errors.Is(err, domain.ErrInvalidTransition) {
			t.Errorf("deactivated -> %s: got %v, want ErrInvalidTransition — deactivation "+
				"is terminal (ADR-IDENTITY-001 §8.3)", to, err)
		}
	}

	// The row survives deactivation: audit events must keep an author.
	if _, err := s.Get(ctx, dead.UserID); err != nil {
		t.Errorf("the user row was removed by deactivation: %v — it is retained so audit "+
			"events keep an attributable author", err)
	}
}

func TestUserStore_SetStatusGuardsAndValidates(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	s := NewUserStore(pool)

	u := newStoredUser(ctx, t, s, "guards")

	if _, err := s.SetStatus(ctx, u.UserID, u.RowVersion, "banned"); err == nil {
		t.Error("an undefined status was accepted")
	} else if _, ok := domain.AsValidation(err); !ok {
		t.Errorf("undefined status: got %T, want a ValidationError", err)
	}

	if _, err := s.SetStatus(ctx, u.UserID, u.RowVersion+99, domain.UserActive); !errors.Is(err, domain.ErrVersionMismatch) {
		t.Errorf("stale row version: got %v, want ErrVersionMismatch", err)
	}

	if _, err := s.SetStatus(ctx, "usr_missing", 1, domain.UserActive); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown user: got %v, want ErrNotFound", err)
	}

	// A self-transition is a no-op that would burn a row version.
	if _, err := s.SetStatus(ctx, u.UserID, u.RowVersion, domain.UserInvited); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("invited -> invited: got %v, want ErrInvalidTransition", err)
	}

	// When BOTH the version is stale and the move is illegal, the version must be
	// reported. The caller's next act is to re-read and retry, and telling them
	// the transition is illegal would send them to argue with a state they have
	// not seen. This ordering is what the explicit check in SetStatus buys: the
	// SQL guard alone reports a version mismatch only after the transition check
	// has already rejected the move.
	_, err := s.SetStatus(ctx, u.UserID, u.RowVersion+99, domain.UserInvited)
	if !errors.Is(err, domain.ErrVersionMismatch) {
		t.Errorf("stale version AND illegal move: got %v, want ErrVersionMismatch — the "+
			"caller must be told to re-read before being told the move is wrong", err)
	}
}

// List pages by user_id, which is a ULID and therefore ordered by creation.
// Keyset pagination is used so a page does not shift under concurrent inserts.
func TestUserStore_ListPaginates(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	s := NewUserStore(pool)

	const n = 5
	for i := 0; i < n; i++ {
		newStoredUser(ctx, t, s, fmt.Sprintf("page%d", i))
	}

	first, err := s.List(ctx, 2, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("first page holds %d, want 2", len(first))
	}
	if first[0].UserID >= first[1].UserID {
		t.Error("page is not ordered by user_id")
	}

	second, err := s.List(ctx, 2, first[len(first)-1].UserID)
	if err != nil {
		t.Fatalf("list page 2: %v", err)
	}
	if len(second) == 0 {
		t.Fatal("second page is empty")
	}
	for _, u := range second {
		if u.UserID <= first[len(first)-1].UserID {
			t.Errorf("keyset cursor leaked a row from the previous page: %q", u.UserID)
		}
	}

	// The cap is only observable when more rows exist than the cap allows, so
	// enough are seeded to exceed it. Without this the assertion passes against
	// any limit, however large.
	for i := 0; i < defaultUserPageSize+5; i++ {
		newStoredUser(ctx, t, s, fmt.Sprintf("cap%d", i))
	}

	all, err := s.List(ctx, 0, "")
	if err != nil {
		t.Fatalf("list with no limit: %v", err)
	}
	if len(all) != defaultUserPageSize {
		t.Errorf("an unbounded request returned %d rows, want exactly the default page "+
			"size %d — an uncapped list hands the whole table to one caller",
			len(all), defaultUserPageSize)
	}

	// The max cap is only observable above maxUserPageSize rows, so the table is
	// filled past it in one statement rather than with a round trip per row.
	if _, err := pool.Exec(ctx, `
		INSERT INTO app_user (user_id, email)
		SELECT 'usr_bulk_' || lpad(i::text, 6, '0'), 'bulk' || i || '@example.com'
		  FROM generate_series(1, $1) AS i
		ON CONFLICT DO NOTHING`, maxUserPageSize+5); err != nil {
		t.Fatalf("bulk seed: %v", err)
	}
	// These rows exist only to make the cap observable. Left behind they slow
	// every later query in this package and, because packages run in parallel,
	// add contention to the timing-sensitive leader-election tests in
	// internal/ops — which is how a correctness test starts failing for reasons
	// that have nothing to do with correctness.
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM app_user WHERE user_id LIKE 'usr_bulk_%'`)
	})

	over, err := s.List(ctx, maxUserPageSize*10, "")
	if err != nil {
		t.Fatalf("list over max: %v", err)
	}
	if len(over) != maxUserPageSize {
		t.Errorf("a request for %d rows returned %d, want exactly %d — an unclamped "+
			"limit lets one caller pull the whole table",
			maxUserPageSize*10, len(over), maxUserPageSize)
	}
}

// app_user is global (ADR-IDENTITY-002 §3): the store must not scope by tenant,
// and a user created under one tenant context must be visible under another.
// If this ever fails, app_user has acquired tenant semantics it must not have.
func TestUserStore_IsNotTenantScoped(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	s := NewUserStore(pool)

	created := newStoredUser(ctx, t, s, "global")

	other := domain.WithTenant(ctx, &domain.Tenant{TenantID: "tn-somewhere-else"})
	got, err := s.Get(other, created.UserID)
	if err != nil {
		t.Fatalf("a user was not visible under a different tenant context: %v — app_user "+
			"is global and must not be scoped by tenant", err)
	}
	if got.UserID != created.UserID {
		t.Errorf("got %q, want %q", got.UserID, created.UserID)
	}
}
