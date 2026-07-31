# ADR-AUDIT-008 — The constitutional audit chain is armed against the replication-role bypass

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-07-31 |
| **Decider** | Acting CTO |
| **Related** | ADR-AUDIT-003 (tamper-evident append-only audit — its promised second layer, delivered here), ADR-AUDIT-004/006 (chain-head lock), ADR-AUDIT-007 §6 + migration 20260805000001 (the admin-audit hardening this copies), ADR-TENANCY-007 (schema invariants validated at startup), ADR-SECURITY-002/003 (invariants continuously re-verified) |

## Context

The product promise is a tamper-evident, append-only constitutional audit chain
(`audit_event`). It was not one.

Migration `20260723000001` installs two guards — `trg_audit_event_no_row_mutate`
(FOR EACH ROW, against UPDATE/DELETE) and `trg_audit_event_no_truncate` (FOR EACH
STATEMENT, against TRUNCATE) — in PostgreSQL's default *origin* firing mode
(`pg_trigger.tgenabled = 'O'`). An origin-mode trigger is suppressed entirely by
`session_replication_role = 'replica'`. Proven live against the dev database
before this ADR:

```sql
SET session_replication_role = 'replica';
UPDATE audit_event SET actor = 'TAMPERED-BY-REPLICA-ROLE' WHERE chain_id='exploit-chain';
-- UPDATE 1        (the whole chain is rewritable this way; the 2026-07-30 probe rewrote all 22 rows)
TRUNCATE audit_event_default;
-- TRUNCATE TABLE  (the partition is erased; rows_remaining = 0)
```

Neither statement raised an error. The sibling administrative chain
(`admin_audit_event`) had already been hardened correctly (20260805000001 /
20260806000001, ADR-AUDIT-007 §6); the constitutional chain — the higher-value
one — had not. `SchemaValidator` was already built to enforce the fix and carried
a comment that `audit_event`'s `alwaysRequired` flag "flips to true once
`audit_event` is hardened by its own change." This is that change.

Separately, `20260729000001_rls_policies.sql` grants
`SELECT/INSERT/UPDATE/DELETE` on every table to `oneops_app` (the request-path
role), both directly (`ON ALL TABLES`) and going forward (`ALTER DEFAULT
PRIVILEGES`). So the request path arrived holding UPDATE and DELETE on an
append-only table. `20260723000001`'s header promised privilege revocation "in
the bootstrap" as the second layer — but no such bootstrap exists in `infra/`,
`deploy/` or `scripts/`, so that layer was never running.

**Who could exploit the replica-role bypass, stated honestly.**
`session_replication_role` is a superuser-context parameter. The request-path
role **cannot** set it — measured: `oneops_app` gets *"permission denied to set
parameter."* The bypass was reachable by the **privileged pool's role** (the
table owner, and in the dev stack the cluster superuser), by operators, and by
DBA/restore sessions — not by a tenant request. The tenant path was already
refused by the origin-mode trigger firing during ordinary traffic and by FORCE
row-level security. This is defence-in-depth against the owning pool and
insiders, and must not be described as closing a tenant-reachable hole.

## Decision

**The constitutional audit chain enforces append-only by two independent
structural layers, and both are verified continuously.**

1. **Both guards are `ENABLE ALWAYS` (`tgenabled = 'A'`) on the parent and on
   every partition.** An always-armed trigger fires regardless of
   `session_replication_role`, so the replica-mode bypass is closed. Because
   `audit_event` is LIST-partitioned and the two trigger kinds propagate
   differently — verified against PostgreSQL 16 — the migration arms the parent
   and then loops over every partition:
   - the row-level trigger is *cloned* onto partitions and `ALTER TABLE <parent>
     ENABLE ALWAYS` recurses to the clones;
   - the statement-level TRUNCATE trigger is **not** propagated (the same reason
     `20260728000002` attaches it partition by partition), so each partition is
     armed explicitly. Measured: without the per-partition arming the parent read
     `'A'` while `audit_event_default` stayed `'O'`.

