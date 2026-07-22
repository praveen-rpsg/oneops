package audit

import (
	"context"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// AppendInput is the fully-resolved, infrastructure-neutral input to one audit
// append. Every field is supplied by the caller: the appender resolves nothing.
// OperationID and EventID are the caller's idempotency identifiers; OccurredAt is
// the caller's server-authoritative time; PayloadCanonical is the already
// canonical hashed byte sequence. It is the canonical append request model.
type AppendInput struct {
	ChainID          string
	OperationID      string
	EventID          string
	Operation        domain.ConfigurationOperation
	Actor            string
	OccurredAt       time.Time
	PayloadCanonical []byte
}

// Appender appends one audit event atomically and returns the persisted event.
// It is the neutral port an orchestrating service depends on; the transactional
// implementation lives in the store/postgres package.
type Appender interface {
	Append(ctx context.Context, in AppendInput) (domain.AuditEvent, error)
}
