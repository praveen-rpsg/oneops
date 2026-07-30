-- OPS-S034 Administrative audit — schema and immutability triggers.
-- Realises ADR-AUDIT-007 §6.3-6.8 and §6.11. Additive: no completed migration
-- is altered, and no backfill is performed (§6.11 — acts before this store
-- existed were never recorded, and inventing them would fabricate history).
--
-- OWNERSHIP AND ISOLATION (§6.3, §6.4). The platform owns every row. These
-- tables are GLOBAL: no tenant_id ownership key, deliberately absent from
-- TenantOwnedTables, no row-level security. The organisation or tenant acted
-- upon is recorded as an ATTRIBUTE -- the fact, not the owner -- in
-- subject_org_id / subject_tenant_id. Those columns are deliberately NOT named
-- tenant_id: TestSchema_EveryTenantIdTableIsCanonicalAndProtected enumerates
-- the live schema for tenant_id columns and fails any table carrying one that
-- is absent from TenantOwnedTables, which §6.11 forbids us to join.
--
-- WHY A SEPARATE STORE, NOT audit_event (§2.1-2.3). audit_event's
-- ck_audit_operation CHECK admits twelve Configuration Object operations and
-- refuses every administrative one; it is partitioned and keyed by a chain that
-- an administrative act does not have; and its single writer is bound by
-- ADR-AUDIT-005 to a governance mutation. Reuse is structurally unavailable,
-- not merely unwise.
--
-- VOCABULARY DISJOINTNESS. ck_admin_audit_operation's values are dotted
-- (subject.verb) and therefore disjoint by construction from audit_event's
-- twelve bare values -- note that 'suspension' and 'deletion' appear there, so
-- bare verbs would collide. Disjointness is what keeps exactly one source of
-- truth per fact: no fact recorded on audit_event is recorded here, and no
-- fact recorded here is recorded there.
--
-- NOT PARTITIONED, DELIBERATELY. audit_event is LIST-partitioned by chain_id,
-- and that is precisely why migration 20260728000002 had to exist: PostgreSQL
-- does not propagate a statement-level TRUNCATE trigger from a partitioned
-- parent to its partitions, so `TRUNCATE audit_event` was refused while
-- `TRUNCATE audit_event_default` erased the same rows. Declining partitioning
-- removes that entire failure class rather than re-mitigating it.
--
-- A CORRECTION TO ADR-AUDIT-007 SS7, WHICH THIS TABLE CANNOT HONOUR AS WRITTEN.
-- SS7 offers partitioning as a mitigation "available exactly as for
-- audit_event" should per-organisation chains proliferate. For TIME-based
-- partitioning that is false, and PostgreSQL refuses it outright: a unique
-- constraint on a partitioned table must contain every partitioning column, and
-- all three unique constraints below (pk_admin_audit_event,
-- uq_admin_audit_event_id, uq_admin_audit_this_hash) are chain_id-prefixed and
-- carry no occurred_at. Measured: "PRIMARY KEY ... lacks column occurred_at".
-- So only LIST/HASH on chain_id is legal -- which does not divide the hot data,
-- because the platform chain is a single chain_id and holds every act that has
-- no organisation. Partitioning this table by time would require rewriting the
-- primary key of an append-only table. That is a real constraint on the growth
-- story, and it is recorded here rather than discovered later.
--
-- A future migration that does partition by chain_id MUST also attach the
-- truncate guard to every partition by catalogue lookup, exactly as
-- 20260728000002 does, and must arm each one with ENABLE ALWAYS.
--
-- NO FOREIGN KEYS ON THE SUBJECT COLUMNS. An administrative record must outlive
-- its subject and must never be blocked, cascaded, or nulled by the subject's
-- lifecycle. The subject ids are recorded as facts, not as references.

-- admin_audit_chain_head: the mutable tip of each administrative chain.
-- One chain per organisation, plus a platform chain for acts with no
-- organisation (§6.8). A single global chain is refused: ADR-AUDIT-006
-- serialises appends on the chain head under FOR UPDATE, so one chain would
-- serialise every administrative write platform-wide. The chain-id derivation
-- function belongs to OPS-S035; this schema only requires that it be non-empty.
CREATE TABLE IF NOT EXISTS admin_audit_chain_head (
    chain_id   text        PRIMARY KEY,
    last_seq   bigint      NOT NULL,
    last_hash  bytea       NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ck_admin_head_hash_len CHECK (octet_length(last_hash) = 32),
    CONSTRAINT ck_admin_head_chain_id CHECK (chain_id <> ''),
    CONSTRAINT ck_admin_head_last_seq CHECK (last_seq >= 0)
);

