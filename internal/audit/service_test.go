package audit

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

// --- test doubles (all satisfy the neutral ports) ---------------------------

type mockAppender struct {
	gotIn    AppendInput
	retEvent domain.AuditEvent
	retErr   error
}

func (m *mockAppender) Append(_ context.Context, in AppendInput) (domain.AuditEvent, error) {
	m.gotIn = in
	return m.retEvent, m.retErr
}

type mockVerifier struct {
	chainRes               domain.VerifyResult
	chainErr               error
	rangeRes               domain.VerifyResult
	rangeErr               error
	gotChainID             string
	gotFrom, gotTo         int64
	chainCalls, rangeCalls int
}

func (m *mockVerifier) VerifyChain(_ context.Context, chainID string) (domain.VerifyResult, error) {
	m.chainCalls++
	m.gotChainID = chainID
	return m.chainRes, m.chainErr
}

func (m *mockVerifier) VerifyRange(_ context.Context, chainID string, from, to int64) (domain.VerifyResult, error) {
	m.rangeCalls++
	m.gotChainID, m.gotFrom, m.gotTo = chainID, from, to
	return m.rangeRes, m.rangeErr
}

type spyPublisher struct {
	calls  int
	gotReq AnchorRequest
	retRes AnchorResult
	retErr error
}

func (s *spyPublisher) PublishAnchor(_ context.Context, req AnchorRequest) (AnchorResult, error) {
	s.calls++
	s.gotReq = req
	return s.retRes, s.retErr
}

