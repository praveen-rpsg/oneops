-- OPS-S<E8.4b> Compliance control register + append-only evidence trail —
-- completes E8.4 (continuous-audit automation stays DEFERRED — speculative
-- on a customer-less product, ADR-SOC-009). Mirrors risk (E8.4a,
-- 20260911000001) for the stateful control entity, and incident/
-- incident_event (20260825000001) for the append-only evidence child.
--
-- One DDL statement per line/clause, exactly as every migration in this tree
-- does: the derived-schema extractor (internal/kg/extract/schema) captures
-- only the first ADD of a multi-clause ALTER.
--
-- compliance_control is TENANT-SCOPED, exactly like risk: tenant_id is
-- denormalised onto the row and is the RLS key.
--
-- framework/control_ref together name WHICH control this row tracks (e.g.
-- framework "SOC2", control_ref "CC6.1") and are immutable after Create —
-- see domain.ComplianceControl's own doc comment. UNIQUE(tenant_id,
-- framework, control_ref): a control appears at most once per framework per
-- tenant. framework is deliberately NOT a closed enum — organisations track
-- many frameworks (SOC2, ISO27001, PCI-DSS, HIPAA, ...) and this layer does
-- not get to decide which; the database enforces only length + charset
-- (domain.complianceControlFrameworkPattern is a domain-layer concern, the
-- same split risk.category draws).
CREATE TABLE IF NOT EXISTS compliance_control (
    control_id  text        PRIMARY KEY,
    tenant_id   text        NOT NULL DEFAULT 'system' REFERENCES tenant (tenant_id),
    framework   text        NOT NULL,
    control_ref text        NOT NULL,
    title       text        NOT NULL,
    description text        NOT NULL DEFAULT '',
    status      text        NOT NULL DEFAULT 'not_implemented',
    row_version bigint      NOT NULL DEFAULT 1,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ck_compliance_control_framework_length    CHECK (char_length(framework) BETWEEN 1 AND 100),
    CONSTRAINT ck_compliance_control_ref_length          CHECK (char_length(control_ref) BETWEEN 1 AND 50),
    CONSTRAINT ck_compliance_control_title_length        CHECK (char_length(title) BETWEEN 1 AND 200),
    CONSTRAINT ck_compliance_control_description_length  CHECK (char_length(description) <= 10000),
    CONSTRAINT ck_compliance_control_status              CHECK (status IN ('not_implemented', 'in_progress', 'implemented', 'not_applicable')),
    CONSTRAINT ck_compliance_control_row_version         CHECK (row_version >= 1),
    CONSTRAINT uq_compliance_control_tenant_framework_ref UNIQUE (tenant_id, framework, control_ref)
);

-- Index every access path (docs/PLATFORM-BUILD-PLAN.md §3). tenant_id leads
-- the keyset-pagination index because it is the RLS-filtered column every
-- List query already carries — mirrors ix_risk_tenant_risk's reasoning.
CREATE INDEX IF NOT EXISTS ix_compliance_control_tenant_control   ON compliance_control (tenant_id, control_id);
CREATE INDEX IF NOT EXISTS ix_compliance_control_tenant_status    ON compliance_control (tenant_id, status);
CREATE INDEX IF NOT EXISTS ix_compliance_control_tenant_framework ON compliance_control (tenant_id, framework);

COMMENT ON TABLE compliance_control IS
    'A tenant-owned compliance-control register entry (E8.4b): a stateful reified entity, like risk/incident/vuln_finding, with a governed implementation lifecycle and an append-only evidence trail (control_evidence). Continuous-audit automation is OUT of scope for this table (ADR-SOC-009).';
COMMENT ON COLUMN compliance_control.framework IS
    'The compliance framework this control belongs to (e.g. SOC2, ISO27001, PCI-DSS) — open-but-bounded, NOT a closed enum. Case-preserving charset rule (domain.complianceControlFrameworkPattern) is a domain-layer concern.';
COMMENT ON COLUMN compliance_control.control_ref IS
    'The control''s own identifier WITHIN framework (e.g. CC6.1 under SOC2). Immutable after Create together with framework — see domain.ComplianceControl''s doc comment.';
COMMENT ON COLUMN compliance_control.status IS
    'The lifecycle: not_implemented -> {in_progress, not_applicable}; in_progress -> {implemented, not_applicable, not_implemented}; implemented -> {in_progress, not_implemented}; not_applicable -> not_implemented (domain.ComplianceControlStatus).';

ALTER TABLE compliance_control ENABLE ROW LEVEL SECURITY;
ALTER TABLE compliance_control FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON compliance_control
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

