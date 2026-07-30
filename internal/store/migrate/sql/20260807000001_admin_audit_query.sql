-- OPS-S038 Administrative audit query API — the indexes the read path needs.
--
-- Additive. No completed migration is altered.
--
-- NO GRANTS. This is deliberate and is the story's least-privilege decision.
-- The reader runs on the privileged pool, so oneops_app -- the role every
-- tenant request connection assumes -- keeps exactly what OPS-S035 left it:
-- INSERT on the events table, SELECT/INSERT/UPDATE on the chain head, and no
-- read access to administrative history at all. Granting SELECT to oneops_app
-- would put every customer's administrative trail one SQL statement away from
-- any request-path code, on a table ADR-AUDIT-007 §6.4 deliberately leaves
-- outside row-level security, with the platform-admin middleware as the only
-- thing in between. Reading through the privileged pool costs one wiring
-- exemption and keeps that surface closed.
--
-- WHY THESE INDEXES. OPS-S034 shipped indexes for actor, operation, occurred_at
-- and a partial one for subject_org_id, and recorded -- correctly -- that they
-- do not cover the whole of §6.1's "who did what, to whom, when". The two
-- missing subject columns and the ordering key are added here, where the reader
-- that needs them exists, rather than being carried speculatively since S034.
-- Measured against 1.025M rows before adding them: subject_user_id was a 172 ms
-- parallel sequential scan over 65,053 buffers, subject_tenant_id 235 ms over
-- 191,500 buffers discarding 710,399 rows.

-- The ordering key. occurred_at alone is NOT unique -- §6.8's multi-chain shape
-- puts sibling acts on the same timestamp -- so a cursor over it silently skips
-- rows at a page boundary. (chain_id, seq) is the primary key and therefore
-- breaks every tie, which makes (occurred_at, chain_id, seq) a total order and
-- the only stable cursor this table admits. DESC matches the read path's
-- newest-first default so the index is walked forwards, not sorted.
CREATE INDEX IF NOT EXISTS ix_admin_audit_page
    ON admin_audit_event (occurred_at DESC, chain_id DESC, seq DESC);

-- "To whom", the two columns S034 left uncovered. Partial because an act on a
-- user has no organisation and an act on the tenant registry has no user, so
-- the NULLs are the majority for each and indexing them would be dead weight.
CREATE INDEX IF NOT EXISTS ix_admin_audit_subject_user
    ON admin_audit_event (subject_user_id, occurred_at DESC)
    WHERE subject_user_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS ix_admin_audit_subject_tenant
    ON admin_audit_event (subject_tenant_id, occurred_at DESC)
    WHERE subject_tenant_id IS NOT NULL;

COMMENT ON INDEX ix_admin_audit_page IS
    'The stable ordering key for OPS-S038 cursor pagination. occurred_at is not '
    'unique; (chain_id, seq) is the primary key and breaks every tie, so this is '
    'a total order and a keyset cursor over it can neither skip nor repeat a row.';
