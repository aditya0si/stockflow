#!/usr/bin/env bash
# Exercised backup/restore rehearsal for local PostgreSQL.
#
# It seeds the test database deterministically, dumps it, restores the dump into
# a scratch database, and compares row counts table by table. It fails on the
# first mismatch and drops the scratch database afterwards.
#
# Requires the Compose PostgreSQL service to be available.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

SRC_DB="${STOCKFLOW_BACKUP_DB:-stockflow_test}"
SCRATCH_DB="stockflow_restore_test"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

TABLES="skus inventory_balances inventory_movements orders order_items reservations idempotency_records fulfilment_events audit_entries reconciliation_runs reconciliation_findings"

echo "== starting postgres =="
docker compose up -d postgres >/dev/null
until docker compose exec -T postgres pg_isready -U stockflow -d "$SRC_DB" >/dev/null 2>&1; do
  sleep 1
done

echo "== seeding $SRC_DB =="
DATABASE_URL="postgres://stockflow:stockflow@localhost:5432/${SRC_DB}?sslmode=disable" go run ./cmd/seed -reset >/dev/null

echo "== dumping $SRC_DB =="
docker compose exec -T postgres pg_dump -U stockflow -d "$SRC_DB" --clean --if-exists > "$WORK/dump.sql"

echo "== restoring into $SCRATCH_DB =="
docker compose exec -T postgres psql -v ON_ERROR_STOP=1 -U stockflow -d postgres \
  -c "drop database if exists $SCRATCH_DB" >/dev/null
docker compose exec -T postgres psql -v ON_ERROR_STOP=1 -U stockflow -d postgres \
  -c "create database $SCRATCH_DB" >/dev/null
docker compose exec -T postgres psql -v ON_ERROR_STOP=1 -U stockflow -d "$SCRATCH_DB" \
  < "$WORK/dump.sql" >/dev/null

echo "== comparing row counts =="
for table in $TABLES; do
  source_count="$(docker compose exec -T postgres psql -tA -U stockflow -d "$SRC_DB" -c "select count(*) from $table")"
  restored_count="$(docker compose exec -T postgres psql -tA -U stockflow -d "$SCRATCH_DB" -c "select count(*) from $table")"
  if [ "$source_count" != "$restored_count" ]; then
    echo "MISMATCH $table: source=$source_count restored=$restored_count" >&2
    exit 1
  fi
  echo "ok $table=$source_count"
done

docker compose exec -T postgres psql -U stockflow -d postgres \
  -c "drop database $SCRATCH_DB" >/dev/null
echo "BACKUP/RESTORE REHEARSAL PASSED"