-- admin_audit_event: append-only, hash-chained administrative record.
-- Column set is deliberately sufficient for internal/audit's existing
-- ChainHash and Verifier to reconstruct the chain without a second sealing
-- implementation (§6.10 forbids re-deriving the sealing logic): chain_id, seq,
-- event_id, operation, actor, occurred_at and payload_canonical are exactly
-- audit.EventHashInput's seven hashed fields.
CREATE TABLE IF NOT EXISTS admin_audit_event (
    chain_id          text        NOT NULL,
    seq               bigint      NOT NULL,
    event_id          text        NOT NULL,
    operation_id      text        NOT NULL,
    operation         text        NOT NULL,
    actor             text        NOT NULL,
    subject_org_id    text,
    subject_tenant_id text,
    subject_user_id   text,
    payload_canonical bytea       NOT NULL,
    payload           jsonb       NOT NULL,
    prev_hash         bytea       NOT NULL,
    this_hash         bytea       NOT NULL,
    occurred_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT pk_admin_audit_event          PRIMARY KEY (chain_id, seq),
    CONSTRAINT uq_admin_audit_event_id       UNIQUE (chain_id, event_id),
    CONSTRAINT uq_admin_audit_this_hash      UNIQUE (chain_id, this_hash),
    CONSTRAINT ck_admin_audit_prev_hash_len  CHECK (octet_length(prev_hash) = 32),
    CONSTRAINT ck_admin_audit_this_hash_len  CHECK (octet_length(this_hash) = 32),
    -- Seq is assigned by the appender as lastSeq + 1 under the chain-head lock.
    -- The floor is enforced here as well so a direct INSERT cannot pre-seed a
    -- seq of 0 or below and wedge the chain against its own primary key.
    CONSTRAINT ck_admin_audit_seq            CHECK (seq >= 1),
    CONSTRAINT ck_admin_audit_chain_id       CHECK (chain_id <> ''),
    CONSTRAINT ck_admin_audit_actor          CHECK (actor <> ''),
    -- audit.ChainHash refuses an empty payload; the database refuses it too, so
    -- an unsealed row cannot be introduced by a path that bypasses the appender.
    CONSTRAINT ck_admin_audit_payload        CHECK (octet_length(payload_canonical) > 0),
    -- Dotted, and therefore disjoint from audit_event's twelve bare values.
    -- New administrative operations are new values here and require no change
    -- to ADR-AUDIT-007 (§6.13).
    CONSTRAINT ck_admin_audit_operation CHECK (operation IN (
        'user.created', 'user.profile_updated', 'user.status_changed',
        'organization.created', 'organization.status_changed',
        'tenant.created', 'tenant.status_changed',
        'invitation.created', 'invitation.redeemed', 'invitation.revoked',
        'membership.granted', 'membership.revoked'))
);

