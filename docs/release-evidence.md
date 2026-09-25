# Release evidence (local freeze run)

This document records the release-evidence and hygiene items resolved for the
StockFlow freeze. It contains **locally verified evidence only**. No remote CI
run was observed for this commit, no CI URL is invented, and no remote result is
claimed.

## Commit under test

- Product code under test: `6af6dc9637de3187a1c06084b843387f7c8b6fae`
  (`feat: harden StockFlow for public release`).
- The freeze commit adds only documentation, `.gitattributes`, `LICENSE`, CI
  workflow wiring, and the backup/restore rehearsal script. It changes no Go or
  TypeScript product code, so every check below ran against the same product code
  as `6af6dc9`.
- The freeze commit hash is reported in the commit summary; this file is
  committed in that same commit (a file cannot contain its own hash).

Environment (local, at the freeze run):

```text
host_os:   MINGW64_NT-10.0-26200 Aditya 3.6.6-1cdd4371.x86_64 x86_64 Msys
go:        go version go1.27.0 windows/amd64
node:      v22.23.2
npm:       12.0.2
docker:    29.6.2 (Docker Compose v5.3.1)
postgres:  postgres:16-alpine@sha256:721873c34ceb9f8d8fc265984940dc982404c105f19ad51be9fdc5970a6080ea
```

## Backend: formatting, vet, unit and integration tests

```sh
gofmt -l .                                                    # no output
go vet ./...                                                  # exit 0
STOCKFLOW_TEST_DATABASE_URL='postgres://stockflow:stockflow@localhost:5432/stockflow_test?sslmode=disable' go test -count=1 ./internal/...
STOCKFLOW_TEST_DATABASE_URL='postgres://stockflow:stockflow@localhost:5432/stockflow_test?sslmode=disable' go test -count=1 ./tests/...
```

Results:

```text
ok  github.com/oliaditya05/stockflow/internal/auth         2.686s
ok  github.com/oliaditya05/stockflow/internal/fulfilment   1.785s
ok  github.com/oliaditya05/stockflow/internal/orders       2.001s
ok  github.com/oliaditya05/stockflow/tests/integration      1.917s
```

A single verbose run of `./internal/...` plus `./tests/...` reported
**52 passing test cases, 0 skipped, 0 failed** (`grep -c '^--- PASS'` = 52).
All tests that make concurrency or constraint claims ran against real
PostgreSQL; no integration test was skipped.

### Race detector

The Linux CI workflow is **configured** to run `go test -race ./...` against a
PostgreSQL service, but that workflow has **not** been observed on a remote CI
run. Locally on Windows the race build fails at `runtime/cgo`
(`cc1.exe: sorry, unimplemented: 64-bit mode not compiled in`) because only a
32-bit MinGW GCC toolchain is installed. That is a local toolchain limitation,
not a test result, and it is why no local race result is claimed here.

## Frontend: format, typecheck, unit tests, and build

```sh
cd web
npm run format:check    # All matched files use Prettier code style!
npm run typecheck       # exit 0
npm test                # 2 test files, 9 tests passed
npm run build           # built in 648ms
```

Result: **9/9 Vitest tests pass**, Prettier and `tsc --noEmit` are clean, and the
production bundle is emitted to `internal/webui/dist`:

```text
../internal/webui/dist/index.html                 0.40 kB
../internal/webui/dist/assets/index-CHSny4FC.css  4.15 kB
../internal/webui/dist/assets/index-Cph2N4H4.js 161.44 kB
```

## Browser tests: Playwright + axe against the production build

The suite runs against the shipped Compose image, not a dev server.

```sh
docker compose up -d --build
cd web && npx playwright install chromium && npx playwright test
```

Result:

```text
Running 7 tests using 1 worker

  ok 1 accessibility.spec.ts  has no detectable accessibility violations on each tab
  ok 2 accessibility.spec.ts  tablist supports Arrow, Home, and End keys with focus management
  ok 3 accessibility.spec.ts  data tables expose captions
  ok 4 errors.spec.ts         failed mutation preserves input and shows an actionable error
  ok 5 errors.spec.ts         sign-in with a wrong password shows an error without losing input
  -  6 screenshots.spec.ts    capture real product screenshots
  ok 7 workflow.spec.ts       operator journey: stock, order, fulfilment, and reconciliation

  1 skipped
  6 passed (7.7s)
```

- **6 passed.**
- The single skipped test is the screenshot generator, gated behind
  `STOCKFLOW_CAPTURE_SCREENSHOTS=true`, so a normal run cannot rewrite the
  committed screenshots.
- **axe result:** test 1 runs `@axe-core/playwright` on every tab (Stock,
  Orders, Reconciliation) and asserts zero detectable accessibility violations.
  It passed.

## Docker build and readiness

```sh
docker compose up -d --build
curl -s localhost:8080/healthz
curl -s localhost:8080/readyz
docker compose ps
```

Result: the image built successfully and the container reports healthy.

