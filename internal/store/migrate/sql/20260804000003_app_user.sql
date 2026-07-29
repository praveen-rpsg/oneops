-- A user is a person-shaped Identity, global to the platform (ADR-IDENTITY-001
-- §8.2, ADR-IDENTITY-002 §2.2).
--
-- This table deliberately carries NO tenant_id and is NOT in TenantOwnedTables.
-- A person exists before, and independently of, any membership: the same human
-- may belong to several organisations, and their email is unique across the
-- platform rather than within a boundary. Membership — which is tenant-scoped —
-- is what joins a user to an organisation, and tenant_id remains the single
-- authoritative ownership key for every row that is owned at all
-- (ADR-IDENTITY-001 §4.2).
--
-- Isolation is not weakened by that. The table holds an id, an email, a display
-- name and a status — no tenant data — and it is reachable only through the
-- privileged pool, where every privileged mutation is already guarded. The
-- deeper reason it cannot be tenant-scoped is resolution order: tenant_id is
-- discovered *by* reading a principal's membership, so a table needed to
-- discover the key cannot itself be gated on the key (ADR-IDENTITY-002 §3.1).
--
-- The table is named `app_user`, not `user`: USER is a reserved word in the SQL
-- standard and in PostgreSQL (pg_get_keywords catcode 'R'), so `CREATE TABLE
-- user` is a syntax error and every reference would need quoting forever. The
-- domain entity remains User (ADR-IDENTITY-002 §1).
--
-- email is citext so that case-insensitive equality lives in the type rather
-- than in every query that touches it. A LOWER() index plus discipline gives the
-- same result only for as long as the discipline holds; one forgotten call site
-- is a duplicate account.
--
-- The extension is pinned to `public` and the type is referenced schema-qualified
-- as `public.citext`. An extension is a database-wide object installed into ONE
-- schema — by default whichever is first in search_path — and its types are only
-- resolvable from schemas that can see it. Left unpinned, this migration
-- installed citext into whichever schema happened to run it first, and every
-- connection using a different search_path then failed with
-- `type "citext" does not exist`. Pinning makes the column definition independent
-- of the search_path of whoever applies the migration.

CREATE EXTENSION IF NOT EXISTS citext WITH SCHEMA public;

CREATE TABLE IF NOT EXISTS app_user (
    user_id      text          PRIMARY KEY,
    email        public.citext NOT NULL,
    display_name text        NOT NULL DEFAULT '',
    status       text        NOT NULL DEFAULT 'invited',
    row_version  bigint      NOT NULL DEFAULT 1,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_user_email  UNIQUE (email),
    CONSTRAINT ck_user_status CHECK (status IN ('invited', 'active', 'suspended', 'deactivated'))
);

COMMENT ON TABLE app_user IS
    'A person-shaped Identity, global to the platform. Carries no tenant_id by decision (ADR-IDENTITY-001 §8.2); membership is what scopes a user to an organisation. Named app_user because USER is a reserved word.';

COMMENT ON COLUMN app_user.email IS
    'citext: case-insensitive equality is a property of the type, so no call site can reintroduce a duplicate by forgetting to fold case. Unique platform-wide, not per organisation.';

COMMENT ON COLUMN app_user.status IS
    'Lifecycle per ADR-IDENTITY-001 §8.3: invited -> active -> suspended -> deactivated. Deactivation revokes every membership; this row survives so audit events retain an attributable author.';
