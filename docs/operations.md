# Local operations

Everything here targets the single-process, single-database local deployment.
Nothing here is a production runbook.

## Start, health, and readiness

```sh
docker compose up -d --build
docker compose ps
curl -fsS localhost:8080/healthz   # liveness
curl -fsS localhost:8080/readyz    # liveness + database ping
```

`/healthz` answers as soon as the process is up. `/readyz` additionally pings
PostgreSQL and returns `503 not_ready` if the database is unreachable.

The `api` service declares a Compose `healthcheck` that runs
`/stockflow-api healthcheck`, an in-process probe of the binary's own
`/healthz`. This works in the distroless image, which has no shell or HTTP
client.

## Image pinning

Base images in `Dockerfile` and `compose.yaml` are pinned by immutable digest,
with the readable tag kept in a comment. Refresh a pin deliberately:

```sh
docker buildx imagetools inspect node:22-alpine
```

## Backup and restore

```sh
scripts/backup.sh                    # backups/stockflow-<stamp>.sql
STOCKFLOW_BACKUP_DB=stockflow_test scripts/backup.sh
scripts/restore.sh backups/stockflow-<stamp>.sql
```

Dumps use `pg_dump --clean --if-exists`, and restore uses
`psql -v ON_ERROR_STOP=1`, so a partial restore cannot look successful.

The rehearsal is exercisable and is run as part of verification:

```sh
scripts/backup-restore-test.sh
```

It seeds the test database deterministically, dumps it, restores the dump into
a scratch database, compares every table's row count, and drops the scratch
database. It fails on the first mismatch.

## Benchmarks

```sh
benchmarks/run.sh
```

Records the environment and writes raw JSON to `benchmarks/raw/`. See
`benchmarks/README.md` for method, seed, and limitations. The raw artifacts, not
any prose summary, are the authority for a number.

## Configuration

See `.env.example`. Outside demo mode, startup fails unless the operator
credential and session secret are set. Docker Compose runs the local demo with
`STOCKFLOW_DEMO_MODE=true`, which uses documented non-secret defaults and a
non-Secure cookie. Never run demo mode on an untrusted network.

## Continuous integration

`.github/workflows/ci.yml` runs three jobs:

1. **go** — `gofmt`, `go vet`, unit tests, PostgreSQL integration tests, the
   deterministic demo, and `go test -race` against a PostgreSQL service;
2. **web** — Prettier check, `tsc`, Vitest, and the production build;
3. **e2e** — builds the Docker image, starts PostgreSQL and the application,
   waits on `/readyz`, then runs the Playwright + axe suite against the shipped
   binary. The suite fails on browser console errors, uncaught page errors, and
   failed network requests.
