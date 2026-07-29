//go:build integration

package ops

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// advisoryPids returns the backend pids currently holding a single-argument
// advisory lock (objsubid=1) — the form RunAsLeader uses.
func advisoryPids(ctx context.Context, t *testing.T, c *pgx.Conn) map[int]bool {
	t.Helper()
	rows, err := c.Query(ctx, "SELECT pid FROM pg_locks WHERE locktype='advisory' AND objsubid=1")
	if err != nil {
		t.Fatalf("query advisory pids: %v", err)
	}
	defer rows.Close()
	out := map[int]bool{}
	for rows.Next() {
		var pid int
		if err := rows.Scan(&pid); err != nil {
			t.Fatalf("scan pid: %v", err)
		}
		out[pid] = true
	}
	return out
}

// A leader that silently loses its advisory lock — its backend terminated out
// from under it (partition, connection reset) — used to keep running its workers;
// watchLeadership only logged "workers must be restarted". That is a permanent
// two-leader overlap. This test proves the fix (ADR-CONCURRENCY-003): the
// demoted leader's worker context is cancelled, and it re-enters the election
// and re-establishes leadership.
func TestRunAsLeader_DemotedLeaderStopsWorkersAndReElects(t *testing.T) {
	d := dsn(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const key int64 = 0x0117E0_7E57_02 // unique to this test

	admin, err := pgx.Connect(ctx, d)
	if err != nil {
		t.Fatalf("admin connect: %v", err)
	}
	defer admin.Close(context.Background())

	base := advisoryPids(ctx, t, admin)

	// start(lctx) is called each time this instance becomes leader; capture the
	// leadership context so we can observe it being cancelled on demotion.
	starts := make(chan context.Context, 4)
	RunAsLeader(ctx, d, key, slog.Default(), func(lctx context.Context) {
		starts <- lctx
	})

	var lctx1 context.Context
	select {
	case lctx1 = <-starts:
	case <-time.After(10 * time.Second):
		t.Fatal("instance never became leader")
	}
	if lctx1.Err() != nil {
		t.Fatal("leadership context cancelled before any demotion")
	}

	// Find the leader's lock backend (the advisory pid that appeared) and
	// terminate it, freeing the lock without the leader process noticing.
	var leaderPid int
	deadline := time.Now().Add(5 * time.Second)
	for leaderPid == 0 {
		for pid := range advisoryPids(ctx, t, admin) {
			if !base[pid] {
				leaderPid = pid
				break
			}
		}
		if leaderPid == 0 {
			if time.Now().After(deadline) {
				t.Fatal("could not locate the leader's lock backend")
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	if _, err := admin.Exec(ctx, "SELECT pg_terminate_backend($1)", leaderPid); err != nil {
		t.Fatalf("terminate leader backend: %v", err)
	}

	// The demoted leader must cancel its workers' context (the fix). Bounded by
	// leaderWatchInterval; allow generous slack.
	select {
	case <-lctx1.Done():
	case <-time.After(20 * time.Second):
		t.Fatal("demoted leader did not stop its workers — a two-leader overlap")
	}

	// And it must re-establish leadership under a fresh, live context.
	var lctx2 context.Context
	select {
	case lctx2 = <-starts:
	case <-time.After(30 * time.Second):
		t.Fatal("no leader re-established after demotion")
	}
	if lctx2.Err() != nil {
		t.Fatal("re-elected leadership context already cancelled")
	}
}
