package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
)

// AuditStore persists the tamper-evident audit chain (audit_event) and its
// per-chain head (audit_chain_head). It is persistence only: it never computes
// hashes, canonicalizes payloads, generates ids, allocates sequence numbers,
// resolves actors or timestamps, authorizes, or validates business rules — those
// belong to the appender and verifier. Methods that must be composed atomically
// accept a pgx.Tx so the caller owns the transaction (ADR-AUDIT-003/004).
type AuditStore struct {
	pool *pgxpool.Pool
}

// NewAuditStore builds a store over the given pool.
func NewAuditStore(pool *pgxpool.Pool) *AuditStore {
	return &AuditStore{pool: pool}
}

// auditEventCols is the projection for reads. The derived payload jsonb column is
// not read back: payload_canonical is the hashed source of truth.
const auditEventCols = `chain_id, seq, event_id, operation_id, operation, actor,
	payload_canonical, prev_hash, this_hash, occurred_at, tenant_id`

// AppendAuditEvent inserts one fully-formed audit event within the caller's
// transaction. The derived payload jsonb column is populated from the already
// canonical payload_canonical bytes (no canonicalization happens here). A
// duplicate (chain_id, seq) or (chain_id, event_id) maps to domain.ErrConflict.
func (s *AuditStore) AppendAuditEvent(ctx context.Context, tx pgx.Tx, e *domain.AuditEvent) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO audit_event
			(chain_id, seq, event_id, operation_id, operation, actor,
			 payload_canonical, payload, prev_hash, this_hash, occurred_at, tenant_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,convert_from($7,'UTF8')::jsonb,$8,$9,$10,$11)`,
		e.ChainID, e.Seq, e.EventID, e.OperationID, string(e.Operation), e.Actor,
		e.PayloadCanonical, e.PrevHash, e.ThisHash, e.OccurredAt,
		domain.TenantIDFrom(ctx))
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrConflict
		}
		return fmt.Errorf("append audit event: %w", err)
	}
	return nil
}

// EnsureChainHead creates the genesis head row for chainID if it does not exist
// (last_seq 0, the caller-supplied genesis hash), and is a no-op otherwise. It
// runs in the caller's transaction so a locking ReadChainHead can follow without
// a create race (ADR-AUDIT-004). genesisHash must be 32 bytes.
func (s *AuditStore) EnsureChainHead(ctx context.Context, tx pgx.Tx, chainID string, genesisHash []byte) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO audit_chain_head (chain_id, last_seq, last_hash, tenant_id)
		VALUES ($1, 0, $2, $3)
		ON CONFLICT (chain_id) DO NOTHING`, chainID, genesisHash,
		domain.TenantIDFrom(ctx))
	if err != nil {
		return fmt.Errorf("ensure chain head: %w", err)
	}
	return nil
}

// ReadChainHead returns the current last_seq and last_hash for chainID. When
// forUpdate is true the row is locked FOR UPDATE (use inside the append
// transaction). found is false when no head row exists.
func (s *AuditStore) ReadChainHead(ctx context.Context, tx pgx.Tx, chainID string, forUpdate bool) (lastSeq int64, lastHash []byte, found bool, err error) {
	q := `SELECT last_seq, last_hash FROM audit_chain_head WHERE chain_id = $1`
	if forUpdate {
		q += ` FOR UPDATE`
	}
	err = tx.QueryRow(ctx, q, chainID).Scan(&lastSeq, &lastHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil, false, nil
	}
	if err != nil {
		return 0, nil, false, fmt.Errorf("read chain head: %w", err)
	}
	return lastSeq, lastHash, true, nil
}

