-- OPS-SA.1 Adds SNMP v2c reachability monitoring's one new column to the
-- existing collector_check table (20260823000001) — no new table, no reified
-- Device: an "snmp" check is a collector_check like any other, and its
-- result is written as telemetry_sample rows exactly as an "http" check's is
-- (ADR-NET-001).
--
-- snmp_community is a device credential, NULLABLE: only a check whose
-- type = 'snmp' sets it (enforced in domain.CollectorCheck.Validate, not
-- here — the same "type-specific invariant lives in application code, not a
-- cross-column CHECK" choice this table already makes for target's format,
-- see this table's own header comment in 20260823000001). It inherits
-- collector_check's existing RLS policy (tenant_isolation, unchanged by this
-- migration) and is never returned by any httpapi response — see
-- collectorCheckDTO's doc comment. There is no secret store in this
-- platform yet, so RLS-scoping plus redaction-on-read is the whole of this
-- column's protection; at-rest encryption and SNMP v3 (no shared secret at
-- all) are flagged follow-ups, not solved here.
ALTER TABLE collector_check ADD COLUMN IF NOT EXISTS snmp_community text;

ALTER TABLE collector_check ADD CONSTRAINT ck_collector_check_snmp_community_length
    CHECK (snmp_community IS NULL OR char_length(snmp_community) <= 255);

COMMENT ON COLUMN collector_check.snmp_community IS
    'SNMP v2c community string, a device credential. Set only for type=snmp (enforced in application code); redacted from every httpapi response. No at-rest encryption yet — no secret store exists in this platform (A.1, ADR-NET-001).';
