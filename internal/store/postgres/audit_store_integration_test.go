//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

var auditChainSeq int64

// uniqueChain returns a chain id unique to this run. Audit rows are immutable
// (UPDATE/DELETE/TRUNCATE are blocked), so tests must not reuse chains.
func uniqueChain(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("test-%d-%d", time.Now().UnixNano(), atomic.AddInt64(&auditChainSeq, 1))
}

func zero32() []byte { return make([]byte, 32) }

// h32 returns a distinct 32-byte value seeded by n (for unique this_hash values).
func h32(n int64) []byte {
	b := make([]byte, 32)
	b[0] = byte(n)
	b[1] = byte(n >> 8)
	return b
}

func mkEvent(chain string, seq int64) *domain.AuditEvent {
	return &domain.AuditEvent{
		ChainID:          chain,
		Seq:              seq,
		EventID:          fmt.Sprintf("%s-e%d", chain, seq),
		OperationID:      fmt.Sprintf("%s-op%d", chain, seq),
		Operation:        domain.OpReplacement,
		Actor:            "actor-1",
		PayloadCanonical: []byte(`{"k":"v"}`),
		PrevHash:         zero32(),
		ThisHash:         h32(seq),
		OccurredAt:       time.Now().UTC(),
	}
}

// appendCommitted appends one event in its own committed transaction.
func appendCommitted(t *testing.T, s *AuditStore, e *domain.AuditEvent) error {
	t.Helper()
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := s.AppendAuditEvent(ctx, tx, e); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

func TestAuditAppendAndReadBack(t *testing.T) {
	s := NewAuditStore(graphPool(t))
	chain := uniqueChain(t)
	e := mkEvent(chain, 1)
	if err := appendCommitted(t, s, e); err != nil {
		t.Fatalf("append: %v", err)
	}
	got, err := s.ReadEvent(context.Background(), chain, 1)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.EventID != e.EventID || got.Operation != domain.OpReplacement ||
		string(got.PayloadCanonical) != `{"k":"v"}` || len(got.ThisHash) != 32 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if _, err := s.ReadEvent(context.Background(), chain, 99); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing event: want ErrNotFound, got %v", err)
	}
}

func TestAuditDuplicateKeyConflict(t *testing.T) {
	s := NewAuditStore(graphPool(t))
	chain := uniqueChain(t)
	if err := appendCommitted(t, s, mkEvent(chain, 1)); err != nil {
		t.Fatalf("first append: %v", err)
	}
	// same (chain_id, seq) → ErrConflict
	dup := mkEvent(chain, 1)
	dup.EventID, dup.ThisHash = chain+"-other", h32(999)
	if err := appendCommitted(t, s, dup); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate seq: want ErrConflict, got %v", err)
	}
	// same (chain_id, event_id) at a new seq → ErrConflict
	dupID := mkEvent(chain, 2)
	dupID.EventID = chain + "-e1"
	if err := appendCommitted(t, s, dupID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate event_id: want ErrConflict, got %v", err)
	}
}

func TestAuditChainHeadLifecycle(t *testing.T) {
	s := NewAuditStore(graphPool(t))
	ctx := context.Background()
	chain := uniqueChain(t)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// genesis
	if err := s.EnsureChainHead(ctx, tx, chain, zero32()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	// idempotent
	if err := s.EnsureChainHead(ctx, tx, chain, zero32()); err != nil {
		t.Fatalf("ensure idempotent: %v", err)
	}
	seq, hash, found, err := s.ReadChainHead(ctx, tx, chain, true)
	if err != nil || !found || seq != 0 || len(hash) != 32 {
		t.Fatalf("head after genesis: seq=%d found=%v err=%v", seq, found, err)
	}
	// advance
	if err := s.UpsertChainHead(ctx, tx, chain, 1, h32(1)); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	seq, hash, found, err = s.ReadChainHead(ctx, tx, chain, false)
	if err != nil || !found || seq != 1 || hash[0] != 1 {
		t.Fatalf("head after advance: seq=%d hash0=%d err=%v", seq, hash[0], err)
	}
	// unknown chain → not found
	_, _, found, err = s.ReadChainHead(ctx, tx, uniqueChain(t), false)
	if err != nil || found {
		t.Fatalf("unknown head: found=%v err=%v", found, err)
	}
}

func TestAuditTransactionAtomicityAndRollback(t *testing.T) {
	s := NewAuditStore(graphPool(t))
	ctx := context.Background()
	chain := uniqueChain(t)

	// committed: event + head advance persist together
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureChainHead(ctx, tx, chain, zero32()); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendAuditEvent(ctx, tx, mkEvent(chain, 1)); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertChainHead(ctx, tx, chain, 1, h32(1)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := s.ReadEvent(ctx, chain, 1); err != nil {
		t.Fatalf("committed event should exist: %v", err)
	}

	// rolled back: nothing persists
	chain2 := uniqueChain(t)
	tx2, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendAuditEvent(ctx, tx2, mkEvent(chain2, 1)); err != nil {
		t.Fatal(err)
	}
	_ = tx2.Rollback(ctx)
	if _, err := s.ReadEvent(ctx, chain2, 1); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("rolled-back event must not exist: got %v", err)
	}
}

func TestAuditVerifyRangeReaderOrdered(t *testing.T) {
	s := NewAuditStore(graphPool(t))
	ctx := context.Background()
	chain := uniqueChain(t)
	for seq := int64(1); seq <= 3; seq++ {
		if err := appendCommitted(t, s, mkEvent(chain, seq)); err != nil {
			t.Fatalf("append %d: %v", seq, err)
		}
	}
	var seen []int64
	if err := s.VerifyRangeReader(ctx, chain, 1, 3, func(e *domain.AuditEvent) error {
		seen = append(seen, e.Seq)
		return nil
	}); err != nil {
		t.Fatalf("range reader: %v", err)
	}
	if len(seen) != 3 || seen[0] != 1 || seen[1] != 2 || seen[2] != 3 {
		t.Fatalf("range order = %v, want [1 2 3]", seen)
	}
	// fn error propagates and stops iteration
	stop := errors.New("stop")
	if err := s.VerifyRangeReader(ctx, chain, 1, 3, func(*domain.AuditEvent) error { return stop }); !errors.Is(err, stop) {
		t.Fatalf("fn error should propagate, got %v", err)
	}
}

func TestAuditConcurrentAppendSameSeq(t *testing.T) {
	s := NewAuditStore(graphPool(t))
	chain := uniqueChain(t)
	const n = 8
	var wg sync.WaitGroup
	var success, conflict int64
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			e := mkEvent(chain, 1)
			e.EventID = fmt.Sprintf("%s-e1-%d", chain, i) // distinct event_id; collide on (chain,seq)
			e.ThisHash = h32(int64(1000 + i))             // distinct this_hash
			<-start
			err := appendCommitted(t, s, e)
			switch {
			case err == nil:
				atomic.AddInt64(&success, 1)
			case errors.Is(err, domain.ErrConflict):
				atomic.AddInt64(&conflict, 1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if success != 1 || conflict != n-1 {
		t.Fatalf("concurrent same-seq: success=%d conflict=%d, want 1 and %d", success, conflict, n-1)
	}
}
