//go:build integration

package ops

import (
	"context"
	"os"
	"testing"
)

func dsn(t *testing.T) string {
	t.Helper()
	d := os.Getenv("TEST_DATABASE_URL")
	if d == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	return d
}

// The leader lock is mutual exclusion: exactly one holder at a time. This is the
// single-writer guarantee the background workers need — two instances holding it
// at once would double-deliver every webhook and double-execute every policy
// action, which was verified live against two replicas before this existed.
func TestLeaderLock_MutualExclusion(t *testing.T) {
	ctx := context.Background()
	// A key unique to this test so it never contends with a running platform or
	// a parallel test.
	const key int64 = 0x0117E0_7E57_01

	first, leader, err := TryAcquireLeadership(ctx, dsn(t), key)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if !leader {
		t.Fatal("first acquisition must become leader")
	}

	// A second, independent instance must be refused while the first holds it.
	second, leader2, err := TryAcquireLeadership(ctx, dsn(t), key)
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if leader2 {
		second.Close(ctx)
		first.Close(ctx)
		t.Fatal("two instances acquired leadership at once — the workers would double-execute")
	}

	// The leader's connection is healthy while it holds the lock.
	if !first.Healthy(ctx) {
		t.Error("leader must be healthy while holding the lock")
	}

	// Releasing the first (as a crash would, by dropping the connection) lets a
	// standby take over — the failover path.
	first.Close(ctx)

	third, leader3, err := TryAcquireLeadership(ctx, dsn(t), key)
	if err != nil {
		t.Fatalf("third acquire after release: %v", err)
	}
	if !leader3 {
		t.Fatal("leadership must be acquirable after the previous leader releases it")
	}
	third.Close(ctx)
}
