-- M2.1 Dependency Graph — edge persistence (additive; does not touch M1 tables).
-- Storage layer only: no traversal, no authority computation (later stories).

CREATE TABLE IF NOT EXISTS dependency_edge (
    id         text        PRIMARY KEY,
    from_cfg   text        NOT NULL REFERENCES configuration_object (cfg_id) ON DELETE CASCADE,
    to_cfg     text        NOT NULL REFERENCES configuration_object (cfg_id) ON DELETE CASCADE,
    edge_kind  text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_edge_from_to_kind UNIQUE (from_cfg, to_cfg, edge_kind),
    CONSTRAINT ck_edge_kind CHECK (edge_kind IN ('depends','extends','supersedes'))
);

CREATE INDEX IF NOT EXISTS ix_edge_from    ON dependency_edge (from_cfg);
CREATE INDEX IF NOT EXISTS ix_edge_to      ON dependency_edge (to_cfg);
CREATE INDEX IF NOT EXISTS ix_edge_from_to ON dependency_edge (from_cfg, to_cfg);
