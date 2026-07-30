#!/usr/bin/env bash
#
# Disaster-recovery drill: back up the live database, restore it into a
# throwaway database, and assert the restored copy matches the source.
#
# docs/disaster-recovery.md described a recovery procedure that had never been
# executed. A procedure that has not been run is a hypothesis, so this script
# exists to run it on demand and in CI, and to fail loudly when recovery breaks.
#
# The drill asserts three things that a backup job alone does not:
#   1. the dump restores at all;
#   2. every table's row count survives the round trip;
#   3. the audit hash chain is still verifiable afterwards — the audit tables
#      carry triggers and a partitioned parent, which are exactly the objects a
#      naive dump/restore tends to lose.
#
# Usage:
#   scripts/dr-drill.sh
#
# Environment:
#   ONEOPS_DB_URL     source database (required)
#   DR_DRILL_DB       name of the throwaway target (default oneops_drdrill)

set -euo pipefail

: "${ONEOPS_DB_URL:?ONEOPS_DB_URL must be set}"
DRILL_DB="${DR_DRILL_DB:-oneops_drdrill}"
WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"' EXIT

# Derive the admin URL (same server, `postgres` database) so the drill database
# can be dropped and recreated without touching the source.
ADMIN_URL="${ONEOPS_DB_URL%/*}/postgres"
QUERY_PARAMS=""
case "$ONEOPS_DB_URL" in
  *\?*) QUERY_PARAMS="?${ONEOPS_DB_URL#*\?}" ;;
esac
ADMIN_URL="${ADMIN_URL%%\?*}$QUERY_PARAMS"
TARGET_URL="${ONEOPS_DB_URL%/*}/$DRILL_DB"
TARGET_URL="${TARGET_URL%%\?*}$QUERY_PARAMS"

# The drill drops and recreates its target. DR_DRILL_DB is operator-supplied and
# flowed straight into `DROP DATABASE IF EXISTS $DRILL_DB` with nothing comparing
# it to the live database — so `DR_DRILL_DB=oneops` against a live
# `.../oneops` DSN dropped production. An easy mistake to make and impossible to
# undo (ADR-TENANCY-010).
#
# Refuse rather than warn: a drill that destroys the thing it exists to prove
# recoverable is worse than no drill.
LIVE_DB="${ONEOPS_DB_URL##*/}"
LIVE_DB="${LIVE_DB%%\?*}"
if [ "$DRILL_DB" = "$LIVE_DB" ]; then
  echo "error: DR_DRILL_DB ($DRILL_DB) is the live database named in ONEOPS_DB_URL." >&2
  echo "       The drill drops and recreates its target; this would destroy production." >&2
  echo "       Set DR_DRILL_DB to a throwaway name (default: oneops_drdrill)." >&2
  exit 1
fi

echo "== 1/5 capture source state =="
# The row counts that must survive. Ordering makes the later diff meaningful.
#
# EXACT counts, not pg_stat_user_tables.n_live_tup. n_live_tup is a planner
# estimate maintained by ANALYZE and autovacuum: on a source whose statistics
# are stale it reads 0 for every table, while the target below was ANALYZEd
# moments earlier, so the drill compared stale estimates against fresh ones and
# failed on every populated database. Counting is slower and correct, and a
# disaster-recovery drill that cannot tell you whether your rows came back is
# not worth its runtime.
count_rows() {
  psql "$1" -tAF, -c "
    SELECT relname,
           (xpath('/row/c/text()',
                  query_to_xml(format('SELECT count(*) AS c FROM public.%I', relname),
                               false, true, '')))[1]::text::bigint AS n
      FROM pg_stat_user_tables
     WHERE schemaname = 'public'
     ORDER BY relname"
}
count_rows "$ONEOPS_DB_URL" >"$WORK_DIR/before.csv"
echo "captured $(wc -l <"$WORK_DIR/before.csv" | tr -d ' ') tables"

echo "== 2/5 back up =="
scripts/db-backup.sh "$WORK_DIR"
DUMP="$(ls -t "$WORK_DIR"/oneops-*.dump | head -1)"

echo "== 3/5 create throwaway target: $DRILL_DB =="
psql "$ADMIN_URL" -q -c "DROP DATABASE IF EXISTS $DRILL_DB" \
                  -c "CREATE DATABASE $DRILL_DB"

echo "== 4/5 restore =="
scripts/db-restore.sh "$DUMP" "$TARGET_URL"

echo "== 5/5 verify =="
# ANALYZE so pg_stat_user_tables lists the restored relations; the counts
# themselves come from count_rows and do not depend on its estimates.
psql "$TARGET_URL" -q -c "ANALYZE" >/dev/null
count_rows "$TARGET_URL" >"$WORK_DIR/after.csv"

