package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/audit"
	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/governance"
)

// chainStore is the persistence port the appender orchestrates. It is satisfied
// by *AuditStore; declaring it here follows the repository's dependency-inversion
// convention (as authority/graph depend on domain ports) and makes the append
// transaction's failure paths testable without bypassing the real store.
type chainStore interface {
	EnsureChainHead(ctx context.Context, tx pgx.Tx, chainID string, genesisHash []byte) error
	// ReadChainHeadForUpdate is the locking read. The appender must never use a
	// non-locking one: without FOR UPDATE, concurrent appends to a chain collide
	// and most of them are lost (ADR-AUDIT-006).
	ReadChainHeadForUpdate(ctx context.Context, tx pgx.Tx, chainID string) (lastSeq int64, lastHash []byte, found bool, err error)
	AppendAuditEvent(ctx context.Context, tx pgx.Tx, e *domain.AuditEvent) error
	UpsertChainHead(ctx context.Context, tx pgx.Tx, chainID string, lastSeq int64, lastHash []byte) error
}

// AuditAppender orchestrates exactly one atomic append of an audit event: within
// a single transaction it ensures the chain head, locks and reads it, computes
// the chain hash (PRS-003), persists the event, and advances the head. It only
// orchestrates — it never canonicalizes payloads, generates ids, resolves actors
// or timestamps, publishes anchors, verifies chains, or retries.
type AuditAppender struct {
	pool  *pgxpool.Pool
	store chainStore
}

// NewAuditAppender wires the appender over a pool (to own the transaction) and a
// store (to persist within it). In production the store is *AuditStore over pool.
func NewAuditAppender(pool *pgxpool.Pool, store chainStore) *AuditAppender {
	return &AuditAppender{pool: pool, store: store}
}

var (
	_ audit.Appender     = (*AuditAppender)(nil)
	_ governance.Auditor = (*AuditAppender)(nil)
)

// Append performs the standalone one-transaction append flow: it OWNS the
// transaction (Begin → appendTx → Commit) and is retained for non-governed
// callers (ADR-AUDIT-005 keeps this pool-owning path unchanged). Any failure
// rolls the whole transaction back, so there are never partial writes. It
// returns the persisted event, or a repository/validation error unchanged.
func (a *AuditAppender) Append(ctx context.Context, in audit.AppendInput) (domain.AuditEvent, error) {
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return domain.AuditEvent{}, fmt.Errorf("begin append tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful Commit

	event, err := a.AppendTx(ctx, tx, in)
	if err != nil {
		return domain.AuditEvent{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.AuditEvent{}, fmt.Errorf("commit append tx: %w", err)
	}
	return event, nil
}

// AppendTx performs the append flow within a CALLER-OWNED transaction and does
// NOT commit: EnsureChainHead → ReadChainHead(FOR UPDATE) → ChainHash →
// AppendAuditEvent → UpsertChainHead (ADR-AUDIT-004 steps, unchanged). It is the
// transaction-scoped entry point ADR-AUDIT-005 introduces so the governance
// engine can append the audit event atomically inside its own transaction. The
// hashing, ordering, canonicalization, and EventID are byte-for-byte identical
// to the standalone path — only transaction ownership moves to the caller. Any
// error leaves the caller's transaction to roll back, so there are never partial
// writes. It returns the persisted event.
func (a *AuditAppender) AppendTx(ctx context.Context, tx pgx.Tx, in audit.AppendInput) (domain.AuditEvent, error) {
	// 1. Ensure the genesis head exists so the locking read has a row to lock.
	if err := a.store.EnsureChainHead(ctx, tx, in.ChainID, audit.GenesisPrevHash()); err != nil {
		return domain.AuditEvent{}, err
	}
	// 2. Lock the head and read the current tip.
	lastSeq, lastHash, found, err := a.store.ReadChainHeadForUpdate(ctx, tx, in.ChainID)
	if err != nil {
		return domain.AuditEvent{}, err
	}
	if !found {
		return domain.AuditEvent{}, fmt.Errorf("append: chain head missing after ensure for %q", in.ChainID)
	}
	seq := lastSeq + 1
	prevHash := lastHash

	// Normalize occurred_at to PostgreSQL timestamptz precision (microseconds)
	// BEFORE hashing, so the hashed value is exactly what is persisted and later
	// re-read during verification. Postgres rounds sub-microsecond input, so an
	// un-normalized nanosecond timestamp would hash differently than it stores.
	occurredAt := in.OccurredAt.UTC().Truncate(time.Microsecond)

	// 3-4. Compute this_hash from the field tuple and the previous hash.
	thisHash, err := audit.ChainHash(audit.EventHashInput{
		ChainID:             in.ChainID,
		Seq:                 seq,
		EventID:             in.EventID,
		Operation:           string(in.Operation),
		Actor:               in.Actor,
		OccurredAtUnixNanos: occurredAt.UnixNano(),
		PayloadCanonical:    in.PayloadCanonical,
	}, prevHash)
	if err != nil {
		return domain.AuditEvent{}, err
	}

	event := domain.AuditEvent{
		ChainID:          in.ChainID,
		Seq:              seq,
		EventID:          in.EventID,
		OperationID:      in.OperationID,
		Operation:        in.Operation,
		Actor:            in.Actor,
		PayloadCanonical: in.PayloadCanonical,
		PrevHash:         prevHash,
		ThisHash:         thisHash,
		OccurredAt:       occurredAt,
	}
	// 5. Persist the event (atomic with the head advance below).
	if err := a.store.AppendAuditEvent(ctx, tx, &event); err != nil {
		return domain.AuditEvent{}, err
	}
	// 6. Advance the head to this event.
	if err := a.store.UpsertChainHead(ctx, tx, in.ChainID, seq, thisHash); err != nil {
		return domain.AuditEvent{}, err
	}
	return event, nil
}
