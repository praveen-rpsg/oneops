-- WP-002 Audit Integrity — tamper-evident, hash-chained audit record.
-- Additive; does not touch M1/M2 tables. Role-agnostic DDL only (ADR-AUDIT-003):
-- no CREATE ROLE, GRANT, or REVOKE — privilege separation is provisioned by the
-- infrastructure bootstrap. Physical model per ADR-AUDIT-004: audit_event is
-- LIST-partitioned by chain_id with a DEFAULT partition; payload jsonb is
-- application-populated (not GENERATED — convert_from is not IMMUTABLE).

-- audit_chain_head: the mutable tip of each audit chain (non-partitioned).
CREATE TABLE IF NOT EXISTS audit_chain_head (
    chain_id   text        PRIMARY KEY,
    last_seq   bigint      NOT NULL,
    last_hash  bytea       NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ck_head_hash_len CHECK (octet_length(last_hash) = 32)
);

-- audit_event: append-only, hash-chained record; LIST-partitioned by chain_id so
-- every UNIQUE/PRIMARY KEY includes the partition key (PostgreSQL requirement).
CREATE TABLE IF NOT EXISTS audit_event (
    chain_id          text        NOT NULL,
    seq               bigint      NOT NULL,
    event_id          text        NOT NULL,
    operation_id      text        NOT NULL,
    operation         text        NOT NULL,
    actor             text        NOT NULL,
    payload_canonical bytea       NOT NULL,
    payload           jsonb       NOT NULL,
    prev_hash         bytea       NOT NULL,
    this_hash         bytea       NOT NULL,
    occurred_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT pk_audit_event      PRIMARY KEY (chain_id, seq),
    CONSTRAINT uq_audit_event_id   UNIQUE (chain_id, event_id),
    CONSTRAINT uq_audit_this_hash  UNIQUE (chain_id, this_hash),
    CONSTRAINT ck_audit_prev_hash_len CHECK (octet_length(prev_hash) = 32),
    CONSTRAINT ck_audit_this_hash_len CHECK (octet_length(this_hash) = 32),
    CONSTRAINT ck_audit_operation CHECK (operation IN
        ('ratification','approval','extension','replacement','suspension',
         'deprecation','withdrawal','archiving','historical_preservation',
         'deletion','baseline_freeze','amendment'))
) PARTITION BY LIST (chain_id);

-- DEFAULT partition: guarantees inserts succeed for any chain until dedicated
-- per-chain (and later per-month) partitions are added operationally (ECR-09).
CREATE TABLE IF NOT EXISTS audit_event_default PARTITION OF audit_event DEFAULT;

CREATE INDEX IF NOT EXISTS ix_audit_operation   ON audit_event (operation);
CREATE INDEX IF NOT EXISTS ix_audit_occurred_at ON audit_event (occurred_at);

-- Immutability enforcement (defense-in-depth alongside privilege revocation in
-- the bootstrap): block UPDATE/DELETE at row level and TRUNCATE at statement
-- level. Deletion of audit history is forbidden (State Model §8).
CREATE OR REPLACE FUNCTION audit_event_immutable() RETURNS trigger
    LANGUAGE plpgsql AS
$$
BEGIN
    RAISE EXCEPTION 'audit_event is append-only: % is not permitted', TG_OP;
END;
$$;

CREATE OR REPLACE TRIGGER trg_audit_event_no_row_mutate
    BEFORE UPDATE OR DELETE ON audit_event
    FOR EACH ROW EXECUTE FUNCTION audit_event_immutable();

CREATE OR REPLACE TRIGGER trg_audit_event_no_truncate
    BEFORE TRUNCATE ON audit_event
    FOR EACH STATEMENT EXECUTE FUNCTION audit_event_immutable();
