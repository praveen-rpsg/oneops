-- Extractor fixture: a miniature migration, held as data.
--
-- This file exists to prove the safe channel. It names §6.2 and audit tables
-- freely, because it is not Go source: the architecture guards read Go, and a
-- .sql data file is invisible to them. Any extractor test needing schema input
-- takes it from here rather than from a Go string literal.
--
-- Shapes mirror internal/store/migrate/sql so a parser exercised against this
-- fixture is exercised against something the real corpus also contains.

CREATE TABLE IF NOT EXISTS app_user (
    id          text PRIMARY KEY,
    email       text NOT NULL UNIQUE,
    status      text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS organization (
    id      text PRIMARY KEY,
    slug    text NOT NULL UNIQUE,
    name    text NOT NULL
);

CREATE TABLE IF NOT EXISTS membership (
    id       text PRIMARY KEY,
    user_id  text NOT NULL REFERENCES app_user (id),
    org_id   text NOT NULL REFERENCES organization (id),
    role     text NOT NULL
);

CREATE TABLE IF NOT EXISTS admin_audit_event (
    chain_id     text NOT NULL,
    seq          bigint NOT NULL,
    operation    text NOT NULL,
    row_hash     bytea NOT NULL,
    occurred_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (chain_id, seq)
);

CREATE INDEX IF NOT EXISTS admin_audit_event_occurred_idx
    ON admin_audit_event (occurred_at DESC, chain_id, seq);

CREATE TRIGGER admin_audit_event_no_update
    BEFORE UPDATE OR DELETE ON admin_audit_event
    FOR EACH ROW EXECUTE FUNCTION refuse_mutation();
