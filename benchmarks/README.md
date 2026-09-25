# Benchmarks

These benchmarks are small, reproducible, and deliberately local. They exist to
make two behaviours measurable, not to claim capacity:

1. **Hot-SKU contention** — many simultaneous single-unit orders for one unit
   produce one winner and a bounded rate set by the balance-row lock.
2. **Reconciliation over a seeded history** — one report-only run over a
   documented number of movements across a documented number of SKUs.

The raw results in `raw/` are the source of truth. Do not quote a number in the
README or a CV without pointing at the matching `raw/` artifact and its
`environment-*.txt` file.

## Reproduce

```sh
docker compose up -d postgres
benchmarks/run.sh
```

`run.sh` records the environment, runs both benchmarks against the Compose
`stockflow_test` database (override with `STOCKFLOW_BENCH_DATABASE_URL`), and
writes raw JSON plus an environment file to `benchmarks/raw/`.

Both benchmarks **truncate** the target database. Point them at a disposable
database only.

## What each benchmark does

### `hot_sku`

- Seeds one SKU with `units` opening stock (default 1).
- Fires `buyers` concurrent single-unit `POST`-equivalent order attempts through
  the real `orders.Service` (default 200) with unique idempotency keys.
- Records wall time, accepted/conflict/error counts, and latency percentiles.
- Fails (exit 1) if the number of accepted orders is not exactly `units`, so a
  green run proves the oversell invariant held.

### `reconcile`

- Seeds `skus` SKUs (default 50) and `movements` receipt movements (default
  100,000) spread across them with a server-side `generate_series`.
- Sets every balance to the ledger sum so the seeded history is consistent.
- Times one `reconciliation.Service.Run` and records `checks_run`,
  `findings_count`, and status.
- Fails (exit 1) if a consistent history produces findings.

## Recorded run

See `raw/hot_sku-*.json`, `raw/reconcile-*.json`, and `raw/environment-*.txt`
for the exact environment, seed, command, and raw output of the committed run.
The figures in that artifact are from a single Windows laptop with a
containerised PostgreSQL; they are not a capacity claim.

## Limitations

- Single process and a single PostgreSQL instance; no network between the
  application and the database is modelled (the benchmark process talks to the
  Compose port on `localhost`).
- The hot-SKU benchmark measures the application transaction path, and that path
  is expected to serialize on the `inventory_balances` row by design. A low
  throughput for one SKU is the intended trade-off, not a defect.
- Reconciliation timing excludes seeding and assumes warm caches; the first run
  against a cold database is slower.
- Numbers depend on the host CPU, Docker Desktop allocation, PostgreSQL
  settings, and the seeded distribution. Compare only runs produced on the same
  documented environment.
