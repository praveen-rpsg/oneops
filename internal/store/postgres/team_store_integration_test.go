//go:build integration

package postgres

import (
	"errors"
	"strings"
	"testing"

	"github.com/oklog/ulid/v2"

	"github.com/rpsg/oneops/internal/domain"
)

// teamFixture creates a real organisation (and therefore a real tenant) and a
// real user, both through the constitutional chokepoint, mirroring
// membershipFixture.
func teamFixture(t *testing.T) (*domain.Organization, *domain.User) {
	t.Helper()
	pool := testPool(t)
	ctx := adminTestCtx()

	o, err := domain.NewOrganization("Team Probe",
		strings.ToLower("tp-"+ulid.Make().String()[:10]))
	if err != nil {
		t.Fatalf("new org: %v", err)
	}
	created, err := NewOrganizationStore(pool).Create(ctx, o)
	if err != nil {
		t.Fatalf("create org: %v", err)
	}
	u := newTestUser(t)
	if _, err := NewUserStore(pool).Create(ctx, u); err != nil {
		t.Fatalf("create user: %v", err)
	}
	return created, u
}

func TestTeamStore_CreateBuildsAnActiveTeam(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	org, _ := teamFixture(t)
	store := NewTeamStore(pool)

	tm, err := domain.NewTeam(org.OrgID, org.TenantID, "Platform Team")
	if err != nil {
		t.Fatalf("new team: %v", err)
	}
	created, err := store.Create(ctx, tm)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !created.Active() {
		t.Errorf("created team status = %q, want active", created.Status)
	}
	if created.RowVersion != 1 {
		t.Errorf("row version = %d, want 1", created.RowVersion)
	}

	got, err := store.Get(ctx, created.TeamID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "Platform Team" || got.OrgID != org.OrgID {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}

func TestTeamStore_GetUnknownIsNotFound(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	store := NewTeamStore(pool)

	if _, err := store.Get(ctx, "no-such-team"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("get unknown team = %v, want ErrNotFound", err)
	}
}

// Optimistic locking: a caller holding a stale row_version must be refused,
// not silently overwrite a concurrent change.
func TestTeamStore_UpdateProfileDetectsVersionConflict(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	org, _ := teamFixture(t)
	store := NewTeamStore(pool)

	tm, _ := domain.NewTeam(org.OrgID, org.TenantID, "Original Name")
	created, err := store.Create(ctx, tm)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	renamed, err := store.UpdateProfile(ctx, created.TeamID, created.RowVersion, "Renamed Once")
	if err != nil {
		t.Fatalf("first rename: %v", err)
	}
	if renamed.Name != "Renamed Once" || renamed.RowVersion != created.RowVersion+1 {
		t.Errorf("unexpected result: %+v", renamed)
	}

	// Reusing the now-stale row_version must be a conflict, not a second
	// successful rename.
	if _, err := store.UpdateProfile(ctx, created.TeamID, created.RowVersion, "Stale Rename"); !errors.Is(err, domain.ErrVersionMismatch) {
		t.Errorf("stale update = %v, want ErrVersionMismatch", err)
	}

	// The row actually held the winning name, not the refused one.
	got, err := store.Get(ctx, created.TeamID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "Renamed Once" {
		t.Errorf("name = %q, want %q — a refused update must not apply", got.Name, "Renamed Once")
	}
}

func TestTeamStore_UpdateProfileOnUnknownTeamIsNotFound(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	store := NewTeamStore(pool)

	if _, err := store.UpdateProfile(ctx, "no-such-team", 1, "New Name"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("update unknown team = %v, want ErrNotFound", err)
	}
}

// SetStatus archives and reactivates under the same optimistic-locking
// discipline as UpdateProfile.
func TestTeamStore_SetStatusArchivesAndReactivates(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	org, _ := teamFixture(t)
	store := NewTeamStore(pool)

	tm, _ := domain.NewTeam(org.OrgID, org.TenantID, "Lifecycle Team")
	created, err := store.Create(ctx, tm)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	archived, err := store.SetStatus(ctx, created.TeamID, created.RowVersion, domain.TeamArchived)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if archived.Status != domain.TeamArchived {
		t.Errorf("status = %q, want archived", archived.Status)
	}

	// The stale version is now the pre-archive one; reusing it must conflict.
	if _, err := store.SetStatus(ctx, created.TeamID, created.RowVersion, domain.TeamActive); !errors.Is(err, domain.ErrVersionMismatch) {
		t.Errorf("stale status change = %v, want ErrVersionMismatch", err)
	}

	reactivated, err := store.SetStatus(ctx, created.TeamID, archived.RowVersion, domain.TeamActive)
	if err != nil {
		t.Fatalf("reactivate: %v", err)
	}
	if !reactivated.Active() {
		t.Errorf("reactivated status = %q, want active", reactivated.Status)
	}
}

func TestTeamStore_SetStatusRejectsUndefinedStatus(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	org, _ := teamFixture(t)
	store := NewTeamStore(pool)

	tm, _ := domain.NewTeam(org.OrgID, org.TenantID, "Bad Status Team")
	created, err := store.Create(ctx, tm)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := store.SetStatus(ctx, created.TeamID, created.RowVersion, "deleted"); err == nil {
		t.Error("an undefined status was accepted")
	}
}

// AddMember is idempotent: adding an existing member returns the same row
// rather than erroring, the same shape MembershipStore.Grant uses.
func TestTeamStore_AddMemberIsIdempotent(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	org, user := teamFixture(t)
	store := NewTeamStore(pool)

	tm, _ := domain.NewTeam(org.OrgID, org.TenantID, "Membership Team")
	team, err := store.Create(ctx, tm)
	if err != nil {
		t.Fatalf("create team: %v", err)
	}

	m, err := domain.NewTeamMembership(team.TeamID, org.TenantID, user.UserID)
	if err != nil {
		t.Fatalf("new team membership: %v", err)
	}
	first, err := store.AddMember(ctx, m)
	if err != nil {
		t.Fatalf("add member: %v", err)
	}

	m2, _ := domain.NewTeamMembership(team.TeamID, org.TenantID, user.UserID)
	second, err := store.AddMember(ctx, m2)
	if err != nil {
		t.Fatalf("re-add member: %v", err)
	}
	if second.TeamMembershipID != first.TeamMembershipID {
		t.Errorf("re-add minted a second row: %s vs %s", second.TeamMembershipID, first.TeamMembershipID)
	}

	members, err := store.ListMembers(ctx, team.TeamID, 0, "")
	if err != nil {
		t.Fatalf("list members: %v", err)
	}
	if len(members) != 1 {
		t.Errorf("team has %d members, want exactly 1", len(members))
	}
}

// RemoveMember is a real delete: the row is gone, not merely flagged, because
// team_membership authorises nothing on its own.
func TestTeamStore_RemoveMemberDeletesTheRow(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	org, user := teamFixture(t)
	store := NewTeamStore(pool)

	tm, _ := domain.NewTeam(org.OrgID, org.TenantID, "Removal Team")
	team, err := store.Create(ctx, tm)
	if err != nil {
		t.Fatalf("create team: %v", err)
	}
	m, _ := domain.NewTeamMembership(team.TeamID, org.TenantID, user.UserID)
	if _, err := store.AddMember(ctx, m); err != nil {
		t.Fatalf("add member: %v", err)
	}

	if err := store.RemoveMember(ctx, team.TeamID, user.UserID); err != nil {
		t.Fatalf("remove member: %v", err)
	}

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM team_membership WHERE team_id = $1 AND user_id = $2`,
		team.TeamID, user.UserID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Error("the row survived removal — team_membership must be a real delete")
	}

	// Removing again is a not-found, not a silent success.
	if err := store.RemoveMember(ctx, team.TeamID, user.UserID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("second removal = %v, want ErrNotFound", err)
	}
}

// Listing is bounded and keyset-paginated over a unique key, so a page can
// neither skip a row nor repeat one — the same property membership's list
// path guarantees.
func TestTeamStore_ListPaginatesDeterministically(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	org, _ := teamFixture(t)
	store := NewTeamStore(pool)

	for i := 0; i < 7; i++ {
		tm, _ := domain.NewTeam(org.OrgID, org.TenantID, "Team")
		if _, err := store.Create(ctx, tm); err != nil {
			t.Fatalf("seed team: %v", err)
		}
	}

	seen := map[string]int{}
	after, pages := "", 0
	for {
		page, err := store.ListByOrg(ctx, org.OrgID, 3, after)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		for _, tm := range page {
			seen[tm.TeamID]++
		}
		pages++
		if len(page) < 3 {
			break
		}
		after = page[len(page)-1].TeamID
		if pages > 50 {
			t.Fatal("pagination did not terminate")
		}
	}
	if pages < 2 {
		t.Fatalf("only %d page(s) — pagination was not exercised", pages)
	}
	if len(seen) != 7 {
		t.Errorf("paging visited %d distinct teams, want 7", len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("team %s appeared %d times across pages", id, n)
		}
	}
}

// A page is bounded whatever the caller asks for.
func TestTeamStore_ListPagesAreBounded(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	org, _ := teamFixture(t)
	store := NewTeamStore(pool)

	for i := 0; i < 3; i++ {
		tm, _ := domain.NewTeam(org.OrgID, org.TenantID, "Team")
		if _, err := store.Create(ctx, tm); err != nil {
			t.Fatalf("seed team: %v", err)
		}
	}

	got, err := store.ListByOrg(ctx, org.OrgID, 100000, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) > maxTeamPageSize {
		t.Errorf("a request for 100000 returned %d rows; the page must be capped at %d",
			len(got), maxTeamPageSize)
	}
	if maxTeamPageSize <= defaultTeamPageSize {
		t.Error("the cap must exceed the default, or the default is the cap")
	}
	if _, err := store.ListByOrg(ctx, org.OrgID, 0, ""); err != nil {
		t.Fatalf("default limit: %v", err)
	}
}
