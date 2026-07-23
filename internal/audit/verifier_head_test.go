package audit

import (
	"bytes"
	"testing"
)

// A fully verified chain reports its head sequence and head hash.
func TestVerifyResultHeadSuccess(t *testing.T) {
	ev := validChain("c", 4)
	res := verifyChain(t, ev)
	if !res.OK {
		t.Fatalf("expected OK: %+v", res)
	}
	if res.HeadSeq != 4 {
		t.Fatalf("head seq = %d, want 4", res.HeadSeq)
	}
	if !bytes.Equal(res.HeadHash, ev[3].ThisHash) {
		t.Fatalf("head hash != last event this_hash")
	}
}

// An empty chain leaves HeadSeq == 0 and HeadHash == nil.
func TestVerifyResultHeadEmptyChain(t *testing.T) {
	res := verifyChain(t, nil)
	if res.HeadSeq != 0 || res.HeadHash != nil {
		t.Fatalf("empty chain head: seq=%d hash=%v", res.HeadSeq, res.HeadHash)
	}
}

// A corrupted chain reports the last successfully verified head (not the broken
// event).
func TestVerifyResultHeadOnCorruptedChain(t *testing.T) {
	ev := validChain("c", 3)
	ev[2].ThisHash = append([]byte(nil), ev[2].ThisHash...)
	ev[2].ThisHash[0] ^= 0xFF // corrupt event 3's this_hash
	res := verifyChain(t, ev)
	if res.OK || res.FirstBreakSeq == nil || *res.FirstBreakSeq != 3 {
		t.Fatalf("expected break at seq 3: %+v", res)
	}
	if res.HeadSeq != 2 {
		t.Fatalf("head seq = %d, want 2 (last verified)", res.HeadSeq)
	}
	if !bytes.Equal(res.HeadHash, ev[1].ThisHash) {
		t.Fatalf("head hash != event 2 this_hash")
	}
}
