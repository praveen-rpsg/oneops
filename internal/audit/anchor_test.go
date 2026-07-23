package audit

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func head32(seed byte) []byte {
	b := make([]byte, HashSize)
	b[0] = seed
	return b
}

func validRequest() AnchorRequest {
	return AnchorRequest{ChainID: "c1", HeadSeq: 3, HeadHash: head32(0xAA), Verified: true}
}

func TestPublishAnchorSuccess(t *testing.T) {
	p := NewMemoryAnchorPublisher()
	res, err := p.PublishAnchor(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if res.AnchorID == "" || res.AlreadyPublished || res.ChainID != "c1" || res.HeadSeq != 3 {
		t.Fatalf("result: %+v", res)
	}
	if !bytes.Equal(res.HeadHash, head32(0xAA)) || len(res.Payload) == 0 {
		t.Fatalf("head/payload: %+v", res)
	}
	if p.Count() != 1 {
		t.Fatalf("count = %d, want 1", p.Count())
	}
}

func TestPublishAnchorIdempotent(t *testing.T) {
	p := NewMemoryAnchorPublisher()
	ctx := context.Background()
	first, _ := p.PublishAnchor(ctx, validRequest())
	second, err := p.PublishAnchor(ctx, validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if second.AnchorID != first.AnchorID {
		t.Fatalf("anchor id changed: %s != %s", second.AnchorID, first.AnchorID)
	}
	if first.AlreadyPublished || !second.AlreadyPublished {
		t.Fatalf("idempotency flags: first=%v second=%v", first.AlreadyPublished, second.AlreadyPublished)
	}
	if p.Count() != 1 {
		t.Fatalf("republish created a new record: count=%d", p.Count())
	}
}

func TestPublishAnchorDistinctHeadsDistinctAnchors(t *testing.T) {
	p := NewMemoryAnchorPublisher()
	ctx := context.Background()
	a, _ := p.PublishAnchor(ctx, validRequest())
	req2 := validRequest()
	req2.HeadSeq = 4
	b, _ := p.PublishAnchor(ctx, req2)
	if a.AnchorID == b.AnchorID {
		t.Fatal("different heads produced the same anchor id")
	}
	if p.Count() != 2 {
		t.Fatalf("count = %d, want 2", p.Count())
	}
}

func TestPublishAnchorInvalidRequests(t *testing.T) {
	p := NewMemoryAnchorPublisher()
	ctx := context.Background()
	cases := []struct {
		name string
		req  AnchorRequest
		want error
	}{
		{"empty chain", func() AnchorRequest { r := validRequest(); r.ChainID = ""; return r }(), ErrEmptyChainID},
		{"zero seq", func() AnchorRequest { r := validRequest(); r.HeadSeq = 0; return r }(), ErrInvalidHeadSeq},
		{"short hash", func() AnchorRequest { r := validRequest(); r.HeadHash = make([]byte, 31); return r }(), ErrHeadHashLen},
		{"nil hash", func() AnchorRequest { r := validRequest(); r.HeadHash = nil; return r }(), ErrHeadHashLen},
		{"not verified", func() AnchorRequest { r := validRequest(); r.Verified = false; return r }(), ErrChainNotVerified},
	}
	for _, c := range cases {
		if _, err := p.PublishAnchor(ctx, c.req); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
	if p.Count() != 0 {
		t.Fatalf("invalid requests published anchors: count=%d", p.Count())
	}
}

func TestAnchorDeterministicPayload(t *testing.T) {
	a, err := NewAnchor(validRequest())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewAnchor(validRequest())
	if a.AnchorID != b.AnchorID || !bytes.Equal(a.Payload, b.Payload) {
		t.Fatal("NewAnchor is not deterministic")
	}
	// Golden vector freezes the payload/id construction as a regression guard.
	const wantID = "7af4a6a7f7f84fa8d917d5abb912768673671b0287ab8fdfcf17d34a1720ea9b"
	if a.AnchorID != wantID {
		t.Fatalf("golden anchor id changed:\n got=%s\nwant=%s", a.AnchorID, wantID)
	}
}

// stubPublisher is a test double proving arbitrary publisher failures propagate
// through the AnchorPublisher interface unchanged.
type stubPublisher struct{ err error }

func (s stubPublisher) PublishAnchor(_ context.Context, _ AnchorRequest) (AnchorResult, error) {
	return AnchorResult{}, s.err
}

func TestPublisherFailurePropagation(t *testing.T) {
	var p AnchorPublisher = stubPublisher{err: errors.New("backend down")}
	if _, err := p.PublishAnchor(context.Background(), validRequest()); err == nil || err.Error() != "backend down" {
		t.Fatalf("failure not propagated: %v", err)
	}
	// Memory publisher propagates validation failures too.
	mp := NewMemoryAnchorPublisher()
	if _, err := mp.PublishAnchor(context.Background(), AnchorRequest{}); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestPublishAnchorContextCancellation(t *testing.T) {
	p := NewMemoryAnchorPublisher()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.PublishAnchor(ctx, validRequest()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation not honored: %v", err)
	}
	if p.Count() != 0 {
		t.Fatalf("published despite cancellation: count=%d", p.Count())
	}
}

func TestAnchorPublisherContractCompliance(t *testing.T) {
	var _ AnchorPublisher = (*MemoryAnchorPublisher)(nil)
	var _ AnchorPublisher = stubPublisher{}
	// drive through the interface
	var p AnchorPublisher = NewMemoryAnchorPublisher()
	if _, err := p.PublishAnchor(context.Background(), validRequest()); err != nil {
		t.Fatalf("interface drive: %v", err)
	}
}
