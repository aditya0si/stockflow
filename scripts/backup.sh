#!/usr/bin/env bash
# Back up the local Compose PostgreSQL database to a timestamped SQL dump.
#
#   scripts/backup.sh                 # dumps DATABASE (default: stockflow)
#   STOCKFLOW_BACKUP_DB=stockflow_test scripts/backup.sh
#
# The dump uses --clean --if-exists so it can be restored over an existing
# database. Dumps are written to STOCKFLOW_BACKUP_DIR (default: backups/), which
# is gitignored.
set -euo pipefail

DB="${STOCKFLOW_BACKUP_DB:-stockflow}"
OUT_DIR="${STOCKFLOW_BACKUP_DIR:-backups}"
STAMP="$(date +%Y%m%d-%H%M%S)"
FILE="${OUT_DIR}/${DB}-${STAMP}.sql"

mkdir -p "$OUT_DIR"
docker compose exec -T postgres pg_dump -U stockflow -d "$DB" --clean --if-exists > "$FILE"
echo "backup written: $FILE"