// UpsertChainHead advances the head for chainID to (lastSeq, lastHash), creating
// the row if absent. It runs in the caller's transaction, atomically with the
// AppendAuditEvent that produced the new hash.
func (s *AuditStore) UpsertChainHead(ctx context.Context, tx pgx.Tx, chainID string, lastSeq int64, lastHash []byte) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO audit_chain_head (chain_id, last_seq, last_hash, updated_at, tenant_id)
		VALUES ($1, $2, $3, now(), $4)
		ON CONFLICT (chain_id) DO UPDATE
		   SET last_seq = EXCLUDED.last_seq,
		       last_hash = EXCLUDED.last_hash,
		       updated_at = now()`, chainID, lastSeq, lastHash,
		domain.TenantIDFrom(ctx))
	if err != nil {
		return fmt.Errorf("upsert chain head: %w", err)
	}
	return nil
}

// ListChainIDs returns every audit chain id (the chain-head partition keys) in
// ascending order. It is a read-only operational query for integrity monitoring;
// it computes nothing and mutates nothing.
func (s *AuditStore) ListChainIDs(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT chain_id FROM audit_chain_head ORDER BY chain_id`)
	if err != nil {
		return nil, fmt.Errorf("list chain ids: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan chain id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list chain ids: %w", err)
	}
	return ids, nil
}

// HeadOf returns the chain head (last_seq, last_hash) for chainID without a
// transaction. found is false for a chain that has no events yet. Read-only
// operational query for the governance read APIs; it computes/mutates nothing.
func (s *AuditStore) HeadOf(ctx context.Context, chainID string) (seq int64, hash []byte, found bool, err error) {
	err = s.pool.QueryRow(ctx,
		`SELECT last_seq, last_hash FROM audit_chain_head WHERE chain_id = $1`, chainID).Scan(&seq, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil, false, nil
	}
	if err != nil {
		return 0, nil, false, fmt.Errorf("read chain head: %w", err)
	}
	return seq, hash, true, nil
}

// ListEvents returns a page of a chain's events for the read APIs. It is a pure
// read: ordering is by seq (ascending unless desc), the page starts strictly
// after/before cursor, an empty operation matches all, and limit bounds the page.
// It performs no verification, hashing, or mutation.
func (s *AuditStore) ListEvents(ctx context.Context, chainID string, cursor int64, desc bool, limit int, operation string) ([]domain.AuditEvent, error) {
	cmp, order := "seq > $2", "ORDER BY seq ASC"
	if desc {
		cmp, order = "seq < $2", "ORDER BY seq DESC"
	}
	q := `SELECT ` + auditEventCols + `
		FROM audit_event
		WHERE chain_id = $1 AND ` + cmp + ` AND ($3 = '' OR operation = $3)
		` + order + ` LIMIT $4`
	rows, err := s.pool.Query(ctx, q, chainID, cursor, operation, limit)
	if err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}
	defer rows.Close()
	var out []domain.AuditEvent
	for rows.Next() {
		e, err := scanAuditEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan audit event: %w", err)
		}
		out = append(out, *e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}
	return out, nil
}

// ReadEvent returns a single event by (chainID, seq), or domain.ErrNotFound.
func (s *AuditStore) ReadEvent(ctx context.Context, chainID string, seq int64) (*domain.AuditEvent, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+auditEventCols+` FROM audit_event WHERE chain_id = $1 AND seq = $2`,
		chainID, seq)
	e, err := scanAuditEvent(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read audit event: %w", err)
	}
	return e, nil
}