2. **`oneops_app` holds no write privilege beyond INSERT.**
   `UPDATE/DELETE/TRUNCATE` are `REVOKE`d from `PUBLIC` and from `oneops_app`,
   explicitly, on the parent and every partition. `SELECT` and `INSERT` are
   retained: the request path appends events and the verifier and ownership
   resolver read the chain. No code path issues UPDATE/DELETE/TRUNCATE on
   `audit_event` — swept before deciding — because the chain-head lock
   (ADR-AUDIT-006) is taken on `audit_chain_head`, a different table this
   migration does not touch.

3. **The arming is a schema invariant.** `audit_event`'s `alwaysRequired` flag in
   `immutableAuditTables` flips to `true`, so `SchemaValidator` reports a silent
   downgrade to origin mode as **DOWNGRADED** (distinct from DISABLED and from
   absent) at boot and every sentinel interval (ADR-SECURITY-002/003).

## Consequences

**What is now guaranteed.** Neither the owning pool nor an operator can rewrite
or erase constitutional audit history through the `session_replication_role`
bypass without disabling the guards outright — and disabling or downgrading them
is itself detected, fail-closed, within one sentinel interval. Append-only is
enforced by privilege as well as by trigger, so a single re-grant or a single
trigger downgrade does not by itself re-open a rewrite path.

**What is *not* claimed.**

- **The guard proves presence and arming, not correctness.** It verifies the
  triggers exist and fire regardless of replication role; it does not prove the
  trigger body is right or that the hash chain itself verifies — that is the
  verifier's job (ADR-AUDIT-003/004).
- **Detection, not prevention, for the DDL path.** An operator with DDL rights
  can still `ALTER TABLE ... DISABLE TRIGGER` / `DROP TRIGGER` or re-grant the
  privileges. Nothing inside the application can stop that. The guarantee is that
  the platform notices within one sentinel interval and stops trusting itself
  (ADR-SECURITY-002); mutations inside that detection window are not prevented,
  and the window is bounded, not zero.
- **Not a tenant-facing fix.** The tenant path was already closed; this changes
  nothing a tenant could reach.
- **Future partitions.** A partition added operationally (ECR-09) inherits
  `oneops_app`'s default privileges and does *not* inherit the statement-level
  TRUNCATE guard. The migration that adds it MUST re-run the two revokes and
  `ENABLE ALWAYS` both guards on the new partition, exactly as `20260728000002`
  documents for the truncate guard. The boot validator checks every partition, so
  a missed one is reported rather than silent.

## Evidence

- **Before (live, owning role):** `UPDATE 1` rewrote a row's `actor`;
  `TRUNCATE audit_event_default` erased the partition (`rows_remaining = 0`).
- **After (live, same role, same statements):**
  `ERROR: audit_event is append-only: UPDATE is not permitted` and
  `ERROR: audit_event is append-only: TRUNCATE is not permitted`; the row
  survives unchanged.
- **Trigger modes after migration:** parent and `audit_event_default`, both
  guards, all `'A'`.
- **Privileges after migration:** `oneops_app` holds `SELECT`+`INSERT` and not
  `UPDATE`/`DELETE`/`TRUNCATE`, on both parent and partition.
- **Migration directory:** `make migrate-hash` + `make migrate-validate` clean.

## Enforcement

- `postgres.TestAuditEvent_GuardsSurviveReplicationRoleBypass` — the live exploit
  (replica-mode UPDATE + partition TRUNCATE) as a regression test.
- `postgres.TestAuditEvent_RequestPathRoleHoldsOnlyAppendAndRead` — the exact
  grant set on parent and partition, asserted directly.
- `postgres.TestAuditEvent_DowngradedGuardIsDetected` — proves the
  `alwaysRequired` flip bites: a downgrade to origin mode is reported DOWNGRADED.
- `postgres.SchemaValidator.validateAuditImmutability` (via ADR-TENANCY-007 boot
  gate and ADR-SECURITY-002/003 sentinel) — continuous re-verification of the
  arming on every partition.

**Mutation-verified.** Reverting the `alwaysRequired` flip makes
`TestAuditEvent_DowngradedGuardIsDetected` fail (the origin-mode guard reads as
healthy). Removing the migration makes
`TestAuditEvent_GuardsSurviveReplicationRoleBypass` and
`...RequestPathRoleHoldsOnlyAppendAndRead` fail (the bypass succeeds and
`oneops_app` still holds UPDATE/DELETE). Both were demonstrated.
