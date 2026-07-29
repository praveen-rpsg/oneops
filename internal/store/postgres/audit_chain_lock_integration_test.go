//go:build integration

package postgres

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/audit"
	"github.com/rpsg/oneops/internal/domain"
)

// Trust Register audit (2026-07-29): entry 11 records "split-brain audit
// authority" as an eliminated class — the append follows a fixed sequence in one
// transaction, `EnsureChainHead → ReadChainHead(FOR UPDATE) → ChainHash →
// AppendAuditEvent → UpsertChainHead` (ADR-AUDIT-004).
//
// That chain-head lock is the most load-bearing invariant in the platform. The
// gapless committed prefix depends on it (ADR-CONCURRENCY-004, "no lost
// events"), and audit-derived ownership depends on the log being authoritative
// (ADR-TENANCY-003/004). Yet the lock is expressed as a *boolean argument* —
// `ReadChainHead(ctx, tx, chain, forUpdate)` — and entry 11's enforcement is an
// integration test with no architecture tier at all.
//
// This test does two things: it reproduces the original evidence (concurrent
// appends serialise into a contiguous prefix), and it proves the invariant is
// load-bearing by running the same concurrency with the lock omitted.
func TestAuditChainLock_IsLoadBearing(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewAuditStore(pool)
	appender := NewAuditAppender(pool, store)

	// --- Control: the real append path, which takes the lock -----------------
	locked := fmt.Sprintf("lock-control-%d", time.Now().UnixNano())
	const n = 12
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			in, err := audit.Resolve(domain.EventInput{
				ChainID:     locked,
				OperationID: fmt.Sprintf("op-%s-%d", locked, i),
				Operation:   domain.OpRatification,
				Payload:     []byte(`{}`),
			}, "system", time.Now().UTC())
			if err != nil {
				t.Errorf("resolve %d: %v", i, err)
				return
			}
			if _, aerr := appender.Append(ctx, in); aerr != nil {
				t.Errorf("append %d: %v", i, aerr)
			}
		}(i)
	}
	wg.Wait()

	seqs := chainSeqs(t, pool, locked)
	t.Logf("with the chain-head lock: %d concurrent appends -> seqs %v", n, seqs)
	if len(seqs) != n {
		t.Fatalf("locked append lost events: got %d rows, want %d", len(seqs), n)
	}
	for i, s := range seqs {
		if s != int64(i+1) {
			t.Fatalf("locked append is not a gapless prefix at position %d: %v", i, seqs)
		}
	}

	// --- The invariant under test: the same steps without the lock ------------
	//
	// The interleaving is forced rather than raced. An earlier version of this
	// test ran 12 goroutines and asserted that fewer than 12 committed, which is
	// probabilistic: if the scheduler happened to serialise them the test failed
	// spuriously. A verification that depends on timing is a verification defect,
	// so the two transactions are stepped by hand and the outcome is exact.
	unlocked := fmt.Sprintf("lock-attack-%d", time.Now().UnixNano())

	// The genesis head is created and committed first. Creating it inside both
	// transactions deadlocks them on the unique key — the second blocks on the
	// first's uncommitted insert — which is a property of the genesis race, not
	// of the invariant under test here.
	seedTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin seed: %v", err)
	}
	if err := store.EnsureChainHead(ctx, seedTx, unlocked, audit.GenesisPrevHash()); err != nil {
		t.Fatalf("ensure head: %v", err)
	}
	if err := seedTx.Commit(ctx); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	tx1, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx1: %v", err)
	}
	defer func() { _ = tx1.Rollback(ctx) }()
	tx2, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx2: %v", err)
	}
	defer func() { _ = tx2.Rollback(ctx) }()

	// Both read the head *without* the lock, so both observe the same last_seq
	// and both compute the same next seq. This is the step the appender takes
	// under FOR UPDATE, and the only difference here.
	seq1, _, _, err := store.ReadChainHead(ctx, tx1, unlocked)
	if err != nil {
		t.Fatalf("tx1 read head: %v", err)
	}
	seq2, _, _, err := store.ReadChainHead(ctx, tx2, unlocked)
	if err != nil {
		t.Fatalf("tx2 read head: %v", err)
	}
	t.Logf("without the lock, two concurrent transactions both observe last_seq=%d/%d "+
		"and therefore both claim seq %d", seq1, seq2, seq1+1)
	if seq1 != seq2 {
		t.Fatalf("the two readers disagreed (%d vs %d); the interleaving under test did not occur",
			seq1, seq2)
	}

	mkEvent := func(i int, seq int64) domain.AuditEvent {
		ev := domain.AuditEvent{
			TenantID: domain.SystemTenantID, ChainID: unlocked, Seq: seq,
			EventID:     fmt.Sprintf("evt_%s_%d", unlocked, i),
			OperationID: fmt.Sprintf("op-%s-%d", unlocked, i),
			Operation:   domain.OpRatification, Actor: "system",
			OccurredAt: time.Now().UTC(), PayloadCanonical: []byte(`{}`),
			PrevHash: audit.GenesisPrevHash(), ThisHash: make([]byte, 32),
		}
		ev.ThisHash[0] = byte(i + 1)
		return ev
	}

	ev1 := mkEvent(1, seq1+1)
	if err := store.AppendAuditEvent(ctx, tx1, &ev1); err != nil {
		t.Fatalf("tx1 append: %v", err)
	}
	if err := store.UpsertChainHead(ctx, tx1, unlocked, ev1.Seq, ev1.ThisHash); err != nil {
		t.Fatalf("tx1 upsert head: %v", err)
	}
	if err := tx1.Commit(ctx); err != nil {
		t.Fatalf("tx1 commit: %v", err)
	}

	ev2 := mkEvent(2, seq2+1)
	appendErr := store.AppendAuditEvent(ctx, tx2, &ev2)
	if appendErr == nil {
		appendErr = tx2.Commit(ctx)
	}

	got := chainSeqs(t, pool, unlocked)
	t.Logf("WITHOUT the lock: tx1 committed seq %d; tx2's append of the same seq -> %v; "+
		"chain now holds %v", ev1.Seq, appendErr, got)

	if appendErr == nil {
		t.Errorf("omitting the chain-head lock changed nothing — both transactions committed the "+
			"same seq. Either the (chain_id, seq) backstop is gone or this test no longer "+
			"exercises the interleaving, so the invariant it guards is unverified "+
			"(ADR-AUDIT-004/006). chain=%v", got)
	}
	if len(got) != 1 {
		t.Errorf("expected exactly one of the two racing appends to survive, got %v — the second "+
			"event was lost, which is the outcome the lock exists to prevent", got)
	}
}

// chainSeqs returns the committed seqs for a chain, ascending.
func chainSeqs(t *testing.T, pool *pgxpool.Pool, chain string) []int64 {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT seq FROM audit_event WHERE chain_id=$1 ORDER BY seq`, chain)
	if err != nil {
		t.Fatalf("read chain %s: %v", chain, err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var s int64
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan seq: %v", err)
		}
		out = append(out, s)
	}
	return out
}
