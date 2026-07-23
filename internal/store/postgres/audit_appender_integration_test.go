//go:build integration

package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rpsg/oneops/internal/audit"
	"github.com/rpsg/oneops/internal/domain"
)

func appendInput(chain string, idSeq int64) audit.AppendInput {
	return audit.AppendInput{
		ChainID:          chain,
		OperationID:      fmt.Sprintf("%s-op%d", chain, idSeq),
		EventID:          fmt.Sprintf("%s-e%d", chain, idSeq),
		Operation:        domain.OpReplacement,
		Actor:            "actor-1",
		OccurredAt:       time.Unix(0, 0).UTC(), // fixed → deterministic hashes
		PayloadCanonical: []byte(`{"k":"v"}`),
	}
}

// expectedHash independently recomputes this_hash via PRS-003 for assertion.
func expectedHash(t *testing.T, in audit.AppendInput, seq int64, prev []byte) []byte {
	t.Helper()
	h, err := audit.ChainHash(audit.EventHashInput{
		ChainID: in.ChainID, Seq: seq, EventID: in.EventID,
		Operation: string(in.Operation), Actor: in.Actor,
		OccurredAtUnixNanos: in.OccurredAt.UTC().UnixNano(), PayloadCanonical: in.PayloadCanonical,
	}, prev)
	if err != nil {
		t.Fatalf("expectedHash: %v", err)
	}
	return h
}

func TestAppenderGenesisAndSecondAppend(t *testing.T) {
	pool := graphPool(t)
	store := NewAuditStore(pool)
	app := NewAuditAppender(pool, store)
	ctx := context.Background()
	chain := uniqueChain(t)

	// Genesis (first) append: seq 1, prev = zero32, this = ChainHash over genesis.
	in1 := appendInput(chain, 1)
	e1, err := app.Append(ctx, in1)
	if err != nil {
		t.Fatalf("first append: %v", err)
	}
	if e1.Seq != 1 || !bytes.Equal(e1.PrevHash, make([]byte, 32)) {
		t.Fatalf("genesis: seq=%d prev=%x", e1.Seq, e1.PrevHash)
	}
	if !bytes.Equal(e1.ThisHash, expectedHash(t, in1, 1, audit.GenesisPrevHash())) {
		t.Fatal("genesis this_hash != independent ChainHash")
	}

	// Second append: seq 2, prev = first this_hash (chaining).
	in2 := appendInput(chain, 2)
	e2, err := app.Append(ctx, in2)
	if err != nil {
		t.Fatalf("second append: %v", err)
	}
	if e2.Seq != 2 || !bytes.Equal(e2.PrevHash, e1.ThisHash) {
		t.Fatalf("second: seq=%d prev!=first.this", e2.Seq)
	}
	if !bytes.Equal(e2.ThisHash, expectedHash(t, in2, 2, e1.ThisHash)) {
		t.Fatal("second this_hash != independent ChainHash")
	}

	// Chain-head advanced to (2, e2.this).
	tx, _ := pool.Begin(ctx)
	defer func() { _ = tx.Rollback(ctx) }()
	seq, hash, found, err := store.ReadChainHead(ctx, tx, chain, false)
	if err != nil || !found || seq != 2 || !bytes.Equal(hash, e2.ThisHash) {
		t.Fatalf("head advancement: seq=%d found=%v err=%v", seq, found, err)
	}

	// Deterministic: same inputs on a fresh chain reproduce the same genesis hash.
	chainB := uniqueChain(t)
	inB := appendInput(chainB, 1)
	eB, err := app.Append(ctx, inB)
	if err != nil {
		t.Fatal(err)
	}
	// Same field tuple except chain id → recompute with chainB.
	if !bytes.Equal(eB.ThisHash, expectedHash(t, inB, 1, audit.GenesisPrevHash())) {
		t.Fatal("deterministic genesis hash mismatch")
	}
}

// failingStore delegates to a real AuditStore but can force one step to fail,
// exercising the appender's rollback paths without bypassing the real store.
type failingStore struct {
	*AuditStore
	failAppend bool
	failUpsert bool
}

func (f *failingStore) AppendAuditEvent(ctx context.Context, tx pgx.Tx, e *domain.AuditEvent) error {
	if f.failAppend {
		return errors.New("forced append failure")
	}
	return f.AuditStore.AppendAuditEvent(ctx, tx, e)
}

