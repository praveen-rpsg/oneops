//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/oklog/ulid/v2"

	"github.com/rpsg/oneops/internal/audit"
	"github.com/rpsg/oneops/internal/domain"
)

func newTestUser(t *testing.T) *domain.User {
	t.Helper()
	id := ulid.Make().String()
	return &domain.User{
		UserID: id, Email: "chokepoint-" + id + "@example.com",
		DisplayName: "Chokepoint", Status: domain.UserInvited,
	}
}

// THE ACCEPTANCE CRITERION for OPS-S035: one write path, not per-handler calls.
// An administrative mutation and its audit record are one transaction.
func TestChokepoint_AdministrativeMutationWritesItsAuditRecord(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	users := NewUserStore(pool)

	u := newTestUser(t)
	created, err := users.Create(ctx, u)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	var operation, actor, subjectUser, chainID string
	var seq int64
	if err := pool.QueryRow(ctx, `
		SELECT operation, actor, subject_user_id, chain_id, seq
		  FROM admin_audit_event WHERE subject_user_id = $1`, created.UserID,
	).Scan(&operation, &actor, &subjectUser, &chainID, &seq); err != nil {
		t.Fatalf("no administrative audit record for the created user: %v", err)
	}

	if operation != string(domain.AdminUserCreated) {
		t.Errorf("operation = %q, want %q", operation, domain.AdminUserCreated)
	}
	if actor != "test-platform-admin" {
		t.Errorf("actor = %q — the record must name the principal that performed the act", actor)
	}
	// app_user is global and has no organisation, so §6.8 puts the act on the
	// platform chain.
	if chainID != domain.PlatformAuditChain {
		t.Errorf("chain = %q, want the platform chain %q", chainID, domain.PlatformAuditChain)
	}
	if seq < 1 {
		t.Errorf("seq = %d, want >= 1", seq)
	}
}

// ADR-AUDIT-007 §6.9: the append and the mutation commit together or neither
// does. The mutation is made to fail after it has run, and the audit record
// must not survive it — nor must the user.
func TestChokepoint_FailedMutationLeavesNoAuditRecord(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()

	// A duplicate email fails the insert, so the whole transaction — the audit
	// append included — must roll back.
	users := NewUserStore(pool)
	first := newTestUser(t)
	if _, err := users.Create(ctx, first); err != nil {
		t.Fatalf("seed: %v", err)
	}
	clash := newTestUser(t)
	clash.Email = first.Email
	if _, err := users.Create(ctx, clash); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate email = %v, want ErrConflict", err)
	}

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM admin_audit_event WHERE subject_user_id = $1`, clash.UserID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("a failed administrative mutation left %d audit record(s); the append must roll back with it", n)
	}
}

// An administrative act that cannot be attributed must fail, and the mutation
// must not happen either. There is deliberately no fallback actor: inventing
// one produces a record that says an act occurred and lies about who performed
// it, which is worse than no record.
func TestChokepoint_RefusesAnUnattributableAct(t *testing.T) {
	pool := testPool(t)
	users := NewUserStore(pool)

	u := newTestUser(t)
	// Bare context — no actor, as an unauthenticated or unthreaded path would be.
	_, err := users.Create(context.Background(), u)
	if !errors.Is(err, domain.ErrNoActor) {
		t.Fatalf("create without an actor = %v, want ErrNoActor", err)
	}

	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM app_user WHERE user_id = $1`, u.UserID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Error("the user was created despite the act being unauditable — §6.9 requires the act to fail")
	}
}

