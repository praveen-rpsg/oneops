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
psql "$ONEOPS_DB_URL" -tAF, -c "
  SELECT relname, n_live_tup FROM pg_stat_user_tables
  WHERE schemaname = 'public' ORDER BY relname" >"$WORK_DIR/before.csv"
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
psql "$TARGET_URL" -q -c "ANALYZE" >/dev/null
psql "$TARGET_URL" -tAF, -c "
  SELECT relname, n_live_tup FROM pg_stat_user_tables
  WHERE schemaname = 'public' ORDER BY relname" >"$WORK_DIR/after.csv"

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

psql "$ADMIN_URL" -q -c "DROP DATABASE IF EXISTS $DRILL_DB"

echo
echo "DR DRILL PASSED"
echo "  tables verified:        $(wc -l <"$WORK_DIR/before.csv" | tr -d ' ')"
echo "  audit triggers restored: $TRIGGERS"
echo "  dump: $(basename "$DUMP") ($(wc -c <"$DUMP" | tr -d ' ') bytes)"