func newTestService(t *testing.T, a Appender, v ChainVerifier, p AnchorPublisher) *Service {
	t.Helper()
	svc, err := NewService(a, v, p)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

// --- constructor -------------------------------------------------------------

func TestNewServiceNilDependencies(t *testing.T) {
	a, v, p := &mockAppender{}, &mockVerifier{}, &spyPublisher{}
	if _, err := NewService(nil, v, p); err == nil {
		t.Error("expected error for nil appender")
	}
	if _, err := NewService(a, nil, p); err == nil {
		t.Error("expected error for nil verifier")
	}
	if _, err := NewService(a, v, nil); err == nil {
		t.Error("expected error for nil publisher")
	}
	if _, err := NewService(a, v, p); err != nil {
		t.Errorf("valid construction failed: %v", err)
	}
}

// --- delegation --------------------------------------------------------------

func TestAppendDelegation(t *testing.T) {
	in := AppendInput{ChainID: "c", OperationID: "op", EventID: "e", Operation: domain.OpApproval, Actor: "a", PayloadCanonical: []byte(`{}`)}
	a := &mockAppender{retEvent: domain.AuditEvent{Seq: 7}}
	svc := newTestService(t, a, &mockVerifier{}, &spyPublisher{})
	got, err := svc.Append(context.Background(), in)
	if err != nil || got.Seq != 7 {
		t.Fatalf("append: %+v err=%v", got, err)
	}
	if !reflect.DeepEqual(a.gotIn, in) {
		t.Fatalf("input not delegated exactly: %+v", a.gotIn)
	}
	// error propagation
	a2 := &mockAppender{retErr: errors.New("boom")}
	svc2 := newTestService(t, a2, &mockVerifier{}, &spyPublisher{})
	if _, err := svc2.Append(context.Background(), in); err == nil || err.Error() != "boom" {
		t.Fatalf("appender error not propagated: %v", err)
	}
}

func TestVerifyChainDelegation(t *testing.T) {
	v := &mockVerifier{chainRes: domain.VerifyResult{ChainID: "c", OK: true, Checked: 3}}
	svc := newTestService(t, &mockAppender{}, v, &spyPublisher{})
	res, err := svc.VerifyChain(context.Background(), "c")
	if err != nil || res.Checked != 3 || v.gotChainID != "c" || v.chainCalls != 1 {
		t.Fatalf("verifychain: res=%+v err=%v calls=%d", res, err, v.chainCalls)
	}
}

func TestVerifyRangeDelegation(t *testing.T) {
	v := &mockVerifier{rangeRes: domain.VerifyResult{ChainID: "c", OK: true, Checked: 2}}
	svc := newTestService(t, &mockAppender{}, v, &spyPublisher{})
	res, err := svc.VerifyRange(context.Background(), "c", 2, 5)
	if err != nil || res.Checked != 2 || v.gotFrom != 2 || v.gotTo != 5 || v.rangeCalls != 1 {
		t.Fatalf("verifyrange: res=%+v err=%v from=%d to=%d", res, err, v.gotFrom, v.gotTo)
	}
}

func TestPublishAnchorDelegation(t *testing.T) {
	req := AnchorRequest{ChainID: "c", HeadSeq: 3, HeadHash: make([]byte, 32), Verified: true}
	p := &spyPublisher{retRes: AnchorResult{AnchorID: "id"}}
	svc := newTestService(t, &mockAppender{}, &mockVerifier{}, p)
	res, err := svc.PublishAnchor(context.Background(), req)
	if err != nil || res.AnchorID != "id" || !reflect.DeepEqual(p.gotReq, req) {
		t.Fatalf("publish: res=%+v err=%v got=%+v", res, err, p.gotReq)
	}
}

// --- VerifyAndPublish --------------------------------------------------------

func TestVerifyAndPublishHappyPath(t *testing.T) {
	head := bytes.Repeat([]byte{0xAB}, 32)
	v := &mockVerifier{chainRes: domain.VerifyResult{ChainID: "c", OK: true, Checked: 5, HeadSeq: 5, HeadHash: head}}
	p := &spyPublisher{retRes: AnchorResult{AnchorID: "anchor-1"}}
	svc := newTestService(t, &mockAppender{}, v, p)

	res, anchor, err := svc.VerifyAndPublish(context.Background(), "c")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !res.OK || anchor.AnchorID != "anchor-1" || p.calls != 1 {
		t.Fatalf("res=%+v anchor=%+v calls=%d", res, anchor, p.calls)
	}
	// AnchorRequest constructed exactly from VerifyResult, head copied unmodified.
	want := AnchorRequest{ChainID: "c", HeadSeq: 5, HeadHash: head, Verified: true}
	if p.gotReq.ChainID != want.ChainID || p.gotReq.HeadSeq != want.HeadSeq ||
		!p.gotReq.Verified || !bytes.Equal(p.gotReq.HeadHash, head) {
		t.Fatalf("anchor request mismatch: got=%+v want=%+v", p.gotReq, want)
	}
}

func TestVerifyAndPublishVerificationFailurePreventsPublication(t *testing.T) {
	seq := int64(2)
	v := &mockVerifier{chainRes: domain.VerifyResult{ChainID: "c", OK: false, Checked: 1, FirstBreakSeq: &seq, BreakReason: ReasonThisHashMismatch}}
	p := &spyPublisher{}
	svc := newTestService(t, &mockAppender{}, v, p)

	res, anchor, err := svc.VerifyAndPublish(context.Background(), "c")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.OK {
		t.Fatal("expected verification failure")
	}
	if anchor.AnchorID != "" || anchor.Payload != nil {
		t.Fatalf("anchor should be empty: %+v", anchor)
	}
	if p.calls != 0 {
		t.Fatalf("publisher must not be called on verification failure: calls=%d", p.calls)
	}
}

func TestVerifyAndPublishVerifierErrorPropagates(t *testing.T) {
	v := &mockVerifier{chainErr: errors.New("read failed")}
	p := &spyPublisher{}
	svc := newTestService(t, &mockAppender{}, v, p)
	if _, _, err := svc.VerifyAndPublish(context.Background(), "c"); err == nil || err.Error() != "read failed" {
		t.Fatalf("verifier error not propagated: %v", err)
	}
	if p.calls != 0 {
		t.Fatal("publisher called despite verifier error")
	}
}

func TestVerifyAndPublishPublisherErrorPropagates(t *testing.T) {
	v := &mockVerifier{chainRes: domain.VerifyResult{ChainID: "c", OK: true, HeadSeq: 1, HeadHash: make([]byte, 32)}}
	p := &spyPublisher{retErr: errors.New("worm down")}
	svc := newTestService(t, &mockAppender{}, v, p)
	res, _, err := svc.VerifyAndPublish(context.Background(), "c")
	if !res.OK {
		t.Fatal("verification should have succeeded")
	}
	if err == nil || err.Error() != "worm down" {
		t.Fatalf("publisher error not propagated: %v", err)
	}
}

// --- context propagation + interface-only composition ------------------------

func TestServicePropagatesContextCancellation(t *testing.T) {
	// The real MemoryAnchorPublisher honors ctx; the service must pass it through.
	svc := newTestService(t, &mockAppender{}, &mockVerifier{}, NewMemoryAnchorPublisher())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := AnchorRequest{ChainID: "c", HeadSeq: 1, HeadHash: make([]byte, 32), Verified: true}
	if _, err := svc.PublishAnchor(ctx, req); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation not propagated: %v", err)
	}
}

func TestServiceInterfaceOnlyComposition(t *testing.T) {
	// All three dependencies are interfaces; concrete real types satisfy them.
	var _ Appender = (*mockAppender)(nil)
	var _ ChainVerifier = (*Verifier)(nil)
	var _ AnchorPublisher = (*MemoryAnchorPublisher)(nil)
	// The service composes over interface values only.
	var a Appender = &mockAppender{}
	var v ChainVerifier = &mockVerifier{}
	var p AnchorPublisher = NewMemoryAnchorPublisher()
	if _, err := NewService(a, v, p); err != nil {
		t.Fatalf("interface composition: %v", err)
	}
}
