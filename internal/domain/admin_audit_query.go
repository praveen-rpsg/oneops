package domain

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// AdminAuditRecord is one administrative audit row as the query surface returns
// it. The sealed payload is exposed as the canonical bytes it was hashed over,
// so a caller can verify the record rather than trust this API's rendering.
type AdminAuditRecord struct {
	ChainID         string
	Seq             int64
	EventID         string
	Operation       AdminOperation
	Actor           string
	SubjectOrgID    string
	SubjectTenantID string
	SubjectUserID   string
	Payload         []byte
	ThisHash        []byte
	OccurredAt      time.Time
}

// Default and maximum page sizes. A caller that asks for everything gets a
// page: this table is never pruned, so an unbounded query is unbounded for the
// life of the platform.
const (
	DefaultAdminAuditPageSize = 50
	MaxAdminAuditPageSize     = 500
)

// AdminAuditCursor is a position in the total order (occurred_at, chain_id, seq).
//
// All three components are required. occurred_at is not unique — ADR-AUDIT-007
// §6.8's multi-chain shape puts sibling acts of one administrative act on the
// same timestamp — so a cursor over the timestamp alone silently skips rows at a
// page boundary. (chain_id, seq) is the primary key and breaks every tie.
type AdminAuditCursor struct {
	OccurredAt time.Time
	ChainID    string
	Seq        int64
}

// ErrInvalidCursor is returned for a cursor that did not come from this API.
var ErrInvalidCursor = errors.New("cursor is not a valid administrative audit cursor")

// Encode renders the cursor as an opaque token. It is opaque by intent: a
// caller that composes its own cursor is depending on the ordering key, which
// is an implementation detail the platform must stay free to change.
func (c AdminAuditCursor) Encode() string {
	raw := fmt.Sprintf("%d\x1f%s\x1f%d", c.OccurredAt.UTC().UnixMicro(), c.ChainID, c.Seq)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeAdminAuditCursor parses a token produced by Encode.
func DecodeAdminAuditCursor(s string) (AdminAuditCursor, error) {
	if s == "" {
		return AdminAuditCursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return AdminAuditCursor{}, ErrInvalidCursor
	}
	parts := strings.Split(string(raw), "\x1f")
	if len(parts) != 3 {
		return AdminAuditCursor{}, ErrInvalidCursor
	}
	micros, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return AdminAuditCursor{}, ErrInvalidCursor
	}
	seq, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || parts[1] == "" || seq < 1 {
		return AdminAuditCursor{}, ErrInvalidCursor
	}
	return AdminAuditCursor{
		OccurredAt: time.UnixMicro(micros).UTC(),
		ChainID:    parts[1],
		Seq:        seq,
	}, nil
}

// AdminAuditFilter is §6.1's question — who did what, to whom, when — as the
// query surface accepts it. Every field is optional; an empty filter is the
// newest page of everything.
type AdminAuditFilter struct {
	Actor           string
	Operation       AdminOperation
	SubjectOrgID    string
	SubjectTenantID string
	SubjectUserID   string
	From            time.Time
	To              time.Time
	Limit           int
	After           AdminAuditCursor
}

// Validate normalises the filter and refuses one the store should not be asked
// to run. Bounding the page here rather than in SQL means every caller of the
// port gets the bound, not only the HTTP one.
func (f *AdminAuditFilter) Validate() error {
	if f.Limit <= 0 {
		f.Limit = DefaultAdminAuditPageSize
	}
	if f.Limit > MaxAdminAuditPageSize {
		f.Limit = MaxAdminAuditPageSize
	}
	if f.Operation != "" && !f.Operation.Valid() {
		return NewValidationError("operation", "is not an administrative audit operation")
	}
	if !f.From.IsZero() && !f.To.IsZero() && f.To.Before(f.From) {
		return NewValidationError("to", "must not be before from")
	}
	return nil
}

// AdminAuditReader is THE constitutional read boundary for administrative audit
// (ADR-AUDIT-007 §6.5). There is exactly one, and every consumer goes through
// it; a second reader would be a second answer to who may see this data.
type AdminAuditReader interface {
	QueryAdminAudit(ctx context.Context, f AdminAuditFilter) ([]*AdminAuditRecord, *AdminAuditCursor, error)
}