-- ---------------------------------------------------------------------------
-- control_evidence: append-only proof an operator files against a control
-- (E8.4b). Reuses incident_event's/asset_change_history's hardened
-- append-only pattern EXACTLY (20260825000001/20260818000001): ENABLE ALWAYS
-- triggers + REVOKEd UPDATE/DELETE/TRUNCATE, built in from this table's
-- first migration, and registered in
-- postgres.SchemaValidator.immutableAuditTables so boot and the sentinel
-- both verify it.
--
-- This is a PLAIN append-only table, NOT a second instance of the audit
-- hash-chain (audit_event/admin_audit_event's chain_id/seq/this_hash/
-- prev_hash machinery, ADR-AUDIT-003/004): control_evidence carries no
-- chain-of-custody linkage between its own rows, only RLS isolation plus the
-- immutability trigger — the identical "append-only but not chained"
-- treatment incident_event already has. See domain.ControlEvidence's own doc
-- comment.
--
-- control_id DOES carry a foreign key to compliance_control, exactly like
-- incident_event's FK to incident: compliance_control has NO Delete method
-- at all (E8.4b decision — see domain.ComplianceControlRepository's doc
-- comment), so the subject this table describes is never gone, and a normal
-- referential-integrity FK is strictly safer than omitting one. PostgreSQL's
-- foreign-key trigger runs with the constraint's own privileges and BYPASSES
-- row-level security on compliance_control (ADR-ASSET-001 §6's reasoning) —
-- ComplianceControlStore.AddEvidence re-verifies control_id against its own
-- tenant-scoped connection (under FOR UPDATE, on the PARENT row) before any
-- evidence row naming it is written.
--
-- TENANT-OWNED, RLS-isolated AND append-only at once — the same combination
-- audit_event/asset_change_history/incident_event already prove is not a
-- contradiction.
CREATE TABLE IF NOT EXISTS control_evidence (
    evidence_id text        PRIMARY KEY,
    tenant_id   text        NOT NULL DEFAULT 'system' REFERENCES tenant (tenant_id),
    control_id  text        NOT NULL REFERENCES compliance_control (control_id),
    kind        text        NOT NULL,
    value       text        NOT NULL,
    recorded_by text        NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ck_control_evidence_kind         CHECK (kind IN ('url', 'note', 'attestation')),
    CONSTRAINT ck_control_evidence_value_length CHECK (char_length(value) BETWEEN 1 AND 4000),
    CONSTRAINT ck_control_evidence_recorded_by  CHECK (recorded_by <> '')
);

-- The query this store serves: "this control's evidence trail, oldest
-- first" — keyset-ordered over evidence_id (a ULID, and therefore
-- chronological), the same shape ix_incident_event_incident uses.
CREATE INDEX IF NOT EXISTS ix_control_evidence_control ON control_evidence (tenant_id, control_id, evidence_id);

COMMENT ON TABLE control_evidence IS
    'Append-only proof an operator files against a compliance_control (E8.4b): a URL, a note, or a formal attestation. NOT part of the audit hash-chain (audit_event) — a plain append-only table, isolation + immutability only.';
COMMENT ON COLUMN control_evidence.kind IS
    'url | note | attestation (domain.ControlEvidenceKind).';
COMMENT ON COLUMN control_evidence.recorded_by IS
    'The actor who filed this evidence, sourced from the authenticated request subject at write time — never client-supplied (domain.ControlEvidence.RecordedBy).';

ALTER TABLE control_evidence ENABLE ROW LEVEL SECURITY;
ALTER TABLE control_evidence FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON control_evidence;
CREATE POLICY tenant_isolation ON control_evidence
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

-- Append-only enforcement, built in from day one — mirrors
-- incident_event's/asset_change_history's identical hardening verbatim.
-- ENABLE ALWAYS fires regardless of session_replication_role, closing the
-- bypass 20260809000001 proved live against origin-mode triggers.
CREATE OR REPLACE FUNCTION control_evidence_immutable() RETURNS trigger
    LANGUAGE plpgsql AS
$$
BEGIN
    RAISE EXCEPTION 'control_evidence is append-only: % is not permitted', TG_OP;
END;
$$;

CREATE OR REPLACE TRIGGER trg_control_evidence_no_row_mutate
    BEFORE UPDATE OR DELETE ON control_evidence
    FOR EACH ROW EXECUTE FUNCTION control_evidence_immutable();
ALTER TABLE control_evidence ENABLE ALWAYS TRIGGER trg_control_evidence_no_row_mutate;

CREATE OR REPLACE TRIGGER trg_control_evidence_no_truncate
    BEFORE TRUNCATE ON control_evidence
    FOR EACH STATEMENT EXECUTE FUNCTION control_evidence_immutable();
ALTER TABLE control_evidence ENABLE ALWAYS TRIGGER trg_control_evidence_no_truncate;

-- Privilege, the second, independent layer (ADR-AUDIT-008 §5's reasoning,
-- applied from the start, exactly as incident_event's does). SELECT and
-- INSERT are RETAINED: the request path both records evidence (on
-- AddEvidence) and reads it back (embedded in GET .../{id}) on this same
-- tenant-scoped connection. UPDATE/DELETE/TRUNCATE are revoked because
-- nothing may ever hold them on an append-only table.
REVOKE UPDATE, DELETE, TRUNCATE ON control_evidence FROM PUBLIC;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'oneops_app') THEN
        EXECUTE 'REVOKE UPDATE, DELETE, TRUNCATE ON control_evidence FROM oneops_app';
    END IF;
END;
$$;
