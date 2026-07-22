package audit

import (
	"context"
	"errors"

	"github.com/rpsg/oneops/internal/domain"
)

// ChainVerifier is the verification port the service depends on. *Verifier
// satisfies it; declaring it here keeps the service composed purely of neutral
// interfaces (matching the repository's dependency-inversion convention).
type ChainVerifier interface {
	VerifyChain(ctx context.Context, chainID string) (domain.VerifyResult, error)
	VerifyRange(ctx context.Context, chainID string, fromSeq, toSeq int64) (domain.VerifyResult, error)
}

var _ ChainVerifier = (*Verifier)(nil)

// Service is the single entry point for the Audit Integrity capability. It owns
// no audit logic: every method delegates to the appender, verifier, or anchor
// publisher. It depends only on neutral audit interfaces — never on
// infrastructure — so it never hashes, verifies, computes heads/sequences,
// accesses SQL, or inspects the database.
type Service struct {
	appender  Appender
	verifier  ChainVerifier
	publisher AnchorPublisher
}

// NewService composes the audit capability from its three ports. All are
// required; a nil dependency is a configuration error.
func NewService(appender Appender, verifier ChainVerifier, publisher AnchorPublisher) (*Service, error) {
	if appender == nil {
		return nil, errors.New("audit: service requires a non-nil Appender")
	}
	if verifier == nil {
		return nil, errors.New("audit: service requires a non-nil Verifier")
	}
	if publisher == nil {
		return nil, errors.New("audit: service requires a non-nil AnchorPublisher")
	}
	return &Service{appender: appender, verifier: verifier, publisher: publisher}, nil
}

// Append delegates to the appender.
func (s *Service) Append(ctx context.Context, in AppendInput) (domain.AuditEvent, error) {
	return s.appender.Append(ctx, in)
}

// VerifyChain delegates to the verifier.
func (s *Service) VerifyChain(ctx context.Context, chainID string) (domain.VerifyResult, error) {
	return s.verifier.VerifyChain(ctx, chainID)
}

// VerifyRange delegates to the verifier.
func (s *Service) VerifyRange(ctx context.Context, chainID string, fromSeq, toSeq int64) (domain.VerifyResult, error) {
	return s.verifier.VerifyRange(ctx, chainID, fromSeq, toSeq)
}

// PublishAnchor delegates to the anchor publisher.
func (s *Service) PublishAnchor(ctx context.Context, req AnchorRequest) (AnchorResult, error) {
	return s.publisher.PublishAnchor(ctx, req)
}

// VerifyAndPublish verifies a chain and, only if verification succeeds, publishes
// an anchor built solely from the VerifyResult's verified head. On a verification
// error it returns that error and does not publish; on a verification failure
// (!OK) it returns the result and does not publish. Verifier and publisher errors
// propagate unchanged. The AnchorRequest is constructed from VerifyResult alone —
// no SQL, repository calls, or recomputation.
func (s *Service) VerifyAndPublish(ctx context.Context, chainID string) (domain.VerifyResult, AnchorResult, error) {
	res, err := s.verifier.VerifyChain(ctx, chainID)
	if err != nil {
		return res, AnchorResult{}, err
	}
	if !res.OK {
		return res, AnchorResult{}, nil
	}
	anchor, err := s.publisher.PublishAnchor(ctx, AnchorRequest{
		ChainID:  res.ChainID,
		HeadSeq:  res.HeadSeq,
		HeadHash: res.HeadHash,
		Verified: true,
	})
	return res, anchor, err
}
