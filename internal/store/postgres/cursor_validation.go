package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// CursorValidator proves that no relay cursor has advanced past the log it
// reads (ADR-TENANCY-011).
//
// The cursors are watermarks over the committed audit log: the relay and the
// policy consumer deliver everything after `last_seq` for a chain. A cursor
// ahead of its chain head means the events in between will never be read —
// silently, because a watermark that is too high looks exactly like a watermark
// that is up to date.
//
// The platform cannot produce that state on its own: the cursor is monotonic
// and only advances past events already enqueued (ADR-CONCURRENCY-004). It is
// reachable through *recovery* — a restore that takes the cursor tables from a
// newer snapshot than `audit_event`, or a partial restore of the log. That is
// exactly the case ADR-TENANCY-006 exists for: recovery is a verification
// boundary, and an inconsistent restore must be refused rather than trusted.
//
// ADR-CONCURRENCY-004's dossier recorded this check as "noted future hardening"
// and it was never built, so the class it belongs to had a known open instance.
type CursorValidator struct{ pool *pgxpool.Pool }

// NewCursorValidator builds the validator over the given pool.
func NewCursorValidator(pool *pgxpool.Pool) *CursorValidator { return &CursorValidator{pool: pool} }

// Validate reports every cursor that has advanced past its chain head.
func (v *CursorValidator) Validate(ctx context.Context) ([]string, error) {
	var problems []string
	for _, c := range []struct{ table, what string }{
		{"webhook_cursor", "event relay"},
		{"policy_cursor", "policy consumer"},
	} {
		var n int
		var example string
		var cursorSeq, headSeq int64
		// A cursor with no chain head at all is equally broken: it claims
		// progress through a log that does not exist.
		q := fmt.Sprintf(`
			SELECT count(*), COALESCE(min(c.chain_id), ''),
			       COALESCE(min(c.last_seq), 0), COALESCE(min(h.last_seq), 0)
			  FROM %s c
			  LEFT JOIN audit_chain_head h ON h.chain_id = c.chain_id
			 WHERE h.chain_id IS NULL OR c.last_seq > h.last_seq`, c.table)
		if err := v.pool.QueryRow(ctx, q).Scan(&n, &example, &cursorSeq, &headSeq); err != nil {
			return nil, fmt.Errorf("cursor check on %s: %w", c.table, err)
		}
		if n > 0 {
			problems = append(problems, fmt.Sprintf(
				"%s has %d cursor(s) ahead of the audit log (e.g. chain %s at seq %d, log head %d) "+
					"— the %s would silently skip every event in between",
				c.table, n, example, cursorSeq, headSeq, c.what))
		}
	}
	return problems, nil
}