func (f *failingStore) UpsertChainHead(ctx context.Context, tx pgx.Tx, chainID string, lastSeq int64, lastHash []byte) error {
	if f.failUpsert {
		return errors.New("forced upsert failure")
	}
	return f.AuditStore.UpsertChainHead(ctx, tx, chainID, lastSeq, lastHash)
}

func TestAppenderRollbackOnAppendFailure(t *testing.T) {
	pool := graphPool(t)
	real := NewAuditStore(pool)
	app := NewAuditAppender(pool, &failingStore{AuditStore: real, failAppend: true})
	ctx := context.Background()
	chain := uniqueChain(t)

	if _, err := app.Append(ctx, appendInput(chain, 1)); err == nil {
		t.Fatal("expected forced append failure")
	}
	// Nothing persisted: no event, and even the genesis head is rolled back.
	if _, err := real.ReadEvent(ctx, chain, 1); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("event must not exist after rollback: %v", err)
	}
	tx, _ := pool.Begin(ctx)
	defer func() { _ = tx.Rollback(ctx) }()
	if _, _, found, _ := real.ReadChainHead(ctx, tx, chain, false); found {
		t.Fatal("chain head must not exist after rollback")
	}
}

func TestAppenderRollbackOnHeadUpdateFailure(t *testing.T) {
	pool := graphPool(t)
	real := NewAuditStore(pool)
	app := NewAuditAppender(pool, &failingStore{AuditStore: real, failUpsert: true})
	ctx := context.Background()
	chain := uniqueChain(t)

	if _, err := app.Append(ctx, appendInput(chain, 1)); err == nil {
		t.Fatal("expected forced upsert failure")
	}
	// Atomicity: the event insert that ran before the head update is rolled back.
	if _, err := real.ReadEvent(ctx, chain, 1); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("event must not persist when head update fails: %v", err)
	}
}

func TestAppenderDuplicateEventConflict(t *testing.T) {
	pool := graphPool(t)
	store := NewAuditStore(pool)
	app := NewAuditAppender(pool, store)
	ctx := context.Background()
	chain := uniqueChain(t)

	if _, err := app.Append(ctx, appendInput(chain, 1)); err != nil {
		t.Fatalf("first: %v", err)
	}
	// Re-append the same EventID (idempotency id) → (chain,event_id) conflict.
	dup := appendInput(chain, 2)
	dup.EventID = fmt.Sprintf("%s-e1", chain)
	if _, err := app.Append(ctx, dup); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate event: want ErrConflict, got %v", err)
	}
	// Head remains at seq 1 (the conflicting append rolled back).
	tx, _ := pool.Begin(ctx)
	defer func() { _ = tx.Rollback(ctx) }()
	seq, _, _, _ := store.ReadChainHead(ctx, tx, chain, false)
	if seq != 1 {
		t.Fatalf("head advanced despite conflict: seq=%d", seq)
	}
}

func TestAppenderConcurrentSerialization(t *testing.T) {
	pool := graphPool(t)
	store := NewAuditStore(pool)
	app := NewAuditAppender(pool, store)
	ctx := context.Background()
	chain := uniqueChain(t)

	const n = 12
	var wg sync.WaitGroup
	var ok int64
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			in := appendInput(chain, int64(i)) // distinct event_id per goroutine
			<-start
			if _, err := app.Append(ctx, in); err != nil {
				t.Errorf("concurrent append %d: %v", i, err)
				return
			}
			atomic.AddInt64(&ok, 1)
		}(i)
	}
	close(start)
	wg.Wait()

	if ok != n {
		t.Fatalf("successes=%d, want %d", ok, n)
	}
	// FOR UPDATE serialization → head at n, contiguous seqs 1..n with no gaps.
	tx, _ := pool.Begin(ctx)
	defer func() { _ = tx.Rollback(ctx) }()
	seq, _, _, _ := store.ReadChainHead(ctx, tx, chain, false)
	if seq != n {
		t.Fatalf("head seq=%d, want %d (gap or duplicate seq)", seq, n)
	}
	for k := int64(1); k <= n; k++ {
		if _, err := store.ReadEvent(ctx, chain, k); err != nil {
			t.Fatalf("missing seq %d (non-contiguous chain): %v", k, err)
		}
	}
}
