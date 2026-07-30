-- Membership joins a User to an Organisation; Invitation is how a membership
-- begins (ADR-IDENTITY-002 §2.3, §2.4).
--
-- Both are TENANT-SCOPED. tenant_id is denormalised onto each row and is the RLS
-- key, even though it is derivable from org_id. A policy that had to join
-- organization to reach the key would be slower and, worse, would make the
-- mapping table a dependency of every isolation decision — a second path to
-- ownership. The column is the key; the foreign key to organization is the
-- relationship (ADR-IDENTITY-002 §6, §2.3).
--
-- DEFAULT 'system' follows 20260728000001 and its recorded reason: a write path
-- that is ever missed lands in the system tenant, where it is greppable
-- evidence of the gap, rather than failing with a not-null violation that
-- surfaces as a 500.
--
-- Membership is the authorisation fact. tenant_id is resolved by reading it, and
-- never taken from the request (ADR-IDENTITY-002 §4.5) — a tenant identifier in
-- a header or body is a claim, this row is the fact. That is ADR-TENANCY-004
-- applied to identity.
--
-- Revocation is a status change, not a delete: the audit chain must outlive the
-- membership that authorised the acts it records (ADR-IDENTITY-001 §8.3).
--
-- invitation.token_hash stores a hash, never the token. The plaintext is
-- returned once at issuance and is unrecoverable afterwards — an invitation
-- token is a bearer credential and gets the same treatment as an API key
-- (ADR-IDENTITY-002 §2.4).
--
-- NOTE for the work-queue guard: invitation.status defaults to 'pending', which
-- is exactly the signature TestEveryWorkQueue_HasAFencingToken uses to discover
-- claimed work from the schema. invitation is not claimed work — nothing leases
-- it, no worker competes for it, and it needs no fencing token. It is justified
-- in that guard's exclusion map rather than renamed to evade detection
-- (ADR-IDENTITY-002 §7, C-4).

CREATE TABLE IF NOT EXISTS membership (
    membership_id text        PRIMARY KEY,
    tenant_id     text        NOT NULL DEFAULT 'system' REFERENCES tenant (tenant_id),
    org_id        text        NOT NULL REFERENCES organization (org_id),
    user_id       text        NOT NULL REFERENCES app_user (user_id),
    status        text        NOT NULL DEFAULT 'active',
    row_version   bigint      NOT NULL DEFAULT 1,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_membership_org_user UNIQUE (org_id, user_id),
    CONSTRAINT ck_membership_status   CHECK (status IN ('active', 'revoked'))
);

CREATE TABLE IF NOT EXISTS invitation (
    invitation_id text          PRIMARY KEY,
    tenant_id     text          NOT NULL DEFAULT 'system' REFERENCES tenant (tenant_id),
    org_id        text          NOT NULL REFERENCES organization (org_id),
    email         public.citext NOT NULL,
    token_hash    text          NOT NULL,
    status        text          NOT NULL DEFAULT 'pending',
    expires_at    timestamptz   NOT NULL,
    created_at    timestamptz   NOT NULL DEFAULT now(),
    redeemed_at   timestamptz,
    CONSTRAINT uq_invitation_token  UNIQUE (token_hash),
    CONSTRAINT ck_invitation_status CHECK (status IN ('pending', 'redeemed', 'revoked', 'expired'))
);

-- ix_membership_user is the hot one: it is read on every authenticated request
-- that resolves a tenant, so without it a sequential scan sits in front of the
-- whole API and the < 5ms p95 budget is spent before authorisation begins.
CREATE INDEX IF NOT EXISTS ix_membership_user   ON membership (user_id, status);
CREATE INDEX IF NOT EXISTS ix_membership_tenant ON membership (tenant_id);
CREATE INDEX IF NOT EXISTS ix_membership_org    ON membership (org_id, status);
CREATE INDEX IF NOT EXISTS ix_invitation_email  ON invitation (email, status);
CREATE INDEX IF NOT EXISTS ix_invitation_tenant ON invitation (tenant_id);

COMMENT ON TABLE membership IS
    'Joins a User to an Organisation. Tenant-scoped: tenant_id is the RLS key and the authorisation fact from which a request''s boundary is resolved (ADR-IDENTITY-002 §4.5).';

COMMENT ON COLUMN membership.tenant_id IS
    'Denormalised from the organisation and NOT NULL. This is the row''s owner and the RLS predicate; deriving it by join at policy-evaluation time would make organization a dependency of every isolation decision.';

COMMENT ON COLUMN invitation.token_hash IS
    'Hash of the invitation token. The plaintext is shown once at issuance and is not recoverable from this table — the token is a bearer credential.';

-- ⚠ citext case-insensitivity depends on search_path, and fails SILENTLY.
--
-- The type is schema-qualified in the DDL above, so the column is always citext.
-- Its `=` OPERATOR, however, lives in public and is resolved through search_path
-- — and an operator cannot be schema-qualified in ordinary syntax. With public
-- absent from search_path, PostgreSQL falls back to the implicit citext->text
-- coercion and compares with text's `=`, which is case-sensitive. No error is
-- raised. Measured on a connection whose search_path excluded public:
--
--     stored 'Invitee@Example.COM', queried 'invitee@example.com'
--       email = 'invitee@example.com'              -> 0 rows
--       email = $1                                 -> 0 rows
--       email = $1::public.citext                  -> 0 rows
--       lower(email::text) = lower($1)             -> 1 row
--
-- UNIQUENESS is unaffected: the index compares citext to citext internally and
-- does not go through operator resolution, so a differently cased duplicate is
-- still rejected. It is LOOKUPS that degrade.
--
-- The control-plane sets no search_path, so it gets the default ("$user",
-- public) and behaves correctly. Any deployment that pins an explicit
-- search_path without public silently loses case-insensitive lookup on every
-- email column — including app_user.email in 20260804000003, which is already
-- applied and cannot carry this note itself.
COMMENT ON COLUMN invitation.email IS
    'Case-insensitive (public.citext). Uniqueness holds regardless of search_path; LOOKUPS DO NOT — with public absent from search_path the comparison silently degrades to case-sensitive text equality. Keep public in search_path, or compare with lower(email::text).';

COMMENT ON COLUMN invitation.status IS
    'Lifecycle: pending -> redeemed | revoked | expired. The pending default resembles a work queue to the schema-derived queue guard; invitation is not claimed work and is justified there rather than renamed.';
