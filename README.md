# StockFlow — inventory reservation and fulfilment workbench

StockFlow is an engineering case study in **transactional inventory
correctness**. It asks one question:

> How can an order API reserve limited stock atomically, remain safe under
> retries and races, and preserve enough evidence to explain or detect every
> discrepancy?

This repository is not a storefront, marketplace, or production product, and it
makes no adoption or traffic claims. It is a single-operator inventory and
fulfilment workbench built to make reservation, cancellation, shipment, and
reconciliation behaviour inspectable and provable against real PostgreSQL.

## Status

| Stage | State |
| --- | --- |
| V0 — executable domain spike | done |
| V1 — useful product (UI, fulfilment, idempotency, reconciliation, demo) | done |
| V2 — measured hardening (auth boundary, cross-table constraints, browser E2E/axe, backup rehearsal, benchmarks; role distinction explicitly descoped) | done |
| V3 — one evidence-driven extension | not started (see scope prohibitions) |

"Done" here means the code, migrations, and tests exist and the checks in
[Verification](#verification) were run locally against real PostgreSQL. See
`SPEC.md` for the item-by-item status matrix, and
[`docs/release-evidence.md`](docs/release-evidence.md) for the exact commands,
raw outputs, and artifacts from the freeze run. The Linux CI workflow is
configured but has **not** yet been observed on a remote CI run; no CI badge or
remote pass is claimed.

## What it is

A Go modular monolith (standard library HTTP, `pgx` PostgreSQL driver) that
serves a small React/TypeScript operator UI from the same binary:

| Capability | Status |
| --- | --- |
| SKU creation with optional opening stock | shipped |
| Stock receipts (positive quantity, actor, reason) | shipped |
| `POST /orders` with canonical hashing and atomic multi-SKU reservation | shipped |
| Payload-aware idempotency (`Idempotency-Key`, scope from session) | shipped |
| Cancellation, legal only before shipment, replay-safe | shipped |
| Fulfilment state machine `accepted → picking → packed → shipped` | shipped |
| Shipment releases reservation, decrements `on_hand` exactly once | shipped |
| Append-only movement ledger, fulfilment and audit evidence | shipped |
| Report-only reconciliation (6 checks, persisted findings) | shipped |
| Single-operator login, encrypted session cookie, CSRF, throttling | shipped |
| Cross-table order/item/SKU consistency constraints | shipped |
| Operator UI: stock, receipts, orders, pick/pack/ship, cancellation, reconciliation | shipped |
| Deterministic assertion-based demo (exits nonzero on failure) | shipped |
| Health/readiness endpoints, RFC 9457 problems | shipped |
| Integration, security, and race tests against real PostgreSQL | shipped |
| Playwright + axe browser suite against the production build | shipped |
| Backup/restore rehearsal and reproducible benchmarks | shipped |
| Roles, multiple operators, second location | not built |

### Modules

- `catalog` — SKU identity and metadata
- `inventory` — balances, receipts, immutable movements
- `orders` — creation, idempotency, atomic reservation, cancellation
- `fulfilment` — pick/pack/ship state machine and fulfilment evidence
- `audit` — actor/action history, fulfilment events, append-only triggers
- `reconciliation` — report-only checks, runs, and findings
- `auth` — single-operator credential, encrypted session, CSRF, login throttle
- `webui` — embedded operator UI
- `internal/platform` — PostgreSQL pool/migrations, HTTP problems, logging
- `cmd/{api,seed,demo,reconcile}` — process entrypoints

## Security in one paragraph

There is one operator. The username, a bcrypt password hash, and a session
secret are configured by environment; outside explicit demo mode a missing
value is a fatal startup error. Login issues an AES-256-GCM encrypted,
`HttpOnly`, `SameSite=Strict`, expiring cookie. Cookie-authenticated mutations
require a matching `X-CSRF-Token`. Every API route requires a session, all
audit actors come from the session (never the request body or headers), and
failed logins are throttled in a bounded in-memory window. This is deliberately
not enterprise authentication. Read `docs/security.md` for the full model,
threat table, and out-of-scope list before exposing the service to anything.

## Quick start

Requires Docker. One command builds the frontend, embeds it in the Go binary,
starts PostgreSQL, applies migrations, and serves API + UI:

```sh
docker compose up --build -d
curl -s localhost:8080/healthz
curl -s localhost:8080/readyz
```

Open `http://localhost:8080/` and sign in with the local demo credentials
`operator` / `stockflow-demo`. Compose sets `STOCKFLOW_DEMO_MODE=true`, which
uses generated non-secret defaults and a non-Secure cookie; the UI shows a demo
banner. **Never run demo mode on an untrusted network.**

To seed demo SKUs (optional):

```sh
docker compose exec api /stockflow-seed
```

To run without the app container:

```sh
cp .env.example .env
go run ./cmd/api hash-password 'choose-a-password'   # paste into STOCKFLOW_OPERATOR_PASSWORD_HASH
go run ./cmd/api new-secret                          # paste into STOCKFLOW_SESSION_SECRET
cd web && npm ci && npm run build && cd ..           # build the embedded UI
go run ./cmd/seed                                    # apply migrations + demo data
go run ./cmd/api
```

Without the frontend build the Go binary still compiles and serves API routes;
the UI route explains how to build the assets. If you cannot use `make`, the
equivalent commands are in the `Makefile`.

## Operator UI

The UI is a single screen with three tabs, served from the Go binary:

- **Stock** — SKU balances (`on_hand`, `reserved`, `available`), receive stock
  with actor/reason, create SKUs.
- **Orders** — submit an all-or-nothing order, select an order to view its
  state, items, movements, and fulfilment events, then pick → pack → ship or
  cancel before shipment.
- **Reconciliation** — run a report and inspect persisted findings (check name,
  reference, expected vs observed).

It is responsive down to a mobile viewport, has a skip link, labelled form
controls, a visible `:focus-visible` outline, WAI-ARIA tabs with
Arrow/Home/End keyboard navigation and selected-tab focus management, captioned
data tables, and `aria-live` notices. The Playwright + axe suite asserts zero
detectable accessibility violations on every tab. There is no offline write
queue: the UI re-reads authoritative server state.

Screenshots captured from a real local session are in `docs/screenshots/`:

![Stock tab](docs/screenshots/01-stock.png)
![Orders tab](docs/screenshots/02-orders.png)
![Order detail](docs/screenshots/03-order-detail.png)
![Reconciliation](docs/screenshots/04-reconciliation.png)

## Sample operator path over HTTP

The API is cookie-authenticated. Log in to a cookie jar, read the CSRF token,
and send it on every mutation:

```sh
# 1. log in (demo credentials)
curl -s -c cookies.txt -X POST localhost:8080/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"operator","password":"stockflow-demo"}'

CSRF=$(curl -s -b cookies.txt localhost:8080/auth/session \
  | sed -E 's/.*"csrf_token":"([^"]+)".*/\1/')

# 2. list SKUs and balances
curl -s -b cookies.txt localhost:8080/inventory/balances

# 3. receive stock (actor is taken from the session; reason is mandatory)
curl -s -b cookies.txt -X POST localhost:8080/inventory/receipts \
  -H "Content-Type: application/json" -H "X-CSRF-Token: $CSRF" \
  -d '{"sku":"DEMO-TEE","quantity":5,"reason":"supplier delivery"}'

# 4. create an order (Idempotency-Key required; scope comes from the session)
curl -s -b cookies.txt -X POST localhost:8080/orders \
  -H "Content-Type: application/json" -H "X-CSRF-Token: $CSRF" \
  -H 'Idempotency-Key: demo-1' \
  -d '{"lines":[{"sku":"DEMO-TEE","quantity":2}]}'

# 5. retry the exact request: the original order is returned, no double reservation
curl -s -b cookies.txt -X POST localhost:8080/orders \
  -H "Content-Type: application/json" -H "X-CSRF-Token: $CSRF" \
  -H 'Idempotency-Key: demo-1' \
  -d '{"lines":[{"sku":"DEMO-TEE","quantity":2}]}'

# 6. reuse the key with a different body: 409, no mutation
curl -s -o /dev/null -w '%{http_code}\n' -b cookies.txt -X POST localhost:8080/orders \
  -H "Content-Type: application/json" -H "X-CSRF-Token: $CSRF" \
  -H 'Idempotency-Key: demo-1' \
  -d '{"lines":[{"sku":"DEMO-TEE","quantity":3}]}'

# 7. progress the order
for step in pick pack ship; do
  curl -s -b cookies.txt -X POST "localhost:8080/orders/<order-id>/$step" \
    -H "Content-Type: application/json" -H "X-CSRF-Token: $CSRF" \
    -d "{\"reason\":\"$step\"}"
done

# 8. inspect append-only movements and fulfilment evidence
curl -s -b cookies.txt "localhost:8080/inventory/movements?order_id=<order-id>"
curl -s -b cookies.txt "localhost:8080/orders/<order-id>/events"

# 9. run report-only reconciliation
curl -s -b cookies.txt -X POST localhost:8080/reconciliation/runs \
  -H "X-CSRF-Token: $CSRF"
```

### Endpoints

All routes except the four authentication routes and the health checks require a
valid session. State-changing methods also require `X-CSRF-Token`.

| Method | Path | Notes |
| --- | --- | --- |
| GET | `/` | operator UI (embedded assets) |
| GET | `/healthz` | liveness (anonymous) |
| GET | `/readyz` | database readiness (anonymous) |
| POST | `/auth/login` | `{username,password}`; sets the session cookie (anonymous) |
| GET | `/auth/session` | current session and CSRF token (anonymous) |
| POST | `/auth/logout` | clears the session cookie (session + CSRF) |
| POST | `/skus` | `{code,name,opening_quantity,opening_reason}`; actor from session |
| GET | `/skus` | paginated with `limit`/`offset` |
| GET | `/skus/{id}` | |
| GET | `/skus/{id}/balance` | `on_hand`, `reserved`, `available` |
| POST | `/inventory/receipts` | `{sku,quantity,reason}`; actor from session |
| GET | `/inventory/movements` | filter by `sku_id`, `order_id` |
| GET | `/inventory/balances` | balances joined with SKU code/name |
| POST | `/orders` | requires `Idempotency-Key`; scope from session |
| GET | `/orders` | paginated |
| GET | `/orders/{id}` | |
| GET | `/orders/{id}/events` | fulfilled transition history |
| POST | `/orders/{id}/cancel` | `{reason}`; legal before shipment |
| POST | `/orders/{id}/pick` | `{reason}`; `accepted → picking` |
| POST | `/orders/{id}/pack` | `{reason}`; `picking → packed` |
| POST | `/orders/{id}/ship` | `{reason}`; `packed → shipped` |
| POST | `/reconciliation/runs` | run checks, persist run + findings |
| GET | `/reconciliation/runs` | paginated run list |
| GET | `/reconciliation/runs/{id}` | run with findings |

## Fulfilment state machine

Legal transitions are explicit. Every transition is a conditional
`update … where status = <expected>` inside a transaction that holds the order
row `FOR UPDATE`, so a repeated or racing command has exactly one effect.
Illegal transitions return `409 illegal_transition` and mutate nothing.

| From | To | Command | Preconditions | Side effects |
| --- | --- | --- | --- | --- |
| `accepted` | `picking` | `POST /orders/{id}/pick` | actor required | fulfilment event + audit entry |
| `picking` | `packed` | `POST /orders/{id}/pack` | actor required | fulfilment event + audit entry |
| `packed` | `shipped` | `POST /orders/{id}/ship` | actor required, consistent reservations | release + shipment movements, event + audit |
| `accepted`, `picking`, `packed` | `cancelled` | `POST /orders/{id}/cancel` | actor required, before shipment | release movement + reserved decrement, event + audit |
| any terminal | same | repeat command | — | returns current order, no mutation (replay-safe) |

- A repeat of the request whose state already equals the target returns `200`
  with the current order and writes nothing (idempotent replay).
- Before shipping, the service locks all of the order's items and active
  reservations and requires **exactly one** active reservation per item with
  matching order, SKU, and quantity. A missing, released, altered, duplicate,
  cross-order, or cross-SKU reservation returns `409 reservation_inconsistent`
  and mutates nothing. See `internal/fulfilment/service.go`.
- Shipment first releases the active reservation (`reserved -= qty`) and then
  writes a `shipment` movement (`on_hand -= qty`). Because
  `available = on_hand - reserved >= 0` is a database invariant, and both sides
  decrease by the same quantity, `on_hand` cannot go negative.
- Cancellation racing a shipment: both commands lock the order row, so one
  commits and the other sees the terminal status and returns
  `409 illegal_transition`. Exactly one stock effect occurs.

See `docs/adr/0003-fulfilment-transition-semantics.md`.

## Reconciliation contract

Reconciliation is **report-only**. It runs its checks inside a read-only
transaction, then persists the run and its findings in a separate transaction.
It never updates balances, movements, orders, or reservations, and never issues
a compensating movement. Fixing a real discrepancy is a deliberate operator
action, not something reconciliation may do silently.

Checks performed every run (`checks_run = 6`):

| Check | Detects |
| --- | --- |
| `reserved_matches_active_reservations` | stored `reserved` ≠ sum of `active` reservation quantities per SKU |
| `balance_matches_movement_ledger` | stored `on_hand`/`reserved` ≠ sums of movement deltas, including a missing balance row |
| `terminal_orders_have_no_active_reservation` | an `active` reservation on a `shipped` or `cancelled` order |
| `shipped_line_has_one_matching_decrement` | a shipped line without exactly one shipment movement matching order, item, SKU, and quantity; also flags shipment evidence whose references disagree |
| `manual_movement_has_actor_and_reason` | a `receipt`/`adjustment` movement with a blank actor or reason |
| `balance_values_respect_invariants` | negative `on_hand`/`reserved`/`available`, or `available ≠ on_hand - reserved` |

Every finding records `check_name`, an optional `sku_id`/`order_id`, an
`entity_ref`, and non-blank **expected** and **observed** strings. Runs and
findings are append-only (database triggers reject `UPDATE`/`DELETE`).

The `cmd/reconcile` command runs the checks once and exits: `0` clean, `1`
findings (configurable with `-fail-on-findings=false`), `2` operational error.

```sh
DATABASE_URL='postgres://stockflow:stockflow@localhost:5432/stockflow?sslmode=disable' \
  go run ./cmd/reconcile
```

See `docs/adr/0004-report-only-reconciliation.md`.

## Data integrity constraints

Migration `0004_cross_table_consistency.sql` makes disagreement between
reservation/movement order, item, and SKU references impossible at the database
level:

- `order_items` gains `unique (id, order_id, sku_id)`;
- `reservations` and `inventory_movements` gain a composite foreign key to it,
  so a reservation or movement cannot name a line but carry a different order or
  SKU;
- order-linked movements must name both an order and an item (or neither), and
  only reservation/release/shipment movements may be order-linked.

The migration is forward-only and additive; existing valid data satisfies it.
The trade-off is recorded in `docs/adr/0005-cross-table-consistency.md`.

## Deterministic demo

`cmd/demo` is an assertion-based proof of the vertical slice. It truncates the
database and injects one test-only discrepancy, so it refuses to run unless
`STOCKFLOW_ALLOW_DEMO_FIXTURES=true` is set. It authenticates through the real
login endpoint and exits nonzero on the first failed assertion.

```sh
STOCKFLOW_ALLOW_DEMO_FIXTURES=true \
STOCKFLOW_DEMO_DATABASE_URL='postgres://stockflow:stockflow@localhost:5432/stockflow_test?sslmode=disable' \
  go run ./cmd/demo
```

It proves, in order:

1. a receipt creates exactly one unit of balance;
2. 25 simultaneous buyers for one unit produce one winner and no negative stock;
3. replaying the winner's idempotency key returns the same order without a
   second reservation;
4. pick → pack → ship each returns the expected state;
5. shipment decrements `on_hand` once and clears the reservation;
6. cancelling a shipped order returns `409 illegal_transition` and mutates
   nothing;
7. a seeded reserved-balance discrepancy is reported as `expected=1
   observed=0` and left unrepaired.

## Tests

Every test that touches reservation, fulfilment, reconciliation, or security
correctness runs against **real PostgreSQL**; no mocks are used for concurrency
claims. `go test ./...` **requires** the Compose test database: if it cannot be
reached the integration suite fails hard instead of skipping.

```sh
docker compose up -d postgres
STOCKFLOW_TEST_DATABASE_URL='postgres://stockflow:stockflow@localhost:5432/stockflow_test?sslmode=disable' \
  go test ./...
go vet ./...
```

High-value backend tests include:

1. `TestCancellationVersusShipmentRace` — one coherent terminal outcome and one
   stock effect.
2. `TestDuplicateShipmentCannotDecrementTwice` — concurrent ship commands
   produce one shipment movement and one decrement.
3. `TestReconciliationDetectsOnHandDriftAgainstLedger` and
   `TestReconciliationDetectsMissingBalanceRow` — balance/ledger agreement
   including a missing balance row.
4. `TestReconciliationDetectsShipmentEvidenceCorruption` — missing, duplicate,
   wrong-SKU, wrong-order, and wrong-quantity shipment evidence.
5. `TestShipRejects{Missing,Released,Altered}Reservation` — a corrupt reservation
   returns `409 reservation_inconsistent` and mutates nothing.
6. `TestCrossTableConstraintsRejectMismatchedReferences` — cross-order and
   cross-SKU references are rejected by the database.
7. Security: `TestUnauthenticatedRequestsAreRejected`,
   `TestMutationsRequireCSRF`, `TestForgedActorIsIgnored`,
   `TestForgedCallerScopeIsIgnored`, `TestInvalidSessionRejected`,
   `TestLoginThrottling`, and `TestLogoutClearsSession`.
8. `internal/auth` unit tests cover credential verification, session
   round-trip/tamper/expiry, cookie attributes, and the login limiter.

Some detection tests deliberately bypass the new foreign keys with
`SET session_replication_role = replica` (superuser-only) to insert the legacy
corruption that reconciliation must still catch.

### Frontend tests

```sh
cd web
npm ci
npm run format:check
npm run typecheck
npm test
npm run build
```

The `web` package pins a small React + TypeScript + Vite + Vitest dependency set
and builds into `internal/webui/dist`, which the Go package embeds.

### Browser tests (Playwright + axe)

These run against the **production build** in Compose, not a dev server:

```sh
docker compose up -d --build
cd web && npm ci && npx playwright install chromium
npx playwright test
```

The suite covers login, create/receive stock, create an order, pick/pack/ship,
inspecting events and movements, running reconciliation, a failed mutation
preserving input with an actionable error, ARIA tab keyboard behaviour,
data-table captions, and axe checks on every tab. The fixture fails a test on
any browser console error, uncaught page error, or failed request. Screenshots
can be regenerated with:

```sh
STOCKFLOW_CAPTURE_SCREENSHOTS=true npx playwright test screenshots
```

### Race detector

`go test -race` requires a C toolchain. On Windows the race detector may be
unavailable with a local MinGW GCC toolchain, so a local Windows `-race` build
failure is a toolchain limitation rather than a test result. Linux CI **is
configured** to run `go test -race ./...` against PostgreSQL; that workflow has
not yet been observed on a remote CI run, so no remote race result is claimed
here. The race detector is never silently omitted from the CI configuration.

## Backup, restore, and benchmarks

```sh
scripts/backup-restore-test.sh   # exercised rehearsal: seed, dump, restore, compare counts
scripts/backup.sh                # write a timestamped dump to backups/
benchmarks/run.sh                # hot-SKU contention + reconciliation over a seeded history
```

See `docs/operations.md` and `benchmarks/README.md`. Raw benchmark artifacts and
the recorded environment live in `benchmarks/raw/`; treat those files, not this
README, as the authority for any number.

## Transaction strategy

- `POST /orders`: one transaction normalizes and sorts lines, resolves SKUs,
  locks balance rows with `SELECT … FOR UPDATE` in stable SKU order, validates
  every line, then writes order/items/reservations/movements and completes the
  idempotency record.
- Fulfilment transitions: lock the order row `FOR UPDATE`, verify the current
  status equals the required source, then conditionally update, append events,
  and (for shipment) settle reservations and on-hand in the same transaction.
- Shipment additionally locks all order items and active reservations and
  validates their consistency before any mutation.
- `inventory_balances.available` is a stored generated column with a
  `CHECK (available >= 0)`, so the database refuses to go negative even if
  application logic regresses.
- Deadlocks/serialization failures (`40P01`, `40001`) get at most three attempts
  with jittered backoff. Other errors are not retried.

See `docs/adr/0001-atomic-reservation-locking.md`,
`docs/adr/0002-payload-aware-idempotency.md`,
`docs/adr/0003-fulfilment-transition-semantics.md`,
`docs/adr/0004-report-only-reconciliation.md`, and
`docs/adr/0005-cross-table-consistency.md`.

## Failure behaviour

- Two orders compete for the final unit: one commits, the rest get
  `409 insufficient_stock`.
- A multi-SKU order with an unavailable line writes nothing.
- A retry after a dropped response returns the committed order.
- A reused key with a changed payload returns `409`, no mutation.
- A duplicate shipment command returns the shipped order without a second
  decrement.
- A shipment with inconsistent reservations returns
  `409 reservation_inconsistent` and mutates nothing.
- Cancellation racing shipment lets one win; the loser gets
  `409 illegal_transition`; stock changes once.
- Cancellation after shipment returns `409 illegal_transition` and mutates
  nothing.
- A disconnect before commit leaves no partial order; the idempotency record
  rolls back with it.
- A process death after commit leaves a `completed` idempotency record whose
  response is replayable.
- Movements, fulfilment events, audit entries, and findings cannot be updated
  or deleted; corrections are compensating entries.

## Verification

The exact checks run for this release, their commands, and their raw results
are recorded in [`docs/release-evidence.md`](docs/release-evidence.md). In
short:

```sh
gofmt -l .
go vet ./...
docker compose up -d postgres
STOCKFLOW_TEST_DATABASE_URL='postgres://stockflow:stockflow@localhost:5432/stockflow_test?sslmode=disable' go test ./...
cd web && npm run format:check && npm run typecheck && npm test && npm run build
docker compose up -d --build && cd web && npx playwright test
STOCKFLOW_ALLOW_DEMO_FIXTURES=true STOCKFLOW_DEMO_DATABASE_URL='.../stockflow_test?sslmode=disable' go run ./cmd/demo
scripts/backup-restore-test.sh
benchmarks/run.sh
```

## Known limitations and security

- **Single operator.** There are no roles, multiple users, or per-route
  authorization beyond "authenticated or not". See `docs/security.md`.
- **Stateless session.** Logout clears the cookie client-side; a copied session
  token remains valid until expiry. There is no revocation list.
- **Single-process login throttle.** A multi-replica deployment would multiply
  the effective limit; no shared store is used.
- **Demo mode is insecure by design.** It uses non-secret defaults and a
  non-Secure cookie and must only run on a trusted machine.
- Single physical location, one currency, all-or-nothing orders.
- No payments, returns, partial fulfilment, substitutions, or multiple
  warehouses.
- Hot SKUs serialize on a balance row; this bounds throughput for that SKU (see
  the benchmark).
- PostgreSQL is a single availability boundary; there is no second source of
  truth to fail over to.
- Reconciliation detects only the modelled checks above; it is not a general
  accounting system.
- The frontend dev toolchain reports `npm audit` findings at freeze time: **5
  dev-toolchain advisories (3 moderate, 1 high, 1 critical)** across `vitest`,
  `vite`, `vite-node`, `@vitest/mocker`, and `esbuild`. They affect the Vite dev
  server, the Vitest UI, and the esbuild dev-server transform path, which are
  `devDependencies` used only for local/CI testing and builds; the shipped
  artifact is a static bundle embedded in a distroless Go image with no Node
  runtime and no dev server. This is **not** a claim that the advisories are
  harmless. The exact advisories, scope, and the gated upgrade acceptance checks
  are in
  [`docs/dependency-upgrade-plan.md`](docs/dependency-upgrade-plan.md).
- No metrics dashboards, analytics, or adoption claims are made or implied.

### Scope prohibitions

No Redis, Kafka/broker, SSE/WebSockets, Kubernetes, microservices, payments,
returns, multiple warehouses, allocation strategies, AI, or fake scale claims.

## Repository layout

```text
stockflow/
├── cmd/{api,seed,demo,reconcile}/
├── internal/{auth,catalog,inventory,orders,fulfilment,audit,reconciliation,httpapi,webui}/
├── internal/platform/{apperr,httpx,observability,postgres}/
├── migrations/
├── web/{src,e2e}/
├── tests/integration/
├── benchmarks/{hot_sku,reconcile,raw}/
├── scripts/
├── deploy/postgres/
├── docs/{adr,evidence,screenshots,operations.md,security.md,release-evidence.md,dependency-upgrade-plan.md}
├── compose.yaml
├── Dockerfile
├── Makefile
└── README.md
```