```text
healthz: {"status":"ok"}     HTTP 200
readyz:  {"status":"ready"}  HTTP 200

NAME                SERVICE   STATUS                   PORTS
stockflow-api-1     api       Up (healthy)             0.0.0.0:8080->8080/tcp
stockflow-postgres-1 postgres Up (healthy)             0.0.0.0:5432->5432/tcp
```

Built image: `stockflow-api:latest`
(`sha256:b4e74221b30677888fade31f84981187c7c42f34e0e55a7c5f270841c63ef8c6`,
`linux/amd64`, 21.8 MB).

## Deterministic demo

```sh
STOCKFLOW_ALLOW_DEMO_FIXTURES=true \
STOCKFLOW_DEMO_DATABASE_URL='postgres://stockflow:stockflow@localhost:5432/stockflow_test?sslmode=disable' \
  go run ./cmd/demo
```

Result: **DEMO PASSED — all assertions held** (exit 0). The seven assertions:

1. receipt creates exactly one unit on hand;
2. 25 simultaneous buyers produce one winner and no negative stock;
3. same-key/body replay returns the committed order without a second reservation;
4. pick → pack → ship each return the expected state;
5. shipment decrements `on_hand` once and clears the reservation;
6. illegal cancellation after shipment returns a stable problem and mutates
   nothing;
7. a seeded discrepancy is reported as `expected=1 observed=0` and left
   unrepaired.

## Backup/restore rehearsal

```sh
scripts/backup-restore-test.sh
```

Result: **BACKUP/RESTORE REHEARSAL PASSED** (exit 0). It seeded the test
database, dumped it, restored the dump into a scratch database, compared all 11
tables' row counts, and dropped the scratch database.

Raw timestamped log (committed):
[`docs/evidence/backup-restore-20260925T224728Z.log`](evidence/backup-restore-20260925T224728Z.log).
The log contains the environment, the exact command, every table's compared
count, and the result. It contains no credentials.

The same script supports a `STOCKFLOW_BACKUP_DIRECT=1` mode for Linux CI, where
there is no Compose project to exec into and the `psql`/`pg_dump` clients run
directly against the PostgreSQL service. That code path was exercised locally by
routing `psql`/`pg_dump` through the Compose container; it produced the same
`BACKUP/RESTORE REHEARSAL PASSED` result. In CI the real client binaries are
used, and the failure contract is unchanged (`set -euo pipefail`,
`psql -v ON_ERROR_STOP=1`, and a nonzero exit on the first row-count mismatch).

## Benchmarks

Committed artifacts (source of truth), from the recorded run
`20260925T221559Z`:

| Artifact | Result |
| --- | --- |
| `benchmarks/raw/hot_sku-20260925T221559Z.json` | 200 concurrent buyers for 1 unit: `accepted=1`, `conflicts=199`, `errors=0`, wall 195.4 ms, p95 188.3 ms |
| `benchmarks/raw/reconcile-20260925T221559Z.json` | 50 SKUs / 100,000 movements: `checks_run=6`, `findings_count=0`, `status=clean`, wall 94.6 ms |
| `benchmarks/raw/environment-20260925T221559Z.txt` | OS, Go, Docker, PostgreSQL image, CPU, database URL |

These are single-laptop, containerised-PostgreSQL figures and are not a capacity
claim. See `benchmarks/README.md`.

## Frontend dependency audit

`cd web && npm audit` reports **5 dev-toolchain advisories (3 moderate, 1 high,
1 critical)** at freeze time across `vitest`, `vite`, `vite-node`,
`@vitest/mocker`, and `esbuild`. All are `devDependencies`; the shipped artifact
is a static bundle embedded in a distroless Go image with no Node runtime and no
dev server. This is not a claim that the advisories are harmless. The exact
advisories, their scope, and the gated upgrade acceptance checks are recorded in
[`docs/dependency-upgrade-plan.md`](dependency-upgrade-plan.md).

## CI status (explicit)

`.github/workflows/ci.yml` defines three jobs (`go`, `web`, `e2e`) plus a
backup/restore rehearsal step in the `go` job that uploads its result log as the
`backup-restore-rehearsal-log` artifact. The Linux `go` job includes the race
detector. **This configuration has not yet been observed on a remote CI run.**
The pull request/push that produces a remote run should be treated as the first
real observation; until then no CI pass, badge, or remote race result is
claimed.

## Repository hygiene checks

- `.gitattributes` normalizes source/config/docs/shell files to LF and marks
  PNG/other binary assets as `binary`, so Windows checkouts stay format-clean.
- `LICENSE` is MIT, 2026, Aditya Singh.
- A secret scan over tracked files found no committed credentials; demo
  credentials are documented non-secret defaults.
- Generated artifacts (frontend build output, Playwright reports, backups,
  benchmark scratch, CI artifact directory) are gitignored and not tracked.
- The working tree is clean at the freeze commit apart from the intended
  changes.
