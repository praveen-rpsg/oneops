#!/usr/bin/env bash
#
# Take a consistent, restorable logical backup of the OneOps database.
#
# Custom format (-Fc) rather than plain SQL: it is compressed, restores in
# parallel, and lets pg_restore select individual objects during a partial
# recovery. --no-owner/--no-privileges keep the dump restorable into a database
# whose role names differ from the source, which is the normal case when
# restoring production into a staging cluster to rehearse recovery.
#
# Usage:
#   scripts/db-backup.sh [output-directory]
#
# Environment:
#   ONEOPS_DB_URL   PostgreSQL connection string (required)
#
# Exits non-zero if the dump fails or the resulting file fails verification, so
# it is safe to run from cron or a Kubernetes CronJob.

set -euo pipefail

OUT_DIR="${1:-./backups}"
: "${ONEOPS_DB_URL:?ONEOPS_DB_URL must be set}"

command -v pg_dump >/dev/null 2>&1 || {
  echo "error: pg_dump not found on PATH" >&2
  exit 1
}

mkdir -p "$OUT_DIR"

STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
FILE="$OUT_DIR/oneops-$STAMP.dump"

echo "Backing up to $FILE"
# --no-owner is kept: ownership is a property of the cluster the dump lands in,
# and the restoring role takes it.
#
# --no-privileges is deliberately NOT used. Privileges are not cosmetic here:
# oneops_app is the role every request-scoped connection assumes
# (NewTenantScopedPool does SET ROLE oneops_app), so a dump without ACLs
# restores to a database the application cannot read at all. Measured against
# this schema: 84 grants live, 0 emitted with --no-privileges, 22 without.
# Migrations do not repair it — schema_migrations restores as already-applied,
# so migrate.Up re-applies nothing.
#
# Revocations survive by absence, which is the property that matters for
# admin_audit_event and admin_audit_chain_head: they carry no grant to
# oneops_app, so no GRANT is emitted, and the restored table is owner-only.
pg_dump \
  --dbname="$ONEOPS_DB_URL" \
  --format=custom \
  --compress=9 \
  --no-owner \
  --file="$FILE"

# A dump that cannot be listed cannot be restored. Verifying here turns a silent
# corrupt-backup failure into a loud backup-job failure, which is the whole
# point of having the job.
if ! pg_restore --list "$FILE" >/dev/null 2>&1; then
  echo "error: backup at $FILE failed verification; removing" >&2
  rm -f "$FILE"
  exit 1
fi

SIZE="$(wc -c <"$FILE" | tr -d ' ')"
OBJECTS="$(pg_restore --list "$FILE" | grep -c '^[0-9]' || true)"
echo "OK: $FILE ($SIZE bytes, $OBJECTS objects)"
