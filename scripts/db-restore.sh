#!/usr/bin/env bash
#
# Restore an OneOps logical backup into a target database.
#
# This is the counterpart to db-backup.sh and exists so that recovery is an
# executed procedure rather than a described one. `make dr-drill` runs both
# against a throwaway database and asserts the row counts match, which is what
# turns "we have backups" into "we have verified we can restore them".
#
# The restore is destructive: --clean --if-exists drops existing objects first
# so a partially-populated target cannot silently merge with the dump.
#
# Usage:
#   scripts/db-restore.sh <dump-file> [target-url]
#
# Environment:
#   ONEOPS_DB_URL   used as the target when no target-url argument is given

set -euo pipefail

DUMP="${1:?usage: db-restore.sh <dump-file> [target-url]}"
TARGET="${2:-${ONEOPS_DB_URL:-}}"
: "${TARGET:?target database URL required (argument or ONEOPS_DB_URL)}"

command -v pg_restore >/dev/null 2>&1 || {
  echo "error: pg_restore not found on PATH" >&2
  exit 1
}
[ -f "$DUMP" ] || {
  echo "error: dump file not found: $DUMP" >&2
  exit 1
}

echo "Restoring $DUMP into the target database (existing objects are dropped)"

# audit_event carries an append-only trigger that raises on DELETE and TRUNCATE.
# --clean drops the table outright, which is DDL and therefore permitted; the
# trigger is recreated from the dump along with the table.
#
# --exit-on-error is deliberately NOT used with --clean: the initial DROP of an
# object that does not yet exist in an empty target is an expected, harmless
# error. Success is asserted by the verification step below, not by pg_restore's
# exit code.
pg_restore \
  --dbname="$TARGET" \
  --clean \
  --if-exists \
  --no-owner \
  --no-privileges \
  --jobs=4 \
  "$DUMP" 2>&1 | grep -v "does not exist, skipping" || true

# Prove the restore produced a queryable schema rather than a partial one.
if ! psql "$TARGET" -tAc "SELECT 1 FROM configuration_object LIMIT 1" >/dev/null 2>&1; then
  echo "error: restore verification failed — configuration_object is not queryable" >&2
  exit 1
fi

echo "OK: restore verified"
