-- Reverses 20260804000003_app_user.
--
-- Dropping the table removes every user record. It is safe only while nothing
-- references it: membership and invitation carry foreign keys to app_user
-- (ADR-IDENTITY-002 §2.3, §2.4), so this must not run while those tables exist
-- with rows — PostgreSQL will refuse, which is the correct outcome rather than a
-- silent cascade.
--
-- The citext extension is deliberately NOT dropped. It is a database-wide
-- object that another table may have started using, and dropping an extension
-- another migration depends on turns a reversible step into an outage. Leaving
-- it costs nothing: CREATE EXTENSION IF NOT EXISTS is idempotent on re-apply.

DROP TABLE IF EXISTS app_user;
