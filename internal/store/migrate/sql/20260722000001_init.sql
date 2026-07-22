-- M1 Configuration Registry — initial schema.
-- Authority is a maintained projection (M3 Authority Resolver); stored here.

CREATE TABLE IF NOT EXISTS configuration_object (
    cfg_id           text        PRIMARY KEY,
    artifact         text        NOT NULL,
    version          text        NOT NULL,
    role             text        NOT NULL,
    lifecycle        text        NOT NULL,
    retention_class  text        NOT NULL,
    authority        text        NOT NULL DEFAULT 'non_normative',
    ratified_by      text        NOT NULL DEFAULT '',
    review_cycle     text        NOT NULL DEFAULT '',
    retention_policy text        NOT NULL DEFAULT 'permanent',
    row_version      bigint      NOT NULL DEFAULT 1,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_cfg_artifact_version UNIQUE (artifact, version),
    CONSTRAINT ck_cfg_role CHECK (role IN
        ('constitution','governance','eng_spec','validation','evidence','audit','planning','reference','working')),
    CONSTRAINT ck_cfg_lifecycle CHECK (lifecycle IN
        ('draft','in_review','ratified','approved','in_progress','complete','suspended','deprecated','withdrawn')),
    CONSTRAINT ck_cfg_authority CHECK (authority IN
        ('active','historical','non_normative')),
    CONSTRAINT ck_cfg_retention CHECK (retention_class IN
        ('current_baseline','current_planning','historical_record','superseded_planning',
         'historical_evidence','audit_record','working_material'))
);

CREATE INDEX IF NOT EXISTS ix_cfg_authority      ON configuration_object (authority);
CREATE INDEX IF NOT EXISTS ix_cfg_role_lifecycle ON configuration_object (role, lifecycle);
CREATE INDEX IF NOT EXISTS ix_cfg_keyset         ON configuration_object (created_at, cfg_id);
CREATE INDEX IF NOT EXISTS ix_cfg_artifact_lower ON configuration_object (lower(artifact));

CREATE TABLE IF NOT EXISTS artifact_version (
    cfg_id       text        NOT NULL REFERENCES configuration_object (cfg_id) ON DELETE CASCADE,
    version      text        NOT NULL,
    body_uri     text        NOT NULL,
    content_hash text        NOT NULL,
    format       text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (cfg_id, version),
    CONSTRAINT ck_av_format CHECK (format IN ('md','mdx','json'))
);

CREATE TABLE IF NOT EXISTS configuration_metadata (
    cfg_id text NOT NULL REFERENCES configuration_object (cfg_id) ON DELETE CASCADE,
    key    text NOT NULL,
    value  text NOT NULL,
    PRIMARY KEY (cfg_id, key)
);

CREATE INDEX IF NOT EXISTS ix_meta_value ON configuration_metadata (lower(value));

CREATE TABLE IF NOT EXISTS idempotency_key (
    id            text        PRIMARY KEY,
    method        text        NOT NULL,
    path          text        NOT NULL,
    status        int         NOT NULL,
    response_body bytea       NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);
