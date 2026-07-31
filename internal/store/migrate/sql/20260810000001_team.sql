-- OPS-S040 Team — grouping Users within an Organisation.
--
-- team and team_membership are TENANT-SCOPED, exactly as membership and
-- invitation are (20260804000004). tenant_id is denormalised onto each row and
-- is the RLS key, even though it is derivable from org_id / team_id: a policy
-- that had to join organization or team to reach the key would make that
-- mapping a dependency of every isolation decision (ADR-IDENTITY-002 §6,
-- applied here).
--
-- DEFAULT 'system' follows 20260728000001's recorded reason: a write path that
-- is ever missed lands in the system tenant, where it is greppable evidence of
-- the gap, rather than failing with a not-null violation that surfaces as a
-- 500.
--
-- TEAM IS NOT AN AUTHORISATION FACT. Membership alone answers "does this user
-- belong to this organisation" (ADR-IDENTITY-002 §2.3). A Team only groups
-- users who are already members, so team_membership carries no status: adding
-- and removing a member are a real INSERT and a real DELETE, not a lifecycle
-- transition. Team itself keeps a lifecycle (active/archived) because it is a
-- named, governed object in its own right, like Organization — archived rather
-- than deleted so a row nothing else has to stop pointing at survives
-- (ADR-IDENTITY-001 §8.3 applied to Team).
--
-- NO ADMINISTRATIVE AUDIT. ADR-AUDIT-007 §6.2 scopes the administrative audit
-- chokepoint (withAdminAudit) to exactly five named tables — app_user,
-- organization, membership, invitation, tenant — the identity-governance facts
-- a bearer token's tenant boundary is resolved from. §9 of that ADR treats
-- widening §6.2 as "an amendment to this ADR's scope table", not a decision a
-- new story makes unilaterally. Team is organisational structure, not an
-- identity-governance fact, so it follows the same pattern as webhook and
-- policy: tenant-owned and under row-level security, without the
-- administrative chokepoint.

CREATE TABLE IF NOT EXISTS team (
    team_id       text        PRIMARY KEY,
    tenant_id     text        NOT NULL DEFAULT 'system' REFERENCES tenant (tenant_id),
    org_id        text        NOT NULL REFERENCES organization (org_id),
    name          text        NOT NULL,
    status        text        NOT NULL DEFAULT 'active',
    row_version   bigint      NOT NULL DEFAULT 1,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ck_team_status      CHECK (status IN ('active', 'archived')),
    CONSTRAINT ck_team_name_length CHECK (char_length(name) <= 200)
);

CREATE TABLE IF NOT EXISTS team_membership (
    team_membership_id text        PRIMARY KEY,
    tenant_id           text        NOT NULL DEFAULT 'system' REFERENCES tenant (tenant_id),
    team_id             text        NOT NULL REFERENCES team (team_id),
    user_id             text        NOT NULL REFERENCES app_user (user_id),
    row_version         bigint      NOT NULL DEFAULT 1,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_team_membership_team_user UNIQUE (team_id, user_id)
);

-- ix_team_membership_user is the mirror of ix_membership_user: it is the path
-- "which teams is this user on", read whenever a console or a permission check
-- needs a user's team memberships.
CREATE INDEX IF NOT EXISTS ix_team_tenant            ON team (tenant_id);
CREATE INDEX IF NOT EXISTS ix_team_org               ON team (org_id, status);
CREATE INDEX IF NOT EXISTS ix_team_membership_tenant ON team_membership (tenant_id);
CREATE INDEX IF NOT EXISTS ix_team_membership_team   ON team_membership (team_id);
CREATE INDEX IF NOT EXISTS ix_team_membership_user   ON team_membership (user_id);

COMMENT ON TABLE team IS
    'Groups Users within an Organisation. Tenant-scoped: tenant_id is the RLS key, denormalised from the organisation (ADR-IDENTITY-002 §6 applied to Team).';
COMMENT ON COLUMN team.tenant_id IS
    'Denormalised from the organisation and NOT NULL. This is the row''s owner and the RLS predicate; deriving it by join at policy-evaluation time would make organization a dependency of every isolation decision.';
COMMENT ON TABLE team_membership IS
    'Joins a User to a Team. NOT an authorisation fact — Membership alone grants access to an organisation''s boundary (ADR-IDENTITY-002 §2.3) — so this table carries no status: removing a member is a real DELETE.';

-- ---------------------------------------------------------------------------
-- Row-level security (ADR-IDENTITY-002 §4, mirroring 20260804000005).
--
-- current_setting('app.tenant_id', true) returns NULL when unset, and NULL
-- matches no row: the policy is fail-closed by construction, exactly as every
-- other tenant_isolation policy in this schema is. USING governs which rows a
-- read may see; WITH CHECK governs which rows a write may create, so a tenant
-- cannot insert a team (or a team membership) labelled with another tenant's
-- id and thereby grant itself access to that boundary.
DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'team',
        'team_membership'
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
