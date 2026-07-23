package audit

import (
	"errors"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// ErrMissingOccurredAt is returned when Resolve is given a zero OccurredAt.
var ErrMissingOccurredAt = errors.New("audit: occurred_at must be set")

// Resolve assembles a complete, fully-resolved AppendInput from a neutral
// domain.EventInput plus the two server-authoritative values a request never
// carries: the authenticated Actor and the server-stamped OccurredAt
// (domain.EventInput intentionally omits both, per its contract). It is the one
// seam that turns a governed operation request into an appendable audit input.
//
// Resolve is the single place the two audit-derived fields are produced:
//   - PayloadCanonical, via Canonicalize(in.Payload) — the authoritative
//     canonicalizer;
//   - EventID, via DeriveEventID(in.OperationID) — the authoritative id owner.
//
// Every other field is copied through unchanged from values the caller already
// holds. A governance caller can therefore obtain a complete AppendInput without
// implementing canonicalization or id generation and without inventing any
// value: it supplies ChainID (domain.AuditChainID of the governed object),
// OperationID (the operation's idempotency key), Operation/Actor/OccurredAt
// (from the committed result), and a raw JSON payload it marshalled itself.
//
// Resolve performs no hashing of chain state, sequence allocation, persistence,
// authorization, or anchoring; those remain the appender's and verifier's.
func Resolve(in domain.EventInput, actor string, occurredAt time.Time) (AppendInput, error) {
	if err := in.Validate(); err != nil {
		return AppendInput{}, err
	}
	if actor == "" {
		return AppendInput{}, ErrEmptyActor
	}
	if occurredAt.IsZero() {
		return AppendInput{}, ErrMissingOccurredAt
	}

	canonical, err := Canonicalize(in.Payload)
	if err != nil {
		return AppendInput{}, err
	}
	eventID, err := DeriveEventID(in.OperationID)
	if err != nil {
		return AppendInput{}, err
	}

	return AppendInput{
		ChainID:          in.ChainID,
		OperationID:      in.OperationID,
		EventID:          eventID,
		Operation:        in.Operation,
		Actor:            actor,
		OccurredAt:       occurredAt,
		PayloadCanonical: canonical,
	}, nil
}
