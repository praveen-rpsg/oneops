-- Tenant registry.
--
-- The platform previously parsed a `tenant` claim from the bearer token and
-- discarded it: no table carried a tenant, and no query filtered on one. Adding
-- a tenant_id column sourced from an unvalidated claim would not have been
-- isolation — any token bearing any string would have minted a silo that could
-- not be enumerated, suspended, or revoked. The registry has to exist before
-- anything can reference it, so this migration establishes the entity and the
-- next one adds the column and the row-level policies to every governed table.
--
-- tenant_id is the platform's own identifier. external_id is the identifier the
-- identity provider asserts (Entra `tid`, Okta org id, and so on); keeping them
-- distinct means the IdP can be replaced without rewriting every foreign key,
-- and means a tenant can exist before an IdP is connected to it.

CREATE TABLE IF NOT EXISTS tenant (
    tenant_id   text        PRIMARY KEY,
    slug        text        NOT NULL,
    name        text        NOT NULL,
    external_id text        NOT NULL DEFAULT '',
    status      text        NOT NULL DEFAULT 'active',
    row_version bigint      NOT NULL DEFAULT 1,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ck_tenant_status CHECK (status IN ('active', 'suspended')),
    CONSTRAINT ck_tenant_slug   CHECK (slug ~ '^[a-z0-9][a-z0-9-]{1,62}$')
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_tenant_slug ON tenant (slug);

-- A partial unique index rather than a plain UNIQUE: external_id is empty for
-- tenants not yet bound to an identity provider, and many such tenants may
-- coexist. Only non-empty external ids must be unique, because that is the
-- value a bearer token is resolved against.
CREATE UNIQUE INDEX IF NOT EXISTS uq_tenant_external
    ON tenant (external_id) WHERE external_id <> '';

-- The system tenant owns every row that predates tenancy. It is seeded here so
-- the backfill in the following migration has a valid foreign-key target, and
-- so a single-tenant deployment keeps working with no configuration change.
INSERT INTO tenant (tenant_id, slug, name, external_id)
VALUES ('system', 'system', 'System', 'system')
ON CONFLICT (tenant_id) DO NOTHING;
