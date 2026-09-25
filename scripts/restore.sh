#!/usr/bin/env bash
# Restore a dump produced by scripts/backup.sh into the local Compose database.
#
#   scripts/restore.sh backups/stockflow-20260926-010203.sql
#   STOCKFLOW_BACKUP_DB=stockflow_test scripts/restore.sh backups/...
#
# ON_ERROR_STOP makes psql exit nonzero on the first failure so a partial
# restore cannot look successful.
set -euo pipefail

FILE="${1:?usage: scripts/restore.sh <dump.sql>}"
DB="${STOCKFLOW_BACKUP_DB:-stockflow}"

if [ ! -f "$FILE" ]; then
  echo "no such dump: $FILE" >&2
  exit 1
fi

docker compose exec -T postgres psql -v ON_ERROR_STOP=1 -U stockflow -d "$DB" < "$FILE"
echo "restored $FILE into $DB"
