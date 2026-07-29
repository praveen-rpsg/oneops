-- An Organisation is a scope of Identity; a Tenant is the realisation of that
-- scope as an isolation boundary (ADR-IDENTITY-001 §4, ADR-IDENTITY-002 §2.1).
--
-- The two stand 1:1, and uq_org_tenant is what enforces it. That constraint is
-- the substance of this migration: without it the model degrades to "an
-- organisation has some tenants", which is a different decision with different
-- consequences for every query that resolves ownership.
--
-- The seam to 1:N is present but closed. Widening later means dropping one
-- constraint rather than re-keying the twelve tables that carry tenant_id —
-- which is the whole reason the two entities are separate rows instead of one.
--
-- organization is GLOBAL: no tenant_id column, not in TenantOwnedTables, no RLS
-- (ADR-IDENTITY-002 §3). Its tenant_id is a *pointer to* the boundary, not the
-- row's own owner, and treating it as an ownership key would make the table
-- unreadable at exactly the moment it is needed — tenant_id is discovered *by*
-- reading this mapping, so gating the mapping on the key is circular
-- (ADR-IDENTITY-002 §3.1).
--
-- ck_org_slug is deliberately identical to ck_tenant_slug in 20260727000001. The
-- backfill copies tenant slugs verbatim, so a laxer rule here would admit an
-- organisation slug that its own tenant could not have held.
--
-- No separate ix_org_tenant index is created. ADR-IDENTITY-002 §6.2 lists one
-- for the org -> tenant resolution path, but uq_org_tenant already creates a
-- unique btree index on that column and serves the lookup completely. A second
-- index on the same column would be written on every insert and read by nothing.

CREATE TABLE IF NOT EXISTS organization (
    org_id      text        PRIMARY KEY,
    tenant_id   text        NOT NULL REFERENCES tenant (tenant_id),
    slug        text        NOT NULL,
    name        text        NOT NULL,
    status      text        NOT NULL DEFAULT 'active',
    row_version bigint      NOT NULL DEFAULT 1,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_org_tenant UNIQUE (tenant_id),
    CONSTRAINT uq_org_slug   UNIQUE (slug),
    CONSTRAINT ck_org_status CHECK (status IN ('active', 'suspended')),
    CONSTRAINT ck_org_slug   CHECK (slug ~ '^[a-z0-9][a-z0-9-]{1,62}$')
);

COMMENT ON TABLE organization IS
    'A scope of Identity to which Policy attaches (Vol IV Part 9 C6). Global: carries no tenant_id of its own and is not tenant-owned. Stands 1:1 with tenant, enforced by uq_org_tenant.';

COMMENT ON COLUMN organization.tenant_id IS
    'The tenant realising this organisation''s isolation boundary. A pointer to the boundary, never this row''s owner — organization is global (ADR-IDENTITY-002 §3.1).';

COMMENT ON CONSTRAINT uq_org_tenant ON organization IS
    'Enforces the 1:1 of ADR-IDENTITY-001 §4. Dropping it widens the model to one organisation holding many tenants, which is an architectural change and not a schema tidy-up.';
