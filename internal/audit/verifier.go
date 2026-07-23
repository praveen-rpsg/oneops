package audit

import (
	"bytes"
	"context"
	"errors"
	"math"

	"github.com/rpsg/oneops/internal/domain"
)

// Break reason categories recorded in domain.VerifyResult.BreakReason.
const (
	// ReasonSequenceGap: an event's seq is greater than the expected next seq.
	ReasonSequenceGap = "sequence_gap"
	// ReasonSequenceDuplicate: an event's seq is less than expected (duplicate or
	// out-of-order stream).
	ReasonSequenceDuplicate = "sequence_duplicate"
	// ReasonPrevHashMismatch: an event's prev_hash does not equal the prior event's
	// this_hash (or genesis for the first event of a full chain).
	ReasonPrevHashMismatch = "prev_hash_mismatch"
	// ReasonThisHashMismatch: the recomputed this_hash does not equal the persisted
	// this_hash — the event's bound fields or hash were tampered.
	ReasonThisHashMismatch = "this_hash_mismatch"
	// ReasonHashInputInvalid: an event's fields are not hashable (e.g., empty
	// payload) — a structurally invalid record.
	ReasonHashInputInvalid = "hash_input_invalid"
)

// errStop halts stream iteration at the first detected break (internal only).
var errStop = errors.New("audit: verification stop")

// RangeReader streams a chain's events in ascending seq order. It is satisfied by
// the AuditStore's VerifyRangeReader; the verifier consumes it and never reads or
// writes persistence directly.
type RangeReader interface {
	VerifyRangeReader(ctx context.Context, chainID string, fromSeq, toSeq int64, fn func(*domain.AuditEvent) error) error
}

// Verifier performs read-only integrity validation of an audit chain by
// reconstructing the hash chain with audit.ChainHash. It never repairs, rewrites,
// appends, or modifies data, and it computes hashes only via ChainHash.
type Verifier struct {
	reader RangeReader
}

// NewVerifier builds a verifier over a range reader (the AuditStore in production).
func NewVerifier(reader RangeReader) *Verifier {
	return &Verifier{reader: reader}
}

// VerifyChain verifies a whole chain from seq 1, requiring the first event's
// prev_hash to equal the genesis hash.
func (v *Verifier) VerifyChain(ctx context.Context, chainID string) (domain.VerifyResult, error) {
	return v.VerifyRange(ctx, chainID, 1, math.MaxInt64)
}

// VerifyRange verifies events with seq in [fromSeq, toSeq]. When fromSeq is 1 the
// first event is checked against genesis; otherwise the first event's stored
// prev_hash is adopted as the chaining anchor and continuity is verified from
// there.
func (v *Verifier) VerifyRange(ctx context.Context, chainID string, fromSeq, toSeq int64) (domain.VerifyResult, error) {
	res := domain.VerifyResult{ChainID: chainID}

	var expPrev []byte
	haveExpPrev := fromSeq == 1
	if haveExpPrev {
		expPrev = GenesisPrevHash()
	}
	expSeq := fromSeq

	err := v.reader.VerifyRangeReader(ctx, chainID, fromSeq, toSeq, func(e *domain.AuditEvent) error {
		// 1. sequence continuity
		if e.Seq != expSeq {
			reason := ReasonSequenceGap
			if e.Seq < expSeq {
				reason = ReasonSequenceDuplicate
			}
			setBreak(&res, e.Seq, reason)
			return errStop
		}
		// Adopt the first event's prev_hash as the anchor for a partial range.
		if !haveExpPrev {
			expPrev = e.PrevHash
			haveExpPrev = true
		}
		// 2. previous-hash linkage
		if !bytes.Equal(e.PrevHash, expPrev) {
			setBreak(&res, e.Seq, ReasonPrevHashMismatch)
			return errStop
		}
		// 3-4. recompute this_hash via ChainHash and compare
		got, herr := ChainHash(EventHashInput{
			ChainID:             e.ChainID,
			Seq:                 e.Seq,
			EventID:             e.EventID,
			Operation:           string(e.Operation),
			Actor:               e.Actor,
			OccurredAtUnixNanos: e.OccurredAt.UTC().UnixNano(),
			PayloadCanonical:    e.PayloadCanonical,
		}, e.PrevHash)
		if herr != nil {
			setBreak(&res, e.Seq, ReasonHashInputInvalid)
			return errStop
		}
		if !bytes.Equal(got, e.ThisHash) {
			setBreak(&res, e.Seq, ReasonThisHashMismatch)
			return errStop
		}
		// verified; capture the verified head (already-computed values) and advance
		res.Checked++
		res.HeadSeq = e.Seq
		res.HeadHash = append([]byte(nil), e.ThisHash...)
		expPrev = e.ThisHash
		expSeq = e.Seq + 1
		return nil
	})
	if err != nil && !errors.Is(err, errStop) {
		return domain.VerifyResult{}, err
	}
	res.OK = res.FirstBreakSeq == nil
	return res, nil
}

// setBreak records the first break only.
func setBreak(res *domain.VerifyResult, seq int64, reason string) {
	if res.FirstBreakSeq != nil {
		return
	}
	s := seq
	res.FirstBreakSeq = &s
	res.BreakReason = reason
}
