#!/usr/bin/env bash
# Exercised backup/restore rehearsal for PostgreSQL.
#
# It seeds the test database deterministically, dumps it, restores the dump into
# a scratch database, and compares row counts table by table. It fails on the
# first mismatch and drops the scratch database afterwards.
#
# Two connection modes:
#
#   * default (docker): drives the Compose PostgreSQL container with
#     `docker compose exec`; use this for local `make restore-test`.
#
#   * direct: set STOCKFLOW_BACKUP_DIRECT=1 to invoke the `psql` and `pg_dump`
#     client binaries directly against PGHOST/PGPORT/PGUSER/PGPASSWORD. Linux CI
#     uses this mode against the PostgreSQL service container, where there is no
#     Compose project to exec into.
#
# The failure contract is identical in both modes: `set -euo pipefail`,
# `psql -v ON_ERROR_STOP=1`, and a row-count comparison that exits nonzero on the
# first mismatch. Neither mode swallows an error.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

SRC_DB="${STOCKFLOW_BACKUP_DB:-stockflow_test}"
SCRATCH_DB="${STOCKFLOW_RESTORE_SCRATCH_DB:-stockflow_restore_test}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

TABLES="skus inventory_balances inventory_movements orders order_items reservations idempotency_records fulfilment_events audit_entries reconciliation_runs reconciliation_findings"

PGHOST="${PGHOST:-localhost}"
PGPORT="${PGPORT:-5432}"
PGUSER="${PGUSER:-stockflow}"
PGPASSWORD="${PGPASSWORD:-stockflow}"
export PGHOST PGPORT PGUSER PGPASSWORD

if [ "${STOCKFLOW_BACKUP_DIRECT:-0}" = "1" ]; then
  command -v psql >/dev/null 2>&1 || { echo "psql not found; install a PostgreSQL client" >&2; exit 1; }
  command -v pg_dump >/dev/null 2>&1 || { echo "pg_dump not found; install a PostgreSQL client" >&2; exit 1; }
  admin() { psql -v ON_ERROR_STOP=1 -U "$PGUSER" -d postgres -c "$1"; }
  query() { psql -tA -U "$PGUSER" -d "$1" -c "$2"; }
  restore_dump() { psql -v ON_ERROR_STOP=1 -U "$PGUSER" -d "$1"; }
  dump_db() { pg_dump -U "$PGUSER" -d "$1" --clean --if-exists; }
else
  echo "== starting postgres =="
  docker compose up -d postgres >/dev/null
  until docker compose exec -T postgres pg_isready -U "$PGUSER" -d "$SRC_DB" >/dev/null 2>&1; do
    sleep 1
  done
  admin() { docker compose exec -T postgres psql -v ON_ERROR_STOP=1 -U "$PGUSER" -d postgres -c "$1"; }
  query() { docker compose exec -T postgres psql -tA -U "$PGUSER" -d "$1" -c "$2"; }
  restore_dump() { docker compose exec -T postgres psql -v ON_ERROR_STOP=1 -U "$PGUSER" -d "$1"; }
  dump_db() { docker compose exec -T postgres pg_dump -U "$PGUSER" -d "$1" --clean --if-exists; }
fi

echo "== seeding $SRC_DB =="
DATABASE_URL="postgres://${PGUSER}:${PGPASSWORD}@${PGHOST}:${PGPORT}/${SRC_DB}?sslmode=disable" \
  go run ./cmd/seed -reset >/dev/null

echo "== dumping $SRC_DB =="
dump_db "$SRC_DB" > "$WORK/dump.sql"

echo "== restoring into $SCRATCH_DB =="
admin "drop database if exists $SCRATCH_DB" >/dev/null
admin "create database $SCRATCH_DB" >/dev/null
restore_dump "$SCRATCH_DB" < "$WORK/dump.sql" >/dev/null

echo "== comparing row counts =="
for table in $TABLES; do
  source_count="$(query "$SRC_DB" "select count(*) from $table")"
  restored_count="$(query "$SCRATCH_DB" "select count(*) from $table")"
  if [ "$source_count" != "$restored_count" ]; then
    echo "MISMATCH $table: source=$source_count restored=$restored_count" >&2
    exit 1
  fi
  echo "ok $table=$source_count"
done

admin "drop database $SCRATCH_DB" >/dev/null
echo "BACKUP/RESTORE REHEARSAL PASSED"
