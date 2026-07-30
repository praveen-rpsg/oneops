package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"

	"github.com/rpsg/oneops/internal/audit"
	"github.com/rpsg/oneops/internal/domain"
)

// withAdminAudit is THE write path for administrative mutations (OPS-S035).
//
// It owns the transaction: fn performs the mutation on the transaction it is
// given, this function appends the administrative audit records on the same
// transaction, and both commit together or neither does. That is
// ADR-AUDIT-007 §6.9 — "an unauditable administrative act must fail" — realised
// as a single commit point rather than as a rule handlers are asked to
// remember. A caller that forgets to audit cannot: there is no path from a
// handler to an administrative INSERT that does not pass through here, because
// the stores' mutating methods have no other transaction to run on.
//
// The acts are computed by the caller BEFORE the mutation runs, because they
// describe what is being done, not what came back. Where a generated identifier
// is only known afterwards, the caller passes a closure result through and the
// subject is completed by the appender from what fn produced — see the stores.
func withAdminAudit(ctx context.Context, pool *pgxpool.Pool,
	acts func() []domain.AdminAct, fn func(pgx.Tx) error) error {

	// Refuse before doing any work. An administrative act with no attributable
	// actor is exactly what §6.9 says must fail, and failing here rather than
	// at the INSERT means the mutation never ran either.
	actor, ok := domain.ActorFrom(ctx)
	if !ok {
		return domain.ErrNoActor
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin administrative transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(tx); err != nil {
		return err
	}

	if err := appendAdminActs(ctx, tx, actor, acts()); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit administrative transaction: %w", err)
	}
	return nil
}

// appendAdminActs seals and writes every act of one administrative operation.
//
// Chain heads are locked in ascending chain-id order, all of them, before any
// append. One act can touch two chains — creating an organisation writes
// `tenant` on the platform chain and `organization` on its own — and two
// transactions taking the same pair in opposite orders deadlock. Reproduced
// live against this schema before the rule existed: the loser aborted after
// 1.6 seconds, and under §6.9 an aborted append means the administrative act
// itself fails. Sorting is what turns a deadlock into a queue.
func appendAdminActs(ctx context.Context, tx pgx.Tx,
	actor string, acts []domain.AdminAct) error {

	if len(acts) == 0 {
		return fmt.Errorf("administrative mutation produced no audit act: %w", domain.ErrNoActor)
	}

	// Phase 1: create every head that does not exist yet, in order. The insert
	// takes a lock too, so it is ordered for the same reason the reads are.
	chains := domain.AdminChainIDs(acts)
	for _, chainID := range chains {
		if err := ensureAdminChainHead(ctx, tx, chainID, audit.GenesisPrevHash()); err != nil {
			return err
		}
	}

	// Phase 2: take every lock, in the same order, before appending anything.
	type head struct {
		seq  int64
		hash []byte
	}
	heads := make(map[string]*head, len(chains))
	for _, chainID := range chains {
		lastSeq, lastHash, err := readAdminChainHeadForUpdate(ctx, tx, chainID)
		if err != nil {
			return err
		}
		heads[chainID] = &head{seq: lastSeq, hash: lastHash}
	}

	// Phase 3: append. Every lock is already held, so no ordering hazard
	// remains and acts may be written in their natural order.
	for _, act := range acts {
		if !act.Operation.Valid() {
			return fmt.Errorf("%q: %w", act.Operation, domain.ErrInvalidAdminOperation)
		}
		chainID := act.Subject.ChainID()
		h := heads[chainID]

		payload, err := sealedAdminPayload(act)
		if err != nil {
			return err
		}

		operationID := ulid.Make().String()
		eventID, err := audit.DeriveEventID(operationID)
		if err != nil {
			return fmt.Errorf("derive admin event id: %w", err)
		}

		// Truncated to microseconds before hashing because timestamptz stores
		// microseconds: hashing a finer value would produce a record that fails
		// its own verification on read-back. Same reason as the constitutional
		// appender.
		occurredAt := time.Now().UTC().Truncate(time.Microsecond)
		seq := h.seq + 1

		thisHash, err := audit.ChainHash(audit.EventHashInput{
			ChainID:             chainID,
			Seq:                 seq,
			EventID:             eventID,
			Operation:           string(act.Operation),
			Actor:               actor,
			OccurredAtUnixNanos: occurredAt.UnixNano(),
			PayloadCanonical:    payload,
		}, h.hash)
		if err != nil {
			return fmt.Errorf("seal admin audit event: %w", err)
		}

		if err := appendAdminAuditEvent(ctx, tx, &adminAuditRow{
			chainID: chainID, seq: seq, eventID: eventID, operationID: operationID,
			operation: act.Operation, actor: actor, subject: act.Subject,
			payloadCanonical: payload, prevHash: h.hash, thisHash: thisHash,
			occurredAt: occurredAt,
		}); err != nil {
			return err
		}

		h.seq, h.hash = seq, thisHash
	}

	// Phase 4: advance every head that moved.
	for chainID, h := range heads {
		if err := upsertAdminChainHead(ctx, tx, chainID, h.seq, h.hash); err != nil {
			return err
		}
	}
	return nil
}

// sealedAdminPayload builds the canonical payload, with the subject identifiers
// inside it.
//
// This is what makes "to whom" evidence. audit.ChainHash covers seven fields,
// and the subject_* columns are not among them — they exist for indexing. If
// the subject lived only in those columns, anyone who reached UPDATE could
// retarget a record to a different organisation and the chain would still
// verify, because this_hash never covered it. Putting the identifiers in the
// payload binds them to the hash; the database's ck_admin_audit_subject_bound
// then refuses any row whose column disagrees with its own sealed payload.
func sealedAdminPayload(act domain.AdminAct) ([]byte, error) {
	doc := make(map[string]any, len(act.Detail)+3)
	for k, v := range act.Detail {
		doc[k] = v
	}
	if act.Subject.OrgID != "" {
		doc["subject_org_id"] = act.Subject.OrgID
	}
	if act.Subject.TenantID != "" {
		doc["subject_tenant_id"] = act.Subject.TenantID
	}
	if act.Subject.UserID != "" {
		doc["subject_user_id"] = act.Subject.UserID
	}
	doc["operation"] = string(act.Operation)

	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("marshal admin audit payload: %w", err)
	}
	canonical, err := audit.Canonicalize(raw)
	if err != nil {
		return nil, fmt.Errorf("canonicalise admin audit payload: %w", err)
	}
	return canonical, nil
}