// The sealed payload must carry the subject, and the database must refuse a row
// whose column disagrees with it. Without this the record's "to whom" would sit
// outside the chain hash and a rewrite could retarget the act silently.
func TestChokepoint_SubjectIsBoundIntoTheSealedPayload(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	users := NewUserStore(pool)

	u := newTestUser(t)
	created, err := users.Create(ctx, u)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	var inPayload string
	var canonical []byte
	if err := pool.QueryRow(ctx, `
		SELECT payload ->> 'subject_user_id', payload_canonical
		  FROM admin_audit_event WHERE subject_user_id = $1`, created.UserID,
	).Scan(&inPayload, &canonical); err != nil {
		t.Fatalf("read payload: %v", err)
	}
	if inPayload != created.UserID {
		t.Errorf("payload subject_user_id = %q, want %q — the subject is not inside the hash", inPayload, created.UserID)
	}

	// The canonical bytes are what ChainHash sealed; they must be canonical.
	again, err := audit.Canonicalize(canonical)
	if err != nil {
		t.Fatalf("stored payload is not canonical JSON: %v", err)
	}
	if string(again) != string(canonical) {
		t.Error("stored payload_canonical is not in canonical form, so its hash is not reproducible")
	}

	// The database refuses a row whose column contradicts its sealed payload.
	_, err = pool.Exec(ctx, `
		INSERT INTO admin_audit_event
			(chain_id, seq, event_id, operation_id, operation, actor,
			 subject_user_id, payload_canonical, payload, prev_hash, this_hash)
		VALUES ('chain:platform', 9999, $1, $2, 'user.created', 'a',
			'CLAIMED-VICTIM', '{"subject_user_id":"SOMEONE-ELSE"}',
			'{"subject_user_id":"SOMEONE-ELSE"}'::jsonb, $3, $4)`,
		ulid.Make().String(), ulid.Make().String(),
		make([]byte, 32), append([]byte{0x7f}, make([]byte, 31)...))
	if err == nil {
		t.Error("a row whose subject column disagrees with its sealed payload was accepted")
	}
}

// S2 closure: the tests above make the MUTATION fail. This makes the APPEND
// fail — an operation outside the vocabulary — and proves the mutation is
// rolled back with it. Without this, swallowing the append error is invisible:
// every act would still be written and only the audit record would be lost,
// which is the precise failure §6.9 exists to prevent.
func TestChokepoint_FailedAuditAppendRollsBackTheMutation(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	u := newTestUser(t)

	err := withAdminAudit(ctx, pool,
		func() []domain.AdminAct {
			return []domain.AdminAct{{
				Operation: domain.AdminOperation("not.a.real.operation"),
				Subject:   domain.AdminSubject{UserID: u.UserID},
			}}
		},
		func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx, `
				INSERT INTO app_user (user_id, email, display_name, status)
				VALUES ($1, $2, $3, 'invited')`, u.UserID, u.Email, u.DisplayName)
			return e
		})
	if !errors.Is(err, domain.ErrInvalidAdminOperation) {
		t.Fatalf("append with an invalid operation = %v, want ErrInvalidAdminOperation", err)
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app_user WHERE user_id = $1`, u.UserID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Error("the mutation committed even though its audit append failed — §6.9 requires both or neither")
	}
}

// S5 closure: every other test runs as the table owner, who is exempt from the
// ACLs entirely, so the grant set OPS-S035's migration issues is never
// exercised. This runs the real append as oneops_app — the role the request
// path actually assumes — which is the only way to prove the grants are
// sufficient. It failed before the migration granted UPDATE on the chain head,
// because PostgreSQL requires UPDATE privilege to take a FOR UPDATE lock.
func TestChokepoint_AppendsAsTheRequestPathRole(t *testing.T) {
	testPool(t) // schema + migrations
	scoped := tenantScopedPool(t)
	ctx := adminTestCtx()

	users := NewUserStore(scoped)
	u := newTestUser(t)
	created, err := users.Create(ctx, u)
	if err != nil {
		t.Fatalf("create as oneops_app: %v — the appender's grants are insufficient", err)
	}

	// The row landed. Read it back with the privileged pool, because §6.5
	// forbids oneops_app from reading the administrative trail.
	priv := testPool(t)
	var actor string
	if err := priv.QueryRow(context.Background(),
		`SELECT actor FROM admin_audit_event WHERE subject_user_id = $1`, created.UserID).Scan(&actor); err != nil {
		t.Fatalf("no audit record written by the request-path role: %v", err)
	}
	if actor != "test-platform-admin" {
		t.Errorf("actor = %q", actor)
	}
}
