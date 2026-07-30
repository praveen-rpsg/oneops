//go:build integration

package postgres

import (
	"sync"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

// The administrative chain's serialisation guarantee, verified behaviourally.
//
// The constitutional chain has had this since ADR-AUDIT-006 —
// TestAuditChainLock_IsLoadBearing and TestAppenderConcurrentSerialization. The
// administrative chain had neither, which is why deleting its `FOR UPDATE`
// passed every gate in the repository. A static guard now catches that deletion;
// this test verifies the property the guard stands for, so the two are
// independent evidence rather than one mechanism checked twice.
//
// What the lock actually buys is progress. Without it, concurrent appends read
// the same head, compute the same sequence number, and all but one are refused
// by the primary key — the history stays intact and the operations are lost.
// Measured on the constitutional chain: twelve concurrent appends became three.
func TestAdminAuditChainLock_IsLoadBearing(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	users := NewUserStore(pool)

	// app_user carries no organisation, so every one of these acts lands on the
	// single platform chain (ADR-AUDIT-007 §6.8). They contend on one chain
	// head, which is the condition the lock exists for.
	const n = 8

	before := countAdminChainSeq(t, domain.PlatformAuditChain)

	var wg sync.WaitGroup
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			_, errs[i] = users.Create(ctx, newTestUser(t))
		}(i)
	}
	wg.Wait()

	succeeded := 0
	for i, err := range errs {
		if err == nil {
			succeeded++
			continue
		}
		t.Errorf("concurrent administrative act %d failed: %v", i, err)
	}
	if succeeded != n {
		t.Fatalf("successes=%d, want %d — concurrent appends to one chain must all commit, not "+
			"contend and fail. This is the guarantee the chain-head lock provides; without it "+
			"the losing transactions are refused by the primary key and their administrative "+
			"acts are lost (ADR-AUDIT-006).", succeeded, n)
	}

	// The sequence advanced by exactly n, with no gap and no duplicate. The
	// primary key alone would prevent a duplicate; only serialisation makes all
	// n of them commit.
	after := countAdminChainSeq(t, domain.PlatformAuditChain)
	if after-before != int64(n) {
		t.Errorf("platform chain advanced by %d, want %d", after-before, n)
	}
	assertAdminChainIsGapless(t, domain.PlatformAuditChain)
}

// countAdminChainSeq returns the chain's current head sequence.
func countAdminChainSeq(t *testing.T, chainID string) int64 {
	t.Helper()
	var seq int64
	err := testPool(t).QueryRow(adminTestCtx(),
		`SELECT coalesce(max(seq), 0) FROM admin_audit_event WHERE chain_id = $1`, chainID).Scan(&seq)
	if err != nil {
		t.Fatalf("read chain %s: %v", chainID, err)
	}
	return seq
}

// assertAdminChainIsGapless proves the committed sequence is 1..N with no holes.
//
// Gaplessness survives without the lock — the primary key and the atomicity of
// the head advance see to that — so this is not the property the lock provides.
// It is asserted because ADR-CONCURRENCY-004 consumes it from this chain, and a
// consumer's dependency should be checked where it is produced.
func assertAdminChainIsGapless(t *testing.T, chainID string) {
	t.Helper()
	var missing int
	err := testPool(t).QueryRow(adminTestCtx(), `
		SELECT count(*) FROM generate_series(1, (
			SELECT coalesce(max(seq), 0) FROM admin_audit_event WHERE chain_id = $1
		)) g
		WHERE g NOT IN (SELECT seq FROM admin_audit_event WHERE chain_id = $1)`,
		chainID).Scan(&missing)
	if err != nil {
		t.Fatalf("gap check on %s: %v", chainID, err)
	}
	if missing != 0 {
		t.Errorf("chain %s has %d gap(s); the committed log must be a gapless prefix "+
			"(ADR-CONCURRENCY-004 consumes this)", chainID, missing)
	}
}
