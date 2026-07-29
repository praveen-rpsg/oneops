-- Reverses 20260728000001_tenant_columns.sql.
--
-- Restores the global artifact/version uniqueness first. This fails if two
-- tenants hold the same artifact and version, which is correct: the rollback
-- cannot proceed without discarding one tenant's data, and that has to be a
-- deliberate operator decision rather than a silent side effect.

ALTER TABLE configuration_object DROP CONSTRAINT IF EXISTS uq_cfg_tenant_artifact_version;
ALTER TABLE configuration_object
    ADD CONSTRAINT uq_cfg_artifact_version UNIQUE (artifact, version);

DROP INDEX IF EXISTS ix_policy_exec_tenant;
DROP INDEX IF EXISTS ix_policy_tenant;
DROP INDEX IF EXISTS ix_delivery_tenant;
DROP INDEX IF EXISTS ix_webhook_tenant;
DROP INDEX IF EXISTS ix_audit_tenant;
DROP INDEX IF EXISTS ix_edge_tenant_to;
DROP INDEX IF EXISTS ix_edge_tenant_from;
DROP INDEX IF EXISTS ix_cfg_tenant_role_lc;
DROP INDEX IF EXISTS ix_cfg_tenant_keyset;
DROP INDEX IF EXISTS ix_cfg_tenant;

ALTER TABLE policy_execution       DROP CONSTRAINT IF EXISTS fk_policy_exec_tenant;
ALTER TABLE policy                 DROP CONSTRAINT IF EXISTS fk_policy_tenant;
ALTER TABLE webhook_replay_job     DROP CONSTRAINT IF EXISTS fk_replay_tenant;
ALTER TABLE webhook_delivery       DROP CONSTRAINT IF EXISTS fk_delivery_tenant;
ALTER TABLE webhook                DROP CONSTRAINT IF EXISTS fk_webhook_tenant;
ALTER TABLE audit_chain_head       DROP CONSTRAINT IF EXISTS fk_chain_head_tenant;
ALTER TABLE idempotency_key        DROP CONSTRAINT IF EXISTS fk_idem_tenant;
ALTER TABLE dependency_edge        DROP CONSTRAINT IF EXISTS fk_edge_tenant;
ALTER TABLE artifact_version       DROP CONSTRAINT IF EXISTS fk_av_tenant;
ALTER TABLE configuration_metadata DROP CONSTRAINT IF EXISTS fk_meta_tenant;
ALTER TABLE configuration_object   DROP CONSTRAINT IF EXISTS fk_cfg_tenant;

ALTER TABLE policy_execution       DROP COLUMN IF EXISTS tenant_id;
ALTER TABLE policy                 DROP COLUMN IF EXISTS tenant_id;
ALTER TABLE webhook_replay_job     DROP COLUMN IF EXISTS tenant_id;
ALTER TABLE webhook_delivery       DROP COLUMN IF EXISTS tenant_id;
ALTER TABLE webhook                DROP COLUMN IF EXISTS tenant_id;
ALTER TABLE audit_chain_head       DROP COLUMN IF EXISTS tenant_id;
ALTER TABLE audit_event            DROP COLUMN IF EXISTS tenant_id;
ALTER TABLE idempotency_key        DROP COLUMN IF EXISTS tenant_id;
ALTER TABLE dependency_edge        DROP COLUMN IF EXISTS tenant_id;
ALTER TABLE artifact_version       DROP COLUMN IF EXISTS tenant_id;
ALTER TABLE configuration_metadata DROP COLUMN IF EXISTS tenant_id;
ALTER TABLE configuration_object   DROP COLUMN IF EXISTS tenant_id;
