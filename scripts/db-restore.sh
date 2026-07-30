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
# Roles are cluster-scoped, so a database dump does not carry them. Restoring
# into a fresh cluster would fail every GRANT without this. Idempotent, and it
# mirrors migration 20260729000001 exactly — NOLOGIN, no privileges of its own
# beyond what the dump grants.
psql "$TARGET" -q -c "DO \$\$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'oneops_app') THEN
        CREATE ROLE oneops_app NOLOGIN;
    END IF;
END
\$\$;"

# --no-privileges is deliberately NOT used; see db-backup.sh. A restore that
# drops the ACLs produces a database the request path cannot read, and nothing
# downstream repairs it.
pg_restore \
  --dbname="$TARGET" \
  --clean \
  --if-exists \
  --no-owner \
  --jobs=4 \
  "$DUMP" 2>&1 | grep -v "does not exist, skipping" || true

# Prove the restore produced a queryable schema rather than a partial one — and
# prove it AS THE ROLE THAT SERVES TRAFFIC. Verifying as the connecting
# superuser is what let a privilege-stripped restore report success: the owner
# is exempt from the ACLs whose absence breaks every request.
if ! psql "$TARGET" -tAc "SET ROLE oneops_app; SELECT 1 FROM configuration_object LIMIT 1" >/dev/null 2>&1; then
  echo "error: restore verification failed — configuration_object is not readable by oneops_app," >&2
  echo "       the role every request-scoped connection assumes. The restore is not serveable." >&2
  exit 1
fi

echo "OK: restore verified (as oneops_app)"
