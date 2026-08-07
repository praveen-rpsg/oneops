-- OPS-S<E8.5> Security-response automation — the SAFE slice of SOAR,
-- completing the E8 SOC epic (ADR-SOC-010). Two tables:
--
--   security_response_rule: a condition (min_severity threshold, optional
--   asset_id scope) + one SAFE ActionSpec (action_type/action_config) — a
--   CONFIG row, mirroring security_rule/ioc exactly. NOT a reified
--   Workflow/Playbook (Vol II §5.3 reduced those away): one rule runs one
--   action, no step sequence, no branching.
--
--   security_response_dispatch: an APPEND-ONLY exactly-once ledger —
--   UNIQUE (tenant_id, incident_id, rule_id) — the leader-gated
--   SecurityResponder (internal/security/responder.go) claims a row here
--   BEFORE running a rule's action, so a restart or a repeated pass can
--   never fire the same (incident, rule) pair twice.
--
-- Both are TENANT-SCOPED, exactly as security_rule/ioc are: tenant_id is
-- denormalised onto the row and is the RLS key.
--
-- This migration does NOT touch audit_event/admin_audit_event, does NOT add
-- a domain.ConfigurationOperation value, and is not consumed by
-- policy.Consumer — a security incident's response is triggered directly
-- from the SECURITY-INCIDENT lifecycle (internal/security), never injected
-- into the constitutional governance hash-chain (docs/PLATFORM-BUILD-PLAN.md
-- E8.5, ADR-SOC-010).
CREATE TABLE IF NOT EXISTS security_response_rule (
    rule_id       text        PRIMARY KEY,
    tenant_id     text        NOT NULL DEFAULT 'system' REFERENCES tenant (tenant_id),
    name          text        NOT NULL,
    min_severity  text        NOT NULL,
    asset_id      text        REFERENCES asset (asset_id) ON DELETE CASCADE,
    action_type   text        NOT NULL,
    action_config jsonb       NOT NULL DEFAULT '{}'::jsonb,
    enabled       boolean     NOT NULL DEFAULT true,
    row_version   bigint      NOT NULL DEFAULT 1,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ck_security_response_rule_name_length   CHECK (char_length(name) BETWEEN 1 AND 200),
    CONSTRAINT ck_security_response_rule_min_severity  CHECK (min_severity IN ('critical', 'high', 'medium', 'low')),
    -- The SAFE allowlist, enforced at the database as well as in
    -- domain.ValidSecurityResponseActionType: 'command' (arbitrary
    -- execution) and any destructive/response verb (isolate, block,
    -- disable, quarantine) can never reach this column, even from a caller
    -- that bypassed the application layer entirely.
    CONSTRAINT ck_security_response_rule_action_type   CHECK (action_type IN ('http', 'notification')),
    CONSTRAINT ck_security_response_rule_row_version    CHECK (row_version >= 1)
);

CREATE INDEX IF NOT EXISTS ix_security_response_rule_tenant_enabled ON security_response_rule (tenant_id, enabled);
CREATE INDEX IF NOT EXISTS ix_security_response_rule_asset          ON security_response_rule (asset_id);

COMMENT ON TABLE security_response_rule IS
    'A security-response-automation config (E8.5, ADR-SOC-010): condition (min_severity + optional asset_id) + one SAFE ActionSpec (http|notification). NOT a reified Workflow/Playbook. Executed by the leader-gated SecurityResponder against NEW security-sourced incidents.';
COMMENT ON COLUMN security_response_rule.tenant_id IS
    'The RLS key. NOT NULL and denormalised, the same reason asset.tenant_id/security_rule.tenant_id are (ADR-IDENTITY-002 §6).';
COMMENT ON COLUMN security_response_rule.asset_id IS
    'Optional: scopes this rule to incidents linked to one Configuration Item. NULL means every security incident this tenant raises. Re-verified against the writer''s own tenant-scoped connection before insert — the foreign key alone bypasses row-level security on asset (ADR-ASSET-001 §6).';
COMMENT ON COLUMN security_response_rule.action_type IS
    'SAFE allowlist ONLY: http (webhook) or notification (internal) — the exact action-type strings policy.DefaultRegistry registers under those names, reused here for EXECUTION. command (arbitrary execution) and any destructive/response action are refused at the database as well as in the application layer (domain.ValidSecurityResponseActionType). Destructive/autonomous response is DEFERRED behind the machine-action attestation model.';
COMMENT ON COLUMN security_response_rule.action_config IS
    'Opaque configuration for action_type, validated by the action implementation itself when it runs (policy.Registry), not by this table.';

ALTER TABLE security_response_rule ENABLE ROW LEVEL SECURITY;
ALTER TABLE security_response_rule FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON security_response_rule
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

