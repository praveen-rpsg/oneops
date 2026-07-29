-- Tenant ownership on every tenant-owned table (ADR-TENANCY-001, step 1).
--
-- Each column is added with DEFAULT 'system' so existing rows are backfilled in
-- the same statement, then constrained NOT NULL and pointed at the tenant
-- registry. The foreign key is the substance of this migration: it makes it
-- impossible for a row to be owned by a tenant that does not exist.
--
-- The DEFAULT is retained rather than dropped. Every write path is threaded
-- with a tenant, but a path that is ever missed must land in the system tenant
-- rather than fail with a not-null violation surfacing as a 500 — and a row
-- visibly owned by `system` is greppable evidence of the gap, where a NULL
-- would silently disappear from every tenant-scoped query.
--
-- Row-level security policies and the role split are deliberately NOT in this
-- migration. Enabling forced RLS before the application sets `app.tenant_id`
-- would take the platform down; the policies land with the pool split that
-- makes them safe.
--
-- Excluded by ADR-TENANCY-001 §4: schema_migrations, tenant, webhook_cursor and
-- policy_cursor (platform processing state, not customer data).

-- Configuration registry -----------------------------------------------------

ALTER TABLE configuration_object
    ADD COLUMN IF NOT EXISTS tenant_id text NOT NULL DEFAULT 'system';
ALTER TABLE configuration_metadata
    ADD COLUMN IF NOT EXISTS tenant_id text NOT NULL DEFAULT 'system';
ALTER TABLE artifact_version
    ADD COLUMN IF NOT EXISTS tenant_id text NOT NULL DEFAULT 'system';
ALTER TABLE dependency_edge
    ADD COLUMN IF NOT EXISTS tenant_id text NOT NULL DEFAULT 'system';

-- An idempotency key presented by one tenant must never replay another
-- tenant's stored response.
ALTER TABLE idempotency_key
    ADD COLUMN IF NOT EXISTS tenant_id text NOT NULL DEFAULT 'system';

-- Audit --------------------------------------------------------------------
--
-- audit_event is LIST-partitioned by chain_id; ADD COLUMN on the parent
-- propagates to every partition. The append-only triggers are untouched:
-- they govern mutation, RLS governs visibility, and the two are independent.

ALTER TABLE audit_event
    ADD COLUMN IF NOT EXISTS tenant_id text NOT NULL DEFAULT 'system';
ALTER TABLE audit_chain_head
    ADD COLUMN IF NOT EXISTS tenant_id text NOT NULL DEFAULT 'system';

-- Event delivery -------------------------------------------------------------

ALTER TABLE webhook
    ADD COLUMN IF NOT EXISTS tenant_id text NOT NULL DEFAULT 'system';
ALTER TABLE webhook_delivery
    ADD COLUMN IF NOT EXISTS tenant_id text NOT NULL DEFAULT 'system';
ALTER TABLE webhook_replay_job
    ADD COLUMN IF NOT EXISTS tenant_id text NOT NULL DEFAULT 'system';

-- Policy automation ----------------------------------------------------------

ALTER TABLE policy
    ADD COLUMN IF NOT EXISTS tenant_id text NOT NULL DEFAULT 'system';
ALTER TABLE policy_execution
    ADD COLUMN IF NOT EXISTS tenant_id text NOT NULL DEFAULT 'system';

-- Referential integrity ------------------------------------------------------
--
-- Added after every column exists and is backfilled, so the constraint is
-- validated against complete data. ON DELETE RESTRICT is implicit and intended:
-- a tenant with rows cannot be deleted, which reinforces that tenants are
-- suspended rather than removed (audit history is append-only and would
-- otherwise be orphaned).

