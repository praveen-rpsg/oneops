package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
)

// anchorDomainTag namespaces the deterministic anchor payload so anchors cannot
// be confused with any other hashed artifact. It is versioned for evolvability.
const anchorDomainTag = "oneops.audit.anchor.v1"

// Anchor validation errors (structured sentinels; ErrEmptyChainID is reused).
var (
	// ErrInvalidHeadSeq is returned when HeadSeq is less than 1.
	ErrInvalidHeadSeq = errors.New("audit: anchor head_seq must be >= 1")
	// ErrHeadHashLen is returned when HeadHash is not exactly HashSize bytes.
	ErrHeadHashLen = errors.New("audit: anchor head_hash must be 32 bytes")
	// ErrChainNotVerified is returned when a request to anchor an unverified chain
	// is made. The publisher never verifies; it refuses unverified input.
	ErrChainNotVerified = errors.New("audit: cannot anchor an unverified chain")
)

// AnchorRequest is the input to publish one anchor: the already-verified head of
// an audit chain. The publisher consumes this; it never verifies the chain,
// recomputes hashes, or reads audit history.
type AnchorRequest struct {
	// ChainID identifies the audit chain being anchored.
	ChainID string
	// HeadSeq is the sequence of the chain head at verification time.
	HeadSeq int64
	// HeadHash is the chain head's this_hash (32 bytes).
	HeadHash []byte
	// Verified is the caller's verification status; it must be true.
	Verified bool
}

// AnchorResult is the immutable anchor record derived deterministically from a
// request: a content-addressed AnchorID over a canonical Payload.
type AnchorResult struct {
	// AnchorID is hex(sha256(Payload)) — content-addressed and idempotent.
	AnchorID string
	// ChainID, HeadSeq, HeadHash echo the anchored head.
	ChainID  string
	HeadSeq  int64
	HeadHash []byte
	// Payload is the deterministic, self-describing anchor bytes to be persisted
	// to an external immutable store by an implementation.
	Payload []byte
	// AlreadyPublished is true when this exact anchor was published before.
	AlreadyPublished bool
}

// AnchorPublisher publishes immutable anchor records for already-verified audit
// chains to an external trust root. Publication is idempotent: the same head
// yields the same AnchorID, and re-publishing reports AlreadyPublished. It is a
// pure outbound abstraction — no verification, no audit-table access, no vendor
// coupling; implementations (e.g., WORM object storage) satisfy this contract.
type AnchorPublisher interface {
	PublishAnchor(ctx context.Context, req AnchorRequest) (AnchorResult, error)
}

// NewAnchor builds the deterministic anchor (Payload + AnchorID) for a request,
// or a validation error. It performs no publication or I/O and is safe to call
// for payload inspection.
func NewAnchor(req AnchorRequest) (AnchorResult, error) {
	if err := validateAnchorRequest(req); err != nil {
		return AnchorResult{}, err
	}
	payload := anchorPayload(req.ChainID, req.HeadSeq, req.HeadHash)
	sum := sha256.Sum256(payload)
	return AnchorResult{
		AnchorID:         hex.EncodeToString(sum[:]),
		ChainID:          req.ChainID,
		HeadSeq:          req.HeadSeq,
		HeadHash:         append([]byte(nil), req.HeadHash...),
		Payload:          payload,
		AlreadyPublished: false,
	}, nil
}

func validateAnchorRequest(req AnchorRequest) error {
	if req.ChainID == "" {
		return ErrEmptyChainID
	}
	if req.HeadSeq < 1 {
		return ErrInvalidHeadSeq
	}
	if len(req.HeadHash) != HashSize {
		return ErrHeadHashLen
	}
	if !req.Verified {
		return ErrChainNotVerified
	}
	return nil
}

// anchorPayload is the deterministic, injection-safe framing of the anchored
// head. It reuses the package's length-prefixed framing helpers so distinct
// inputs cannot collide by field-boundary shifting.
func anchorPayload(chainID string, headSeq int64, headHash []byte) []byte {
	var buf bytes.Buffer
	writeLenPrefixed(&buf, []byte(anchorDomainTag))
	writeLenPrefixed(&buf, []byte(chainID))
	writeUint64(&buf, uint64(headSeq))
	writeLenPrefixed(&buf, headHash)
	return buf.Bytes()
}

// MemoryAnchorPublisher is the non-vendor reference implementation: it records
// published anchors in memory (content-addressed by AnchorID) to provide the
// idempotent contract for tests and default wiring. It writes no database and
// touches no audit table.
type MemoryAnchorPublisher struct {
	mu        sync.Mutex
	published map[string]AnchorResult
}

// NewMemoryAnchorPublisher builds an empty in-memory publisher.
func NewMemoryAnchorPublisher() *MemoryAnchorPublisher {
	return &MemoryAnchorPublisher{published: make(map[string]AnchorResult)}
}

var _ AnchorPublisher = (*MemoryAnchorPublisher)(nil)

// PublishAnchor deterministically builds and records the anchor. Re-publishing
// the same head is idempotent (AlreadyPublished=true). It honors context
// cancellation and never mutates audit history.
func (p *MemoryAnchorPublisher) PublishAnchor(ctx context.Context, req AnchorRequest) (AnchorResult, error) {
	if err := ctx.Err(); err != nil {
		return AnchorResult{}, err
	}
	res, err := NewAnchor(req)
	if err != nil {
		return AnchorResult{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if prev, ok := p.published[res.AnchorID]; ok {
		prev.AlreadyPublished = true
		return prev, nil
	}
	p.published[res.AnchorID] = res
	return res, nil
}

// Count returns the number of distinct anchors recorded (for tests/diagnostics).
func (p *MemoryAnchorPublisher) Count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.published)
}
