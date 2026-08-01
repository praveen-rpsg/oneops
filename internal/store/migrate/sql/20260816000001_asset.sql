-- OPS-CMDB-001 Asset (Configuration Item) — the CMDB bedrock every downstream
-- NOC/SOC/ITSM capability (monitoring, alerting, incidents, tickets, service
-- catalog) will reference.
--
-- asset and asset_relationship are TENANT-SCOPED, exactly as team and
-- team_membership are (20260810000001). tenant_id is denormalised onto each
-- row and is the RLS key (ADR-IDENTITY-002 §6, applied here).
--
-- DEFAULT 'system' follows 20260728000001's recorded reason: a write path
-- that is ever missed lands in the system tenant, where it is greppable
-- evidence of the gap, rather than failing with a not-null violation that
-- surfaces as a 500.
--
-- ASSET IS NOT configuration_object. configuration_object is a governance
-- artifact under the Configuration State Model's constitutional lifecycle
-- (Authority, Retention, §8 operations). Asset is operational data — a
-- typed, tenant-owned Configuration Item — and carries none of that
-- (ADR-ASSET-001 §2). Overloading configuration_object for this would make
-- every future monitoring/incident/ticket record a constitutional object,
-- which it is not.
--
-- One ADD COLUMN per statement is not needed here (these are fresh CREATE
-- TABLE statements), but every later ALTER on these tables must still follow
-- that rule: the derived-schema extractor (internal/kg/extract/schema)
-- captures only the first ADD of a multi-clause ALTER.

CREATE TABLE IF NOT EXISTS asset (
    asset_id      text        PRIMARY KEY,
    tenant_id     text        NOT NULL DEFAULT 'system' REFERENCES tenant (tenant_id),
    type          text        NOT NULL,
    name          text        NOT NULL,
    attributes    jsonb       NOT NULL DEFAULT '{}',
    status        text        NOT NULL DEFAULT 'active',
    row_version   bigint      NOT NULL DEFAULT 1,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ck_asset_status      CHECK (status IN ('active', 'retired')),
    CONSTRAINT ck_asset_name_length CHECK (char_length(name) <= 200),
    CONSTRAINT ck_asset_type_length CHECK (char_length(type) <= 100)
);

-- The CMDB graph. Relationship endpoints ON DELETE CASCADE: deleting an
-- asset removes the edges naming it rather than leaving a dangling
-- reference — the graph engine's recursive traversal (internal/graph,
-- reused unchanged from the configuration-object dependency graph) assumes
-- every edge it walks resolves to a live node.
--
-- type is a CLOSED set (unlike asset.type): it is the vocabulary the
-- traversal engine and downstream monitoring/incident correlation reason
-- over, so extending it is a deliberate change to this migration, not a
-- value a caller happens to send (ADR-ASSET-001 §4).
CREATE TABLE IF NOT EXISTS asset_relationship (
    relationship_id text        PRIMARY KEY,
    tenant_id       text        NOT NULL DEFAULT 'system' REFERENCES tenant (tenant_id),
    from_asset_id   text        NOT NULL REFERENCES asset (asset_id) ON DELETE CASCADE,
    to_asset_id     text        NOT NULL REFERENCES asset (asset_id) ON DELETE CASCADE,
    type            text        NOT NULL,
    row_version     bigint      NOT NULL DEFAULT 1,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ck_asset_relationship_no_self CHECK (from_asset_id <> to_asset_id),
    CONSTRAINT uq_asset_relationship         UNIQUE (from_asset_id, to_asset_id, type),
    CONSTRAINT ck_asset_relationship_type CHECK (type IN ('depends_on', 'runs_on', 'connected_to', 'member_of'))
);

CREATE INDEX IF NOT EXISTS ix_asset_tenant             ON asset (tenant_id);
CREATE INDEX IF NOT EXISTS ix_asset_tenant_status      ON asset (tenant_id, status);
CREATE INDEX IF NOT EXISTS ix_asset_relationship_tenant ON asset_relationship (tenant_id);
-- The traversal engine's recursive CTE (internal/store/postgres, the same
-- shape graph_traversal_repo.go already uses for dependency_edge) walks
-- from_asset_id and to_asset_id directly, one hop at a time, so both
-- endpoints are indexed on their own — the same pair dependency_edge carries
-- (ix_edge_from / ix_edge_to).
CREATE INDEX IF NOT EXISTS ix_asset_relationship_from ON asset_relationship (from_asset_id);
CREATE INDEX IF NOT EXISTS ix_asset_relationship_to   ON asset_relationship (to_asset_id);

COMMENT ON TABLE asset IS
    'A Configuration Item: the operational unit every downstream NOC/SOC/ITSM capability references. Tenant-scoped; distinct from configuration_object, which is a governance artifact (ADR-ASSET-001 §2).';
COMMENT ON COLUMN asset.tenant_id IS
    'The RLS key. NOT NULL and denormalised, the same reason team.tenant_id is (ADR-IDENTITY-002 §6).';
COMMENT ON TABLE asset_relationship IS
    'A directed, typed edge of the CMDB graph between two Assets. Traversed by the same dependency-graph engine (internal/graph) the configuration-object graph uses (ADR-ASSET-001 §4).';

-- ---------------------------------------------------------------------------
-- Row-level security (ADR-IDENTITY-002 §4, mirroring 20260810000001).
--
-- current_setting('app.tenant_id', true) returns NULL when unset, and NULL
-- matches no row: the policy is fail-closed by construction. USING governs
-- which rows a read may see; WITH CHECK governs which rows a write may
-- create, so a tenant cannot insert an asset or a relationship labelled with
-- another tenant's id and thereby grant itself access to that boundary.
DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'asset',
        'asset_relationship'
    ]
    LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);

        EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', t);
        EXECUTE format(
            'CREATE POLICY tenant_isolation ON %I '
            'USING (tenant_id = current_setting(''app.tenant_id'', true)) '
            'WITH CHECK (tenant_id = current_setting(''app.tenant_id'', true))', t);
    END LOOP;
END;
$$;