ALTER TABLE configuration_object   ADD CONSTRAINT fk_cfg_tenant        FOREIGN KEY (tenant_id) REFERENCES tenant (tenant_id);
ALTER TABLE configuration_metadata ADD CONSTRAINT fk_meta_tenant       FOREIGN KEY (tenant_id) REFERENCES tenant (tenant_id);
ALTER TABLE artifact_version       ADD CONSTRAINT fk_av_tenant         FOREIGN KEY (tenant_id) REFERENCES tenant (tenant_id);
ALTER TABLE dependency_edge        ADD CONSTRAINT fk_edge_tenant       FOREIGN KEY (tenant_id) REFERENCES tenant (tenant_id);
ALTER TABLE idempotency_key        ADD CONSTRAINT fk_idem_tenant       FOREIGN KEY (tenant_id) REFERENCES tenant (tenant_id);
ALTER TABLE audit_chain_head       ADD CONSTRAINT fk_chain_head_tenant FOREIGN KEY (tenant_id) REFERENCES tenant (tenant_id);
ALTER TABLE webhook                ADD CONSTRAINT fk_webhook_tenant    FOREIGN KEY (tenant_id) REFERENCES tenant (tenant_id);
ALTER TABLE webhook_delivery       ADD CONSTRAINT fk_delivery_tenant   FOREIGN KEY (tenant_id) REFERENCES tenant (tenant_id);
ALTER TABLE webhook_replay_job     ADD CONSTRAINT fk_replay_tenant     FOREIGN KEY (tenant_id) REFERENCES tenant (tenant_id);
ALTER TABLE policy                 ADD CONSTRAINT fk_policy_tenant     FOREIGN KEY (tenant_id) REFERENCES tenant (tenant_id);
ALTER TABLE policy_execution       ADD CONSTRAINT fk_policy_exec_tenant FOREIGN KEY (tenant_id) REFERENCES tenant (tenant_id);

-- audit_event is deliberately absent from the list above. PostgreSQL does not
-- support foreign keys on partitioned tables, so its tenant_id is constrained
-- by the application and by the RLS policy rather than by a reference. The
-- column is still NOT NULL, so a row can never be unowned.

-- Indexes --------------------------------------------------------------------
--
-- Every tenant-scoped query filters on tenant_id first, so it leads each index.
-- These mirror the existing access paths rather than adding new ones.

CREATE INDEX IF NOT EXISTS ix_cfg_tenant            ON configuration_object (tenant_id);
CREATE INDEX IF NOT EXISTS ix_cfg_tenant_keyset     ON configuration_object (tenant_id, created_at, cfg_id);
CREATE INDEX IF NOT EXISTS ix_cfg_tenant_role_lc    ON configuration_object (tenant_id, role, lifecycle);
CREATE INDEX IF NOT EXISTS ix_edge_tenant_from      ON dependency_edge (tenant_id, from_cfg);
CREATE INDEX IF NOT EXISTS ix_edge_tenant_to        ON dependency_edge (tenant_id, to_cfg);
CREATE INDEX IF NOT EXISTS ix_audit_tenant          ON audit_event (tenant_id);
CREATE INDEX IF NOT EXISTS ix_webhook_tenant        ON webhook (tenant_id);
CREATE INDEX IF NOT EXISTS ix_delivery_tenant       ON webhook_delivery (tenant_id);
CREATE INDEX IF NOT EXISTS ix_policy_tenant         ON policy (tenant_id);
CREATE INDEX IF NOT EXISTS ix_policy_exec_tenant    ON policy_execution (tenant_id);

-- Uniqueness becomes per-tenant --------------------------------------------
--
-- uq_cfg_artifact_version was global: two tenants could not both hold an
-- artifact of the same name and version, which would leak one tenant's naming
-- into another's namespace and is a cross-tenant conflict channel.

ALTER TABLE configuration_object DROP CONSTRAINT IF EXISTS uq_cfg_artifact_version;
ALTER TABLE configuration_object
    ADD CONSTRAINT uq_cfg_tenant_artifact_version UNIQUE (tenant_id, artifact, version);
