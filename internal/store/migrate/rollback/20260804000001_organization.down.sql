-- Reverses 20260804000001_organization.
--
-- Safe only while nothing references organization. membership and invitation
-- carry foreign keys to it (ADR-IDENTITY-002 §2.3, §2.4), so their migration
-- must be rolled back first — PostgreSQL will refuse otherwise, which is the
-- intended protection rather than a silent cascade.
--
-- Dropping the table also drops the backfilled rows from 20260804000002, so that
-- migration needs no separate reversal of its own data.

DROP TABLE IF EXISTS organization;