-- ---------------------------------------------------------------------------
-- security_response_dispatch: the append-only exactly-once ledger. A ROW
-- CLAIMS one (incident, rule) pair BEFORE the rule's SAFE action runs
-- (record-first, not run-then-record) — see
-- internal/security/responder.go's own doc comment for why this ordering,
-- rather than alerting/security's usual run-then-record, is the right trade
-- for a SAFE-action responder: an outbound webhook POST or internal
-- notification firing TWICE after a crash-and-retry is a worse failure mode
-- here than firing zero times in that same, rare crash window.
--
-- Hardened exactly like incident_event/control_evidence (ENABLE ALWAYS
-- triggers + REVOKEd UPDATE/DELETE/TRUNCATE), built in from this table's
-- first migration, and registered in
-- postgres.SchemaValidator.immutableAuditTables so boot and the sentinel
-- both verify it — a PLAIN append-only table, NOT part of the
-- audit_event/admin_audit_event hash chain (no chain_id/seq/this_hash/
-- prev_hash): the uniqueness constraint alone is this ledger's integrity
-- mechanism, not a hash chain.
CREATE TABLE IF NOT EXISTS security_response_dispatch (
    dispatch_id    text        PRIMARY KEY,
    tenant_id      text        NOT NULL DEFAULT 'system' REFERENCES tenant (tenant_id),
    incident_id    text        NOT NULL REFERENCES incident (incident_id) ON DELETE CASCADE,
    rule_id        text        NOT NULL REFERENCES security_response_rule (rule_id) ON DELETE CASCADE,
    action_type    text        NOT NULL,
    dispatched_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ck_security_response_dispatch_action_type CHECK (action_type IN ('http', 'notification')),
    CONSTRAINT uq_security_response_dispatch_once UNIQUE (tenant_id, incident_id, rule_id)
);

CREATE INDEX IF NOT EXISTS ix_security_response_dispatch_incident ON security_response_dispatch (tenant_id, incident_id);

COMMENT ON TABLE security_response_dispatch IS
    'Append-only exactly-once ledger (E8.5, ADR-SOC-010): a row CLAIMS one (incident, rule) pair BEFORE the rule''s SAFE action runs, so a restart or repeated SecurityResponder pass never double-fires. NOT part of the audit_event/admin_audit_event hash chain.';
COMMENT ON CONSTRAINT uq_security_response_dispatch_once ON security_response_dispatch IS
    'The exactly-once mechanism itself: SecurityResponder INSERTs ... ON CONFLICT DO NOTHING against this constraint before running the action; a conflict means an earlier pass already claimed (and ran) this pair.';

ALTER TABLE security_response_dispatch ENABLE ROW LEVEL SECURITY;
ALTER TABLE security_response_dispatch FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON security_response_dispatch
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

-- Append-only enforcement, built in from day one — mirrors
-- incident_event's/control_evidence's identical hardening verbatim. ENABLE
-- ALWAYS fires regardless of session_replication_role, closing the bypass
-- 20260809000001 proved live against origin-mode triggers.
CREATE OR REPLACE FUNCTION security_response_dispatch_immutable() RETURNS trigger
    LANGUAGE plpgsql AS
$$
BEGIN
    RAISE EXCEPTION 'security_response_dispatch is append-only: % is not permitted', TG_OP;
END;
$$;

CREATE OR REPLACE TRIGGER trg_security_response_dispatch_no_row_mutate
    BEFORE UPDATE OR DELETE ON security_response_dispatch
    FOR EACH ROW EXECUTE FUNCTION security_response_dispatch_immutable();
ALTER TABLE security_response_dispatch ENABLE ALWAYS TRIGGER trg_security_response_dispatch_no_row_mutate;

CREATE OR REPLACE TRIGGER trg_security_response_dispatch_no_truncate
    BEFORE TRUNCATE ON security_response_dispatch
    FOR EACH STATEMENT EXECUTE FUNCTION security_response_dispatch_immutable();
ALTER TABLE security_response_dispatch ENABLE ALWAYS TRIGGER trg_security_response_dispatch_no_truncate;

-- Privilege, the second, independent layer (ADR-AUDIT-008 §5's reasoning,
-- applied from the start, exactly as incident_event's/control_evidence's
-- does). SELECT and INSERT are RETAINED: the responder's privileged
-- connection both claims (INSERT) and can read this ledger back; nothing
-- ever UPDATEs or DELETEs a row.
REVOKE UPDATE, DELETE, TRUNCATE ON security_response_dispatch FROM PUBLIC;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'oneops_app') THEN
        EXECUTE 'REVOKE UPDATE, DELETE, TRUNCATE ON security_response_dispatch FROM oneops_app';
    END IF;
END;
$$;
