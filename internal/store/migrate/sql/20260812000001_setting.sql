-- OPS-S046 Setting — a validated, tenant-scoped key-value override of the
-- platform's in-code SettingDefinition registry (internal/domain/setting.go).
--
-- setting is TENANT-SCOPED, exactly as team is (20260810000001): tenant_id is
-- denormalised onto the row and is the RLS key, and the primary key is
-- (tenant_id, key) rather than a surrogate id, which is what makes this row
-- automatically pass arch's tenant-key-scope sweep: the key space one tenant
-- can occupy for a given setting name is confined to its own tenant_id by the
-- primary key itself, not by a remembered justification.
--
-- VALIDATED KEYS, NOT A JUNK DRAWER. There is deliberately no CHECK on `key`
-- here: the allowlist is the in-code SettingDefinition registry, enforced by
-- domain.Setting.Validate before any write reaches this table. A schema-level
-- enum would duplicate that registry and the two would drift; the store is the
-- single source for "which keys exist", the domain package is the single
-- source for "what a legal value looks like" (a currency this codebase already
-- pays for team.status and notification.channel — see their own CHECK
-- constraints in the migrations that created them). `value` is unconstrained
-- text at the schema level for the same reason: it holds the canonical string
-- form of a bool, int, string or enum, and only SettingDefinition knows which.
--
-- NO ADMINISTRATIVE AUDIT. ADR-AUDIT-007 §6.2 scopes the administrative-audit
-- chokepoint (withAdminAudit) to exactly five named tables — app_user,
-- organization, membership, invitation, tenant. Setting is application
-- configuration, not an identity-governance fact, so it follows the same
-- pattern as team, webhook, policy and notification: tenant-owned and under
-- row-level security, without the administrative chokepoint.

CREATE TABLE IF NOT EXISTS setting (
    tenant_id   text        NOT NULL DEFAULT 'system' REFERENCES tenant (tenant_id),
    key         text        NOT NULL,
    value       text        NOT NULL,
    row_version bigint      NOT NULL DEFAULT 1,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, key)
);

COMMENT ON TABLE setting IS
    'A tenant''s stored override of a SettingDefinition. Absence of a row for a (tenant_id, key) means the tenant has not overridden that key; the effective value is the definition''s default (internal/domain/setting.go).';
COMMENT ON COLUMN setting.tenant_id IS
    'The RLS key and half of the primary key, so a client-chosen key value can never collide across tenants (arch tenant-key-scope sweep).';
COMMENT ON COLUMN setting.key IS
    'Must name an entry in domain.SettingDefinitions; enforced by domain.Setting.Validate before the write reaches this table, not by a schema CHECK, which would duplicate the in-code registry.';

-- ---------------------------------------------------------------------------
-- Row-level security (ADR-TENANCY-001, mirroring 20260810000001's team policy
-- and 20260811000001's notification policy).
--
-- current_setting('app.tenant_id', true) returns NULL when unset, and NULL
-- matches no row: fail-closed by construction. USING governs which rows a read
-- may see; WITH CHECK governs which rows a write may create, so a tenant
-- cannot insert a setting labelled with another tenant's id.
ALTER TABLE setting ENABLE ROW LEVEL SECURITY;
ALTER TABLE setting FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON setting;
CREATE POLICY tenant_isolation ON setting
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
