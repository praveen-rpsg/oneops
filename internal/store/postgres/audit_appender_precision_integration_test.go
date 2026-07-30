//go:build integration

package postgres

import (
	"bytes"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/audit"
)

// hashOf recomputes the chain hash for one appended event over the given
// occurred-at (nanoseconds) and previous hash, mirroring the appender/verifier.
func hashOf(t *testing.T, in audit.AppendInput, seq int64, occurredNanos int64, prev []byte) []byte {
	t.Helper()
	h, err := audit.ChainHash(audit.EventHashInput{
		ChainID: in.ChainID, Seq: seq, EventID: in.EventID, Operation: string(in.Operation),
		Actor: in.Actor, OccurredAtUnixNanos: occurredNanos, PayloadCanonical: in.PayloadCanonical,
	}, prev)
	if err != nil {
		t.Fatalf("hashOf: %v", err)
	}
	return h
}

// Sub-microsecond input is normalized to microsecond precision before hashing, so
// the hash produced equals the hash recomputed after the database round-trip, and
// verification of nanosecond-precision input succeeds.
func TestAppenderNormalizesSubMicrosecondTimestamp(t *testing.T) {
	pool := graphPool(t)
	store := NewAuditStore(pool)
	app := NewAuditAppender(pool, store)
	ver := audit.NewVerifier(store)
	ctx := adminTestCtx()
	chain := uniqueChain(t)

	in := appendInput(chain, 1)
	in.OccurredAt = time.Unix(0, 1500).UTC() // 1.5µs — sub-microsecond
	want := time.Unix(0, 1000).UTC()         // truncated to 1µs

	e, err := app.Append(ctx, in)
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	// (1) Normalized: the returned event carries the microsecond-truncated time.
	if !e.OccurredAt.Equal(want) {
		t.Fatalf("occurred_at not normalized: got %v (%d ns), want %v", e.OccurredAt, e.OccurredAt.UnixNano(), want)
	}

	// (2a) Hash-before-persistence equals ChainHash over the normalized value.
	if !bytes.Equal(e.ThisHash, hashOf(t, in, 1, want.UnixNano(), audit.GenesisPrevHash())) {
		t.Fatal("this_hash was not computed over the normalized timestamp")
	}

	// (2b) Hash after the database round-trip equals the hash before persistence.
	got, err := store.ReadEvent(ctx, chain, 1)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !got.OccurredAt.Equal(want) {
		t.Fatalf("stored occurred_at = %v, want %v", got.OccurredAt, want)
	}
	if !bytes.Equal(hashOf(t, in, 1, got.OccurredAt.UTC().UnixNano(), got.PrevHash), e.ThisHash) {
		t.Fatal("hash after round-trip != hash before persistence")
	}

	// (3) Verification succeeds with the original nanosecond-precision input.
	res, err := ver.VerifyChain(ctx, chain)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Checked != 1 {
		t.Fatalf("verify with nanosecond input: %+v", res)
	}
}

// An already-microsecond timestamp is unchanged by normalization (no drift), and
// verifies cleanly — the invariant that keeps existing deterministic hashes stable.
func TestAppenderAlreadyMicrosecondUnchanged(t *testing.T) {
	pool := graphPool(t)
	store := NewAuditStore(pool)
	app := NewAuditAppender(pool, store)
	ver := audit.NewVerifier(store)
	ctx := adminTestCtx()
	chain := uniqueChain(t)

	in := appendInput(chain, 1)
	in.OccurredAt = time.UnixMicro(12345).UTC() // exact microseconds

	e, err := app.Append(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if !e.OccurredAt.Equal(time.UnixMicro(12345).UTC()) {
		t.Fatalf("already-microsecond time drifted: %v", e.OccurredAt)
	}
	if !bytes.Equal(e.ThisHash, hashOf(t, in, 1, time.UnixMicro(12345).UTC().UnixNano(), audit.GenesisPrevHash())) {
		t.Fatal("hash drift for already-normalized timestamp")
	}
	res, err := ver.VerifyChain(ctx, chain)
	if err != nil || !res.OK {
		t.Fatalf("verify: %+v err=%v", res, err)
	}
}
