-- OPS-S035 Audit interception at the storage chokepoint -- the writer's
-- privileges, and the constraint that makes a record's subject evidence.
--
-- Additive. 20260805000001 is checksummed in atlas.sum and is not edited.
--
-- WHY GRANTS ARE NEEDED AT ALL. OPS-S034 revoked everything, deliberately,
-- because it shipped no writer and a table nothing writes should not be
-- writable. It recorded that the chokepoint story must grant what its appender
-- needs. This is that grant, and the set is measured rather than guessed:
-- with INSERT alone on the event table, the appender's
-- SELECT ... FOR UPDATE on the chain head -- the ADR-AUDIT-006 serialisation
-- point -- fails with "permission denied", because PostgreSQL requires UPDATE
-- privilege to lock a row for update.
--
-- WHY oneops_app AND NOT A DEDICATED ROLE. ADR-AUDIT-005's shape, applied to
-- administration by ADR-AUDIT-007 SS6.9, requires the audit append and the
-- administrative mutation to commit in ONE transaction, and ADR-TENANCY-002
-- clause 5 records that a transaction cannot span two pools. The identity
-- stores are built from the tenant-scoped pool, which assumes oneops_app, so
-- the appender runs as oneops_app or it cannot share their transaction. A
-- dedicated NOINHERIT writer role entered with SET LOCAL ROLE was considered
-- and refused here: it would require RESET ROLE inside a shared transaction,
-- which is the documented escape back to the table owner, and it would trade a
-- narrow grant for a wider one. Recorded as engineering debt, not silently
-- dropped.
--
-- WHAT IS STILL WITHHELD, AND WHY THAT IS THE POINT. UPDATE, DELETE and
-- TRUNCATE on admin_audit_event are NOT granted and never will be: append-only
-- is enforced by privilege as well as by the ENABLE ALWAYS triggers. SELECT on
-- admin_audit_event is NOT granted -- reading the administrative trail is
-- OPS-S038's, behind requirePlatformAdmin, and it must grant that itself.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'oneops_app') THEN
        -- The appender writes events and nothing else.
        EXECUTE 'GRANT INSERT ON admin_audit_event TO oneops_app';
        -- The chain head is the mutable tip: INSERT for a chain's first act,
        -- SELECT + UPDATE for the FOR UPDATE lock and the advance.
        EXECUTE 'GRANT SELECT, INSERT, UPDATE ON admin_audit_chain_head TO oneops_app';
    END IF;
END;
$$;

-- The subject identifiers are indexed columns AND sealed payload fields, and
-- this constraint is what keeps the two from diverging.
--
-- audit.ChainHash covers seven fields: chain_id, seq, event_id, operation,
-- actor, occurred_at and payload_canonical. The subject_* columns are not among
-- them. Without this constraint a row's "to whom" would live outside its own
-- hash, so anyone who reached UPDATE could retarget an administrative record to
-- a different organisation and the chain would still verify. The chokepoint
-- writes the identifiers into the canonical payload; here the database refuses
-- any row whose column disagrees with what was sealed.
--
-- NULL columns are permitted and unconstrained in the payload direction: an act
-- on a user has no organisation, and an absent subject is written as NULL
-- rather than '' so "no subject" and "empty subject" stay distinguishable.
ALTER TABLE admin_audit_event
    ADD CONSTRAINT ck_admin_audit_subject_bound CHECK (
        (subject_org_id    IS NULL OR payload ->> 'subject_org_id'    = subject_org_id)
    AND (subject_tenant_id IS NULL OR payload ->> 'subject_tenant_id' = subject_tenant_id)
    AND (subject_user_id   IS NULL OR payload ->> 'subject_user_id'   = subject_user_id)
    );

COMMENT ON CONSTRAINT ck_admin_audit_subject_bound ON admin_audit_event IS
    'Ties each subject column to the same key inside payload, which IS covered '
    'by the chain hash. Without it the record''s subject would be unsealed and '
    'a rewrite could retarget the act while the chain still verified.';
