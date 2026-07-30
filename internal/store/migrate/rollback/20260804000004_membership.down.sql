-- Reverses 20260804000004_membership.
--
-- Must run BEFORE the organization (20260804000001) and app_user
-- (20260804000003) rollbacks: both tables carry foreign keys to those, and
-- PostgreSQL will refuse to drop a referenced table — which is the intended
-- protection rather than a silent cascade.
--
-- Dropping the tables drops their row-level-security policies with them, so
-- 20260804000005 needs no separate reversal.

DROP TABLE IF EXISTS invitation;
DROP TABLE IF EXISTS membership;
