#!/usr/bin/env bash
# Reproducible benchmark runner. It records the environment, then runs the
# hot-SKU contention and reconciliation-over-seeded-history benchmarks against
# the Compose PostgreSQL instance, writing raw JSON to benchmarks/raw/.
#
#   scripts:   benchmarks/run.sh
#   database:  STOCKFLOW_BENCH_DATABASE_URL (default: Compose stockflow_test)
#
# Both benchmarks truncate the target database, so use a disposable database.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

RAW="${STOCKFLOW_BENCH_RAW:-benchmarks/raw}"
mkdir -p "$RAW"

STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
DATABASE_URL="${STOCKFLOW_BENCH_DATABASE_URL:-postgres://stockflow:stockflow@localhost:5432/stockflow_test?sslmode=disable}"
export DATABASE_URL

ENV_FILE="$RAW/environment-$STAMP.txt"
{
  echo "timestamp_utc: $STAMP"
  echo "os: $(uname -a 2>/dev/null || echo unknown)"
  echo "go: $(go version 2>&1)"
  echo "docker_server: $(docker version --format '{{.Server.Version}}' 2>/dev/null || echo unknown)"
  echo "postgres_image: $(docker compose config --images 2>/dev/null | grep -m1 postgres || echo unknown)"
  echo "cpu: $(powershell.exe -NoProfile -Command '(Get-CimInstance Win32_Processor).Name' 2>/dev/null | tr -d '\r' | head -1 || (grep -m1 'model name' /proc/cpuinfo 2>/dev/null) || echo unknown)"
  echo "database_url: $DATABASE_URL"
} | tee "$ENV_FILE"

echo
echo "== hot SKU contention =="
go run ./benchmarks/hot_sku | tee "$RAW/hot_sku-$STAMP.json"

echo
echo "== reconciliation over seeded history =="
go run ./benchmarks/reconcile | tee "$RAW/reconcile-$STAMP.json"

echo
echo "raw artifacts written to $RAW"
