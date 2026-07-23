package audit

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// sliceReader is a synthetic RangeReader over a fixed event stream, letting the
// verifier be tested against corrupted/gapped/duplicate streams that the
// immutable, PK-protected database can never actually produce.
type sliceReader []*domain.AuditEvent

func (r sliceReader) VerifyRangeReader(_ context.Context, _ string, from, to int64, fn func(*domain.AuditEvent) error) error {
	for _, e := range r {
		if e.Seq < from || e.Seq > to {
			continue
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	return nil
}

// validChain builds a correctly hash-chained sequence of n events from genesis.
func validChain(chainID string, n int) []*domain.AuditEvent {
	var events []*domain.AuditEvent
	prev := GenesisPrevHash()
	for seq := int64(1); seq <= int64(n); seq++ {
		e := &domain.AuditEvent{
			ChainID: chainID, Seq: seq,
			EventID: fmt.Sprintf("e%d", seq), OperationID: fmt.Sprintf("op%d", seq),
			Operation: domain.OpReplacement, Actor: "a",
			PayloadCanonical: []byte(`{"k":"v"}`),
			OccurredAt:       time.Unix(0, seq).UTC(),
			PrevHash:         prev,
		}
		h, _ := ChainHash(EventHashInput{
			ChainID: chainID, Seq: seq, EventID: e.EventID, Operation: string(e.Operation),
			Actor: e.Actor, OccurredAtUnixNanos: e.OccurredAt.UTC().UnixNano(), PayloadCanonical: e.PayloadCanonical,
		}, prev)
		e.ThisHash = h
		events = append(events, e)
		prev = h
	}
	return events
}

func verifyChain(t *testing.T, events []*domain.AuditEvent) domain.VerifyResult {
	t.Helper()
	res, err := NewVerifier(sliceReader(events)).VerifyChain(context.Background(), "c")
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	return res
}

func TestVerifyEmptyChain(t *testing.T) {
	res := verifyChain(t, nil)
	if !res.OK || res.Checked != 0 || res.FirstBreakSeq != nil {
		t.Fatalf("empty chain: %+v", res)
	}
}

func TestVerifySingleEvent(t *testing.T) {
	res := verifyChain(t, validChain("c", 1))
	if !res.OK || res.Checked != 1 {
		t.Fatalf("single: %+v", res)
	}
}

func TestVerifyMultiEvent(t *testing.T) {
	res := verifyChain(t, validChain("c", 5))
	if !res.OK || res.Checked != 5 || res.BreakReason != "" {
		t.Fatalf("multi: %+v", res)
	}
}

func TestVerifyCorruptedPrevHash(t *testing.T) {
	ev := validChain("c", 3)
	ev[1].PrevHash = append([]byte(nil), ev[1].PrevHash...)
	ev[1].PrevHash[0] ^= 0xFF // tamper prev of event 2
	res := verifyChain(t, ev)
	if res.OK || res.FirstBreakSeq == nil || *res.FirstBreakSeq != 2 ||
		res.BreakReason != ReasonPrevHashMismatch || res.Checked != 1 {
		t.Fatalf("corrupt prev: %+v", res)
	}
}

func TestVerifyCorruptedThisHash(t *testing.T) {
	ev := validChain("c", 3)
	ev[1].ThisHash = append([]byte(nil), ev[1].ThisHash...)
	ev[1].ThisHash[0] ^= 0xFF // tamper this_hash of event 2
	res := verifyChain(t, ev)
	if res.OK || res.FirstBreakSeq == nil || *res.FirstBreakSeq != 2 ||
		res.BreakReason != ReasonThisHashMismatch || res.Checked != 1 {
		t.Fatalf("corrupt this: %+v", res)
	}
}

func TestVerifySequenceGap(t *testing.T) {
	ev := validChain("c", 3)
	stream := []*domain.AuditEvent{ev[0], ev[2]} // drop seq 2
	res := verifyChain(t, stream)
	if res.OK || *res.FirstBreakSeq != 3 || res.BreakReason != ReasonSequenceGap || res.Checked != 1 {
		t.Fatalf("gap: %+v", res)
	}
}

func TestVerifyDuplicateSequence(t *testing.T) {
	ev := validChain("c", 2)
	stream := []*domain.AuditEvent{ev[0], ev[0]} // seq 1 twice
	res := verifyChain(t, stream)
	if res.OK || *res.FirstBreakSeq != 1 || res.BreakReason != ReasonSequenceDuplicate || res.Checked != 1 {
		t.Fatalf("duplicate: %+v", res)
	}
}

func TestVerifyTruncatedChainLeading(t *testing.T) {
	ev := validChain("c", 3)
	stream := []*domain.AuditEvent{ev[1], ev[2]} // missing seq 1
	res := verifyChain(t, stream)
	if res.OK || *res.FirstBreakSeq != 2 || res.BreakReason != ReasonSequenceGap || res.Checked != 0 {
		t.Fatalf("truncated: %+v", res)
	}
}

func TestVerifyHashInputInvalid(t *testing.T) {
	e := &domain.AuditEvent{
		ChainID: "c", Seq: 1, EventID: "e1", OperationID: "op1",
		Operation: domain.OpReplacement, Actor: "a",
		PayloadCanonical: nil, // empty → ChainHash rejects
		OccurredAt:       time.Unix(0, 1).UTC(),
		PrevHash:         GenesisPrevHash(),
		ThisHash:         make([]byte, 32),
	}
	res := verifyChain(t, []*domain.AuditEvent{e})
	if res.OK || *res.FirstBreakSeq != 1 || res.BreakReason != ReasonHashInputInvalid {
		t.Fatalf("invalid input: %+v", res)
	}
}

func TestVerifyPartialRangeAnchorsFirstPrev(t *testing.T) {
	ev := validChain("c", 5)
	res, err := NewVerifier(sliceReader(ev)).VerifyRange(context.Background(), "c", 3, 5)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Checked != 3 {
		t.Fatalf("partial range: %+v", res)
	}
}

func TestVerifyDeterministic(t *testing.T) {
	ev := validChain("c", 4)
	first := verifyChain(t, ev)
	for i := 0; i < 5; i++ {
		got := verifyChain(t, ev)
		if got.OK != first.OK || got.Checked != first.Checked || got.BreakReason != first.BreakReason {
			t.Fatal("verification is not deterministic")
		}
	}
}

func TestVerifyLargeStreamedChain(t *testing.T) {
	res := verifyChain(t, validChain("c", 10000))
	if !res.OK || res.Checked != 10000 {
		t.Fatalf("large: OK=%v checked=%d", res.OK, res.Checked)
	}
}