-- Query paths this store is expected to serve (SS6.1 "who did what, to whom,
-- when"). The admin audit query API is OPS-S038.
--
-- These indexes cover "who" (actor), "what" (operation) and "when"
-- (occurred_at), and one of the three "to whom" columns (subject_org_id).
-- They do NOT cover subject_user_id or subject_tenant_id, and OPS-S038 WILL
-- need a schema change if its "target" filter accepts either. Measured against
-- 1.025M rows: subject_user_id = 172 ms parallel seq scan (65,053 buffers),
-- subject_tenant_id = 235 ms (191,500 buffers, 710,399 rows discarded);
-- with matching partial indexes, 0.14 ms and 0.06 ms. Those indexes are not
-- created here because they cost append latency inside the chain-head lock for
-- a reader that does not exist yet -- OPS-S038 should add exactly the ones its
-- filter set needs.
--
-- Also unresolved for OPS-S038: occurred_at is not unique, and SS6.8's
-- multi-chain shape guarantees ties, so a keyset cursor over occurred_at alone
-- silently skips rows at a page boundary while a row-value cursor against the
-- single-column index below degrades quadratically within a tie block
-- (measured: 17.1 ms / 20,014 buffers through a 20k tie, versus 0.07 ms with a
-- matching (occurred_at, chain_id, seq) index). A stable global ordering needs
-- that composite index; it is OPS-S038's to add with its cursor design.
CREATE INDEX IF NOT EXISTS ix_admin_audit_occurred_at ON admin_audit_event (occurred_at);
CREATE INDEX IF NOT EXISTS ix_admin_audit_actor       ON admin_audit_event (actor, occurred_at);
CREATE INDEX IF NOT EXISTS ix_admin_audit_operation   ON admin_audit_event (operation, occurred_at);
-- Partial: most administrative acts carry a subject organisation, but
-- platform-chain acts (a user created outside any organisation) do not, and
-- those rows would otherwise bloat the index.
CREATE INDEX IF NOT EXISTS ix_admin_audit_subject_org ON admin_audit_event (subject_org_id, occurred_at)
    WHERE subject_org_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Immutability.
--
-- The acceptance criterion for OPS-S034 is "triggers as for audit_event". These
-- are modelled on audit_event's (20260723000001) and are deliberately STRONGER
-- in two respects, both of which were proven by execution against a live
-- database while this story was being reviewed:
--
--   1. ENABLE ALWAYS. audit_event's triggers are created with the default
--      "origin" firing mode (pg_trigger.tgenabled = 'O'). A session that sets
--      session_replication_role = 'replica' suppresses them entirely.
--      Measured: a single SET followed by one UPDATE rewrote every row of
--      audit_event with no error raised. ENABLE ALWAYS (tgenabled = 'A') fires
--      regardless of replication role; the same attempt is then refused.
--
--      Precisely who can do this, because the distinction matters: the
--      parameter's context is "superuser". The PRIVILEGED pool's role can set
--      it (in the dev compose stack that role is the cluster superuser, and the
--      repository's own restore-integrity test relies on the capability). The
--      REQUEST-PATH role cannot -- measured: oneops_app gets "permission denied
--      to set parameter". So this hardening defends against the owning pool,
--      operators and DBA sessions, not against a tenant request. It is
--      defence-in-depth against insiders, and should not be described as
--      closing a tenant-reachable hole.
--
--   2. Privilege, not only trigger. See the REVOKE block below.
--
-- A trigger is the last line, not the only one: 20260723000001's own header
-- describes the triggers as "defense-in-depth alongside privilege revocation in
-- the bootstrap", but no such bootstrap exists anywhere in infra/, deploy/ or
-- scripts/ -- so on audit_event that stated second layer is not running. Here
-- it is, in this migration.
CREATE OR REPLACE FUNCTION admin_audit_event_immutable() RETURNS trigger
    LANGUAGE plpgsql AS
$$
BEGIN
    RAISE EXCEPTION 'admin_audit_event is append-only: % is not permitted', TG_OP;
END;
$$;

CREATE OR REPLACE TRIGGER trg_admin_audit_event_no_row_mutate
    BEFORE UPDATE OR DELETE ON admin_audit_event
    FOR EACH ROW EXECUTE FUNCTION admin_audit_event_immutable();
ALTER TABLE admin_audit_event ENABLE ALWAYS TRIGGER trg_admin_audit_event_no_row_mutate;

CREATE OR REPLACE TRIGGER trg_admin_audit_event_no_truncate
    BEFORE TRUNCATE ON admin_audit_event
    FOR EACH STATEMENT EXECUTE FUNCTION admin_audit_event_immutable();
ALTER TABLE admin_audit_event ENABLE ALWAYS TRIGGER trg_admin_audit_event_no_truncate;

-- ---------------------------------------------------------------------------
-- Privilege.
--
-- 20260729000001_rls_policies.sql ends with
--
--     ALTER DEFAULT PRIVILEGES IN SCHEMA %I
--       GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO oneops_app
--
-- with the comment "Tables added by later migrations must be reachable without
-- revisiting this one." This table IS a table added by a later migration, so it
-- arrives granted SELECT/INSERT/UPDATE/DELETE to oneops_app -- the role every
-- request-scoped connection assumes (NewTenantScopedPool does SET ROLE
-- oneops_app). ADR-AUDIT-007 §6.4 forbids the row-level security that would
-- otherwise confine it. Without the block below, the request path would arrive
-- holding write access to the administrative audit trail on day one.
--
-- On admin_audit_event, UPDATE/DELETE/TRUNCATE are revoked permanently: the
-- table is append-only, so nothing may ever hold them.
--
-- On admin_audit_chain_head the rule is DIFFERENT, and saying otherwise would
-- be false: the chain head is the MUTABLE tip. Its whole purpose is to be
-- advanced by UpsertChainHead, and PostgreSQL requires UPDATE privilege even to
-- take the SELECT ... FOR UPDATE lock that ADR-AUDIT-006 serialises appends on.
-- OPS-S035 MUST therefore grant SELECT, INSERT, UPDATE on this table to
-- whichever role its appender runs as. Revoking here is a "no writer exists
-- yet" measure, not a permanent prohibition.
--
-- INSERT on the event table is revoked for the same reason. OPS-S034 ships no
-- writer; OPS-S035 grants what its appender needs, at the point where a writer
-- actually exists. Until then the store is unwritable by the request path,
-- which closes the interval in which a forged row could pre-seed a chain and
-- wedge every subsequent administrative act against the primary key. That wedge
-- is not hypothetical: a forged (chain_id, seq) is unrecoverable here, because
-- the append-only triggers refuse the DELETE that would clear it.
--
-- SELECT is revoked on BOTH tables. The chain head is not merely bookkeeping:
-- chain_id identifies the organisation (SS6.8), last_seq counts the
-- administrative acts committed against it, and updated_at times the most
-- recent one. Left readable on a table with no row-level security, it is an
-- enumeration oracle over every customer's administrative activity -- precisely
-- what SS6.5 confines to platform administrators.
--
-- This migration is therefore NOT role-agnostic, departing from
-- 20260723000001's header. That header defers privilege separation to an
-- "infrastructure bootstrap" and attributes the rule to ADR-AUDIT-003 -- but
-- ADR-AUDIT-003 contains no such rule, the bootstrap does not exist, and
-- 20260729000001 already creates roles and grants inside a migration. Privilege
-- management in migrations is the established, more recent practice.
REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON admin_audit_event      FROM PUBLIC;
REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON admin_audit_chain_head FROM PUBLIC;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'oneops_app') THEN
        EXECUTE 'REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON admin_audit_event      FROM oneops_app';
        EXECUTE 'REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON admin_audit_chain_head FROM oneops_app';
        -- SS6.5 confines visibility to platform administrators, and OPS-S038's
        -- reader is the only thing that should ever hold SELECT. Granting it
        -- back is that story's decision to record. Both tables: see the note
        -- above on the chain head as an enumeration oracle.
        EXECUTE 'REVOKE SELECT ON admin_audit_event      FROM oneops_app';
        EXECUTE 'REVOKE SELECT ON admin_audit_chain_head FROM oneops_app';
    END IF;
END;
$$;

COMMENT ON TABLE admin_audit_event IS
    'Append-only, hash-chained record of administrative acts (ADR-AUDIT-007 §6). '
    'Platform-owned and global: no tenant_id, no row-level security, absent from '
    'TenantOwnedTables. Not an event source -- the relay and replay read '
    'audit_event alone, so these rows are undeliverable by construction rather '
    'than by filter. Never pruned.';
COMMENT ON COLUMN admin_audit_event.chain_id IS
    'One chain per organisation, plus a platform chain for acts with no '
    'organisation (ADR-AUDIT-007 §6.8). Derivation belongs to OPS-S035.';
COMMENT ON COLUMN admin_audit_event.subject_org_id IS
    'The organisation ACTED UPON -- an attribute, never an ownership or RLS key '
    '(ADR-AUDIT-007 §6.4). Deliberately not a foreign key: the record outlives '
    'its subject.';
COMMENT ON COLUMN admin_audit_event.subject_tenant_id IS
    'The tenant acted upon, as an attribute. Deliberately NOT named tenant_id: '
    'that name is reserved for ownership keys and is swept by '
    'TestSchema_EveryTenantIdTableIsCanonicalAndProtected.';
COMMENT ON COLUMN admin_audit_event.payload_canonical IS
    'Canonical JSON as sealed by audit.ChainHash. The subject identifiers must '
    'also appear here, inside the hashed payload -- the sibling subject_* '
    'columns exist for indexing and are outside the hash, so they alone are not '
    'evidence. Binding them is the appender''s obligation (OPS-S035).';
COMMENT ON TABLE admin_audit_chain_head IS
    'Mutable tip of each administrative audit chain. Serialises appends under '
    'FOR UPDATE (ADR-AUDIT-006). Global and platform-owned, like the events.';