// ResolveEventOwner returns the tenant that owns the committed event at
// (chainID, seq), read straight from the append-only audit log.
//
// This is the authoritative source for execution-time ownership (ADR-TENANCY-003,
// ADR-TENANCY-004). It does not trust audit_event.tenant_id alone.
//
// A chain is the history of one governed object: chain_id equals the object's
// cfg_id. The object's own tenant_id is the root of authority — it is
// RLS-enforced at write time and cannot be set cross-tenant. audit_event.tenant_id
// is a denormalized copy written beside it. The two are read together and
// required to agree.
//
// Reading only the copy was exploitable: a partial restore, an operator INSERT,
// a legacy backfill or split-brain history can leave an audit row labelled with
// a different tenant than the object it records. Verified against the running
// service — a row appended to a victim's chain but labelled with an attacker's
// tenant caused the victim's event to be delivered to the attacker, because the
// resolver and the corrupted value were the same row. Cross-checking against the
// object detects the divergence.
//
// Failure modes, all closed:
//   - no committed event, or the chain has no governed object (deleted or
//     orphaned): domain.ErrEventNotFound;
//   - the object's owner and the audit label disagree: domain.ErrOwnershipAmbiguous,
//     which the consumers treat as a refusal and which startup validation
//     refuses to boot on.
func (s *AuditStore) ResolveEventOwner(ctx context.Context, chainID string, seq int64) (string, error) {
	var root, labeled string
	err := s.pool.QueryRow(ctx, `
		SELECT co.tenant_id, ae.tenant_id
		  FROM audit_event ae
		  JOIN configuration_object co ON co.cfg_id = ae.chain_id
		 WHERE ae.chain_id = $1 AND ae.seq = $2`,
		chainID, seq).Scan(&root, &labeled)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", domain.ErrEventNotFound
	}
	if err != nil {
		return "", fmt.Errorf("resolve event owner: %w", err)
	}
	if root != labeled {
		return "", fmt.Errorf("%w: chain %s seq %d: object owner %q, audit label %q",
			domain.ErrOwnershipAmbiguous, chainID, seq, root, labeled)
	}
	return root, nil
}

// ValidateOwnershipConsistency reports whether the audit log's ownership agrees
// with the governed objects it records, across every chain.
//
// It is run at startup. A divergence means authoritative ownership is ambiguous
// somewhere — a partial restore, an operator error, split-brain history — and
// the platform must not begin performing outbound actions on it. The check is a
// single indexed anti-join and returns the count and one example so the operator
// can locate the corruption.
//
// Orphaned chains (audit events whose object was deleted) are deliberately not
// flagged: append-only audit outlives its object by design, and the runtime
// resolver already refuses such chains as unresolvable.
func (s *AuditStore) ValidateOwnershipConsistency(ctx context.Context) (divergent int, example string, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT count(*), COALESCE(min(ae.chain_id), '')
		  FROM audit_event ae
		  JOIN configuration_object co ON co.cfg_id = ae.chain_id
		 WHERE ae.tenant_id <> co.tenant_id`).Scan(&divergent, &example)
	if err != nil {
		return 0, "", fmt.Errorf("validate ownership consistency: %w", err)
	}
	return divergent, example, nil
}

// VerifyRangeReader streams the events of chainID with seq in [fromSeq, toSeq]
// in ascending seq order, invoking fn for each. It is the read path the verifier
// uses to recompute the chain; it performs no verification itself. If fn returns
// an error, iteration stops and that error is returned.
func (s *AuditStore) VerifyRangeReader(ctx context.Context, chainID string, fromSeq, toSeq int64, fn func(*domain.AuditEvent) error) error {
	rows, err := s.pool.Query(ctx,
		`SELECT `+auditEventCols+`
		 FROM audit_event
		 WHERE chain_id = $1 AND seq BETWEEN $2 AND $3
		 ORDER BY seq`, chainID, fromSeq, toSeq)
	if err != nil {
		return fmt.Errorf("verify range query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		e, err := scanAuditEvent(rows)
		if err != nil {
			return fmt.Errorf("scan audit event: %w", err)
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	return rows.Err()
}

// rowScanner is satisfied by both pgx.Row (single row) and pgx.Rows (iteration).
type rowScanner interface {
	Scan(dest ...any) error
}

// scanAuditEvent scans one row into a domain.AuditEvent (payload jsonb excluded).
func scanAuditEvent(sc rowScanner) (*domain.AuditEvent, error) {
	var e domain.AuditEvent
	var op string
	if err := sc.Scan(
		&e.ChainID, &e.Seq, &e.EventID, &e.OperationID, &op, &e.Actor,
		&e.PayloadCanonical, &e.PrevHash, &e.ThisHash, &e.OccurredAt, &e.TenantID,
	); err != nil {
		return nil, err
	}
	e.Operation = domain.ConfigurationOperation(op)
	return &e, nil
}