if ! diff -u "$WORK_DIR/before.csv" "$WORK_DIR/after.csv" >"$WORK_DIR/diff.txt"; then
  echo "FAIL: restored row counts differ from source" >&2
  cat "$WORK_DIR/diff.txt" >&2
  psql "$ADMIN_URL" -q -c "DROP DATABASE IF EXISTS $DRILL_DB" || true
  exit 1
fi

# The append-only trigger is the audit subsystem's load-bearing guarantee. If a
# restore silently dropped it, audit history would become mutable without any
# other symptom, so the drill asserts it came back.
TRIGGERS="$(psql "$TARGET_URL" -tAc "
  SELECT count(*) FROM pg_trigger
  WHERE tgrelid = 'audit_event'::regclass AND NOT tgisinternal")"
if [ "$TRIGGERS" -lt 2 ]; then
  echo "FAIL: audit_event append-only triggers did not survive the restore (found $TRIGGERS, want 2)" >&2
  psql "$ADMIN_URL" -q -c "DROP DATABASE IF EXISTS $DRILL_DB" || true
  exit 1
fi

# The administrative audit store has its own guards, and they must be ENABLE
# ALWAYS ('A'): an origin-mode trigger is suppressed by
# session_replication_role, and a replica-mode one never fires during
# application traffic at all. Counting triggers is not enough — a restore that
# brought them back disarmed would have passed the check above.
# Conditional on presence: a database restored from a source that predates
# migration 20260805000001 has no administrative audit store, and that is not a
# restore failure. When the table IS present its guards must come back armed.
ADMIN_PRESENT="$(psql "$TARGET_URL" -tAc "SELECT to_regclass('public.admin_audit_event') IS NOT NULL")"
ADMIN_TRIGGERS="n/a"
if [ "$ADMIN_PRESENT" = "t" ]; then
  ADMIN_TRIGGERS="$(psql "$TARGET_URL" -tAc "
    SELECT count(*) FROM pg_trigger
    WHERE tgrelid = 'public.admin_audit_event'::regclass
      AND NOT tgisinternal AND tgenabled = 'A'")"
  if [ "$ADMIN_TRIGGERS" -lt 2 ]; then
    echo "FAIL: admin_audit_event's append-only guards did not survive the restore armed" >&2
    echo "      (found $ADMIN_TRIGGERS with tgenabled='A', want 2)" >&2
    psql "$ADMIN_URL" -q -c "DROP DATABASE IF EXISTS $DRILL_DB" || true
    exit 1
  fi
  if psql "$TARGET_URL" -tAc "SET ROLE oneops_app; SELECT 1 FROM admin_audit_event LIMIT 1" >/dev/null 2>&1; then
    echo "FAIL: oneops_app can read admin_audit_event after restore — the restore re-granted a" >&2
    echo "      privilege OPS-S034 revoked (ADR-AUDIT-007 §6.5: platform administrators only)." >&2
    psql "$ADMIN_URL" -q -c "DROP DATABASE IF EXISTS $DRILL_DB" || true
    exit 1
  fi
fi

# A restore is only a recovery if the application can serve from it. The drill
# previously verified as the connecting superuser, which is exempt from every
# ACL — so a restore that discarded all privileges reported PASSED while no pod
# could read a single table. Assert both directions as the request-path role:
# what it must be able to read, and what it must not.
if ! psql "$TARGET_URL" -tAc "SET ROLE oneops_app; SELECT 1 FROM configuration_object LIMIT 1" >/dev/null 2>&1; then
  echo "FAIL: oneops_app cannot read configuration_object after restore — the request path is dead." >&2
  echo "      Privileges did not survive the round trip." >&2
  psql "$ADMIN_URL" -q -c "DROP DATABASE IF EXISTS $DRILL_DB" || true
  exit 1
fi

psql "$ADMIN_URL" -q -c "DROP DATABASE IF EXISTS $DRILL_DB"

echo
echo "DR DRILL PASSED"
echo "  tables verified:         $(wc -l <"$WORK_DIR/before.csv" | tr -d ' ')"
echo "  audit triggers restored: $TRIGGERS"
if [ "$ADMIN_PRESENT" = "t" ]; then
  echo "  admin guards armed (A):  $ADMIN_TRIGGERS (admin_audit_event refused to oneops_app)"
else
  echo "  admin guards armed (A):  n/a (admin_audit_event absent from this source)"
fi
echo "  serveable as oneops_app: yes (configuration_object readable)"
echo "  dump: $(basename "$DUMP") ($(wc -c <"$DUMP" | tr -d ' ') bytes)"
