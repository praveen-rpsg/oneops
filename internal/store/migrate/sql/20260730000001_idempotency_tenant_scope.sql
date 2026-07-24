-- Scope idempotency keys to the tenant that supplied them.
--
-- `id` is the client's Idempotency-Key header, and it was a GLOBAL primary key
-- on a tenant-scoped table. Uniqueness is enforced beneath row-level security:
-- RLS filters which rows a connection may see, and says nothing about which
-- rows a constraint considers. So one tenant's key silently occupied the whole
-- cluster's key space.
--
-- The exploit, verified against the running service:
--
--   1. tenant A POSTs with Idempotency-Key "K"       -> 201, row stored for A
--   2. tenant B POSTs with the same "K"              -> 201, but B's Save hits
--                                                       ON CONFLICT (id) DO
--                                                       NOTHING and is silently
--                                                       discarded — A's row wins
--   3. tenant B retries with "K", as a client does
--      after a network failure                       -> Lookup finds nothing
--                                                       (RLS hides A's row), so
--                                                       the write is replayed:
--                                                       HTTP 409 instead of the
--                                                       idempotent 201
--
-- No disclosure: RLS correctly stopped B reading A's stored response body. The
-- damage is to integrity and availability. Idempotency exists precisely so a
-- client may retry safely, and any tenant could disable it for any other by
-- claiming a key first. Keys are frequently semantic and guessable
-- ("import-2026-07-24", "batch-1"), so this is choosable, not accidental.
-- Where the resource has no unique constraint of its own, the same pattern
-- produces duplicate resources rather than a 409.
--
-- Making tenant_id the leading key column restores the property: the key space
-- is per tenant, ON CONFLICT is evaluated per tenant, and a colliding key
-- across tenants is no longer observable in either direction.

ALTER TABLE idempotency_key DROP CONSTRAINT IF EXISTS idempotency_key_pkey;
ALTER TABLE idempotency_key
    ADD CONSTRAINT idempotency_key_pkey PRIMARY KEY (tenant_id, id);
