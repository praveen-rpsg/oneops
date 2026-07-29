//go:build integration

package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/audit"
	"github.com/rpsg/oneops/internal/domain"
)

// craftEvent builds a fully-formed event with a caller-chosen prev_hash and a
// this_hash correctly computed over that prev — used to inject a real chain break
// (a wrong prev) through the store, which the immutable schema otherwise prevents.
func craftEvent(chain string, seq int64, prev []byte) *domain.AuditEvent {
	e := &domain.AuditEvent{
		ChainID: chain, Seq: seq,
		EventID: fmt.Sprintf("%s-c%d", chain, seq), OperationID: fmt.Sprintf("%s-cop%d", chain, seq),
		Operation: domain.OpApproval, Actor: "a",
		PayloadCanonical: []byte(`{}`),
		OccurredAt:       time.Unix(0, 0).UTC(), // microsecond-exact → round-trips through timestamptz
		PrevHash:         prev,
	}
	h, _ := audit.ChainHash(audit.EventHashInput{
		ChainID: chain, Seq: seq, EventID: e.EventID, Operation: string(e.Operation),
		Actor: e.Actor, OccurredAtUnixNanos: 0, PayloadCanonical: e.PayloadCanonical,
	}, prev)
	e.ThisHash = h
	return e
}

func storeInsert(t *testing.T, s *AuditStore, e *domain.AuditEvent) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendAuditEvent(ctx, tx, e); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("store insert: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func headSeq(t *testing.T, s *AuditStore, chain string) int64 {
	t.Helper()
	ctx := context.Background()
	tx, _ := s.pool.Begin(ctx)
	defer func() { _ = tx.Rollback(ctx) }()
	seq, _, _, err := s.ReadChainHead(ctx, tx, chain)
	if err != nil {
		t.Fatal(err)
	}
	return seq
}

func TestVerifierHappyPathAndZeroWrites(t *testing.T) {
	pool := graphPool(t)
	store := NewAuditStore(pool)
	app := NewAuditAppender(pool, store)
	ver := audit.NewVerifier(store)
	ctx := context.Background()
	chain := uniqueChain(t)

	for i := int64(1); i <= 3; i++ {
		if _, err := app.Append(ctx, appendInput(chain, i)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	before := headSeq(t, store, chain)

	res, err := ver.VerifyChain(ctx, chain)
	if err != nil {
		t.Fatalf("verify chain: %v", err)
	}
	if !res.OK || res.Checked != 3 || res.FirstBreakSeq != nil {
		t.Fatalf("happy path: %+v", res)
	}

	partial, err := ver.VerifyRange(ctx, chain, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !partial.OK || partial.Checked != 2 {
		t.Fatalf("partial range: %+v", partial)
	}

	// Zero database writes: the head is unchanged after verification.
	if after := headSeq(t, store, chain); after != before {
		t.Fatalf("verifier mutated state: head %d -> %d", before, after)
	}
}

func TestVerifierDetectsRealCorruption(t *testing.T) {
	pool := graphPool(t)
	store := NewAuditStore(pool)
	ver := audit.NewVerifier(store)
	ctx := context.Background()
	chain := uniqueChain(t)

	e1 := craftEvent(chain, 1, audit.GenesisPrevHash())
	wrongPrev := make([]byte, 32)
	wrongPrev[0] = 0xAB // not equal to e1.ThisHash
	e2 := craftEvent(chain, 2, wrongPrev)
	storeInsert(t, store, e1)
	storeInsert(t, store, e2)

	res, err := ver.VerifyChain(ctx, chain)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.OK || res.FirstBreakSeq == nil || *res.FirstBreakSeq != 2 ||
		res.BreakReason != audit.ReasonPrevHashMismatch || res.Checked != 1 {
		t.Fatalf("expected prev-hash break at seq 2: %+v", res)
	}
}
