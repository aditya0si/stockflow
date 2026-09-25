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

**Shipped:** V1 (useful product). **Not built:** V2 hardening and anything in
the V3/scope-prohibition list below.

## What exists now (V1)

A Go modular monolith (standard library HTTP, `pgx` PostgreSQL driver) that
serves a small React/TypeScript operator UI from the same binary:

| Capability | Status |
| --- | --- |
| SKU creation with optional opening stock | shipped |
| Stock receipts (positive quantity, actor, reason) | shipped |
| `POST /orders` with canonical hashing and atomic multi-SKU reservation | shipped |
| Payload-aware idempotency (`Idempotency-Key` + `X-Caller-Scope`) | shipped |
| Cancellation, legal only before shipment, replay-safe | shipped |
| Fulfilment state machine `accepted → picking → packed → shipped` | shipped |
| Shipment releases reservation, decrements `on_hand` exactly once | shipped |
| Append-only movement ledger and fulfilment/audit evidence | shipped |
| Report-only reconciliation runs with persisted findings | shipped |
| Operator UI: stock, receipts, orders, pick/pack/ship, cancel, reconciliation | shipped |
| Deterministic assertion-based demo (exits nonzero on failure) | shipped |
| Health/readiness endpoints, RFC 9457 problems | shipped |
| Concurrency/race tests against real PostgreSQL | shipped |
| Benchmarks, role-based auth, second location | not built (V2/V3) |

### Modules

- `catalog` — SKU identity and metadata
- `inventory` — balances, receipts, immutable movements
- `orders` — creation, idempotency, atomic reservation, cancellation
- `fulfilment` — pick/pack/ship state machine and fulfilment evidence
- `audit` — actor/action history, fulfilment events, append-only triggers
- `reconciliation` — report-only checks, runs, and findings
- `webui` — embedded operator UI
- `internal/platform` — PostgreSQL pool/migrations, HTTP problems, logging
- `cmd/api`, `cmd/seed`, `cmd/demo`, `cmd/reconcile` — process entrypoints

## Quick start

Requires Docker. One command builds the frontend, embeds it in the Go binary,
starts PostgreSQL, applies migrations, and serves API + UI:

```sh
docker compose up --build -d
docker compose exec api /stockflow-seed
curl -s localhost:8080/healthz
```

Open `http://localhost:8080/` for the operator UI. The API listens on the same
address; `GET /healthz` is liveness and `GET /readyz` additionally pings
PostgreSQL.

To run locally without the app container:

```sh
cp .env.example .env
cd web && npm ci && npm run build && cd ..   # build the embedded UI
go run ./cmd/seed                             # apply migrations + demo data
go run ./cmd/api
```

Without the frontend build the Go binary still compiles and serves API routes;
the UI route explains how to build the assets.

If you cannot use `make`, the equivalent commands are in the `Makefile`
(`make db-up`, `make test`, `make web-test`, `make demo`, and so on).

## Operator UI

The UI is a single compact screen with three tabs, served from the Go binary:

- **Stock** — SKU balances (`on_hand`, `reserved`, `available`), receive stock
  with actor/reason, create SKUs.
- **Orders** — submit an all-or-nothing order, select an order to view its
  state, items, movements, and fulfilment events, then pick → pack → ship or
  cancel before shipment.
- **Reconciliation** — run a report and inspect persisted findings
  (check name, reference, expected vs observed).

It is responsive down to a mobile viewport (single-column layout, horizontally
scrollable tables), has a skip link, labelled form controls, a visible
`:focus-visible` outline, ARIA tab/tabpanel wiring, and `aria-live` notices.
There is no offline write queue: the UI re-reads authoritative server state.

### Sample operator path

Via the UI: create/receive a SKU on **Stock**, submit an order on **Orders**,
select it, click the next action (`pick`, then `pack`, then `ship`), and inspect
the movement/event history. On **Reconciliation**, click **Run report**.

The same journey over HTTP:

```sh
# 1. list SKUs and their balances
curl -s localhost:8080/inventory/balances

# 2. receive stock (reason is mandatory)
curl -s -X POST localhost:8080/inventory/receipts \
  -H 'Content-Type: application/json' \
  -d '{"sku":"DEMO-TEE","quantity":5,"actor":"owner","reason":"supplier delivery"}'

# 3. create an order (both headers are required)
curl -s -X POST localhost:8080/orders \
  -H 'Content-Type: application/json' \
  -H 'X-Caller-Scope: operator' \
  -H 'Idempotency-Key: demo-1' \
  -d '{"lines":[{"sku":"DEMO-TEE","quantity":2}]}'

# 4. retry the exact request: the original order is returned, no double reservation
curl -s -X POST localhost:8080/orders \
  -H 'Content-Type: application/json' -H 'X-Caller-Scope: operator' \
  -H 'Idempotency-Key: demo-1' \
  -d '{"lines":[{"sku":"DEMO-TEE","quantity":2}]}'

# 5. reuse the key with a different body: 409, no mutation
curl -s -o /dev/null -w '%{http_code}\n' -X POST localhost:8080/orders \
  -H 'Content-Type: application/json' -H 'X-Caller-Scope: operator' \
  -H 'Idempotency-Key: demo-1' \
  -d '{"lines":[{"sku":"DEMO-TEE","quantity":3}]}'

# 6. progress the order (actor is required)
curl -s -X POST localhost:8080/orders/<order-id>/pick \
  -H 'Content-Type: application/json' -d '{"actor":"operator","reason":"start pick"}'
curl -s -X POST localhost:8080/orders/<order-id>/pack \
  -H 'Content-Type: application/json' -d '{"actor":"operator","reason":"packed"}'
curl -s -X POST localhost:8080/orders/<order-id>/ship \
  -H 'Content-Type: application/json' -d '{"actor":"operator","reason":"shipped"}'

# 7. cancellation before shipment is legal; after shipment it returns 409
curl -s -X POST localhost:8080/orders/<order-id>/cancel \
  -H 'Content-Type: application/json' -d '{"actor":"operator","reason":"customer cancelled"}'

# 8. inspect append-only movements and fulfilment evidence
curl -s "localhost:8080/inventory/movements?order_id=<order-id>"
curl -s "localhost:8080/orders/<order-id>/events"

# 9. run report-only reconciliation
curl -s -X POST localhost:8080/reconciliation/runs
curl -s "localhost:8080/reconciliation/runs?limit=5"
```

### Endpoints

| Method | Path | Notes |
| --- | --- | --- |
| GET | `/` | operator UI (embedded assets) |
| GET | `/healthz` | liveness |
| GET | `/readyz` | database readiness |
| POST | `/skus` | `{code,name,actor,opening_quantity,opening_reason}` |
| GET | `/skus` | paginated with `limit`/`offset` |
| GET | `/skus/{id}` | |
| GET | `/skus/{id}/balance` | `on_hand`, `reserved`, `available` |
| POST | `/inventory/receipts` | `{sku,quantity,actor,reason}` |
| GET | `/inventory/movements` | filter by `sku_id`, `order_id` |
| GET | `/inventory/balances` | balances joined with SKU code/name |
| POST | `/orders` | requires `X-Caller-Scope` and `Idempotency-Key` |
| GET | `/orders` | paginated |
| GET | `/orders/{id}` | |
| GET | `/orders/{id}/events` | fulfilled transition history |
| POST | `/orders/{id}/cancel` | `{actor,reason}`; legal before shipment |
| POST | `/orders/{id}/pick` | `{actor,reason}`; `accepted → picking` |
| POST | `/orders/{id}/pack` | `{actor,reason}`; `picking → packed` |
| POST | `/orders/{id}/ship` | `{actor,reason}`; `packed → shipped` |
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
| `packed` | `shipped` | `POST /orders/{id}/ship` | actor required | release movement + reserved decrement, shipment movement + on_hand decrement, event + audit |
| `accepted`, `picking`, `packed` | `cancelled` | `POST /orders/{id}/cancel` | actor required, before shipment | release movement + reserved decrement, event + audit |
| any terminal | same | repeat command | — | returns current order, no mutation (replay-safe) |

- A repeat of the request whose state already equals the target returns `200`
  with the current order and writes nothing (idempotent replay).
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

Checks performed every run:

| Check | Detects |
| --- | --- |
| `reserved_matches_active_reservations` | stored `reserved` ≠ sum of `active` reservation quantities per SKU |
| `terminal_orders_have_no_active_reservation` | an `active` reservation on a `shipped` or `cancelled` order |
| `shipped_line_has_one_decrement` | a shipped line without exactly one `shipment` movement of the right size |
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

## Deterministic demo

`cmd/demo` is an assertion-based proof of the V1 vertical slice. It truncates
the database and injects one test-only discrepancy, so it refuses to run unless
`STOCKFLOW_ALLOW_DEMO_FIXTURES=true` is set. It exits nonzero on the first
failed assertion.

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

### Screenshots

No screenshots are committed, because they must be generated from the running
UI rather than fabricated. To capture them:

1. `docker compose up --build -d && docker compose exec api /stockflow-seed`
2. open `http://localhost:8080/` and, for each tab, capture at desktop width and
   at a ~390px mobile viewport;
3. capture the order detail for a shipped order (items, movements, events) and a
   reconciliation run with a finding;
4. store the files under `docs/screenshots/` and reference them here only after
   they have been captured from a real session.

## Tests

Every test that touches reservation, fulfilment, or reconciliation correctness
runs against **real PostgreSQL**; no mocks are used for concurrency claims.
`go test ./...` **requires** the Compose test database: if it cannot be reached
the integration suite fails hard instead of skipping, so a green run always
means the real database was exercised.

```sh
docker compose up -d postgres        # or: make db-up
STOCKFLOW_TEST_DATABASE_URL='postgres://stockflow:stockflow@localhost:5432/stockflow_test?sslmode=disable' \
  go test ./...
go vet ./...
```

`STOCKFLOW_TEST_DATABASE_URL` defaults to that Compose test database, so with
the container running `go test ./...` needs no extra environment.

V1 high-value tests:

1. `TestCancellationVersusShipmentRace` — cancellation and shipment race to one
   coherent terminal outcome and exactly one stock effect.
2. `TestDuplicateShipmentCannotDecrementTwice` — eight concurrent ship commands
   produce one shipment movement and one decrement.
3. `TestIllegalTransitionLeavesDomainTablesUnchanged` — illegal moves return
   `409` and leave orders/items/reservations/movements/events/audit unchanged.
4. `TestReconciliationDetectsCorruptedReservedBalance` — a corrupted `reserved`
   is reported with expected vs observed values.
5. `TestReconciliationDoesNotRepairCorruptedRow` — reconciliation reports but
   does not repair, and persists the run/findings.
6. `TestOperatorJourneyReceiptOrderPickPackShip` — receipt → order → pick →
   pack → ship with visible evidence and ledger entries.
7. Existing V0 tests (`TestOneUnitFiftyBuyers`,
   `TestIdempotentRetryReturnsOriginalOrder`, `TestIdempotencyPayloadConflict`,
   `TestMultiSKURollback`, `TestRepeatedCancellationReleasesOnce`, and the
   concurrent same-key/cancellation/ledger tests) remain green.

Additional coverage: the movement ledger rejects `UPDATE`/`DELETE`; duplicate
lines normalize; problem responses use the documented codes; the fulfilment
transition table is unit-tested.

### Frontend tests

```sh
cd web
npm ci
npm run format:check
npm run typecheck
npm test
npm run build
```

The `web` package pins a small React + TypeScript + Vite + Vitest dependency
set. `npm run build` writes into `internal/webui/dist`, which the Go package
embeds.

### Race detector

`go test -race` requires a C toolchain. On Windows the race detector may be
unavailable with a local MinGW GCC toolchain, so a local Windows `-race` build
failure is a toolchain limitation rather than a test result. CI runs
`go test -race ./...` against the Compose database on Linux; the race detector
is never silently omitted.

## Transaction strategy

- `POST /orders`: one transaction normalizes and sorts lines, resolves SKUs,
  locks balance rows with `SELECT … FOR UPDATE` in stable SKU order, validates
  every line, then writes order/items/reservations/movements and completes the
  idempotency record.
- Fulfilment transitions: lock the order row `FOR UPDATE`, verify the current
  status equals the required source, then conditionally update, append events,
  and (for shipment) settle reservations and on-hand in the same transaction.
- `POST /orders/{id}/cancel`: locks the order `FOR UPDATE`, rejects shipment,
  releases every `active` reservation, appends `release` movements, decrements
  `reserved`, and marks the order cancelled. Repeating the call is a no-op.
- `inventory_balances.available` is a stored generated column with a
  `CHECK (available >= 0)`, so the database refuses to go negative even if
  application logic regresses.
- Deadlocks/serialization failures (`40P01`, `40001`) get at most three
  attempts with jittered backoff. Other errors are not retried.

See `docs/adr/0001-atomic-reservation-locking.md`,
`docs/adr/0002-payload-aware-idempotency.md`,
`docs/adr/0003-fulfilment-transition-semantics.md`, and
`docs/adr/0004-report-only-reconciliation.md`.

## Failure behaviour

- Two orders compete for the final unit: one commits, the rest get
  `409 insufficient_stock`.
- A multi-SKU order with an unavailable line writes nothing.
- A retry after a dropped response returns the committed order.
- A reused key with a changed payload returns `409`, no mutation.
- A duplicate shipment command returns the shipped order without a second
  decrement.
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

## Known limitations and security

- **Authentication is demo-grade.** `X-Caller-Scope` is caller-asserted; there
  is no login, session, token, or per-route authorization. Actors on receipts
  and transitions are also caller-supplied. Anyone who can reach the port can
  mutate stock. Do not expose this to an untrusted network.
- **No role separation.** Owner/operator/worker roles are not enforced; the UI
  uses a plain string scope.
- Single physical location, one currency, all-or-nothing orders.
- No payments, returns, partial fulfilment, substitutions, or multiple
  warehouses.
- Hot SKUs serialize on a balance row; this bounds throughput for that SKU.
- PostgreSQL is a single availability boundary; there is no second source of
  truth to fail over to.
- Reconciliation detects only the modelled checks above; it is not a general
  accounting system.
- No benchmarks, load tests, or performance numbers are claimed in V1.
- No metrics dashboards, analytics, or adoption claims are made or implied.

### Future work (V2 and V3)

V2 (measured hardening): role distinction, indexed/paginated queries,
structured logs/correlation IDs, backup/restore rehearsal, accessibility pass,
fault tests, and committed benchmark artifacts. V3 (one evidence-driven
extension): at most one of returns, a second location, notification outbox,
barcode input, or SSE — only after real use or measurement justifies it.

### Scope prohibitions

No Redis, broker/outbox, SSE/WebSockets, microservices, Kubernetes, payments,
returns, multiple warehouses, allocation strategies, recommendations, AI,
offline writes, or generic dashboard metrics.

## Repository layout

```text
stockflow/
├── cmd/{api,seed,demo,reconcile}/
├── internal/{catalog,inventory,orders,fulfilment,audit,reconciliation,httpapi,webui}/
├── internal/platform/{apperr,httpx,observability,postgres}/
├── migrations/
├── web/{src,index.html,vite.config.ts}/
├── tests/integration/
├── deploy/postgres/
├── docs/adr/
├── compose.yaml
├── Dockerfile
├── Makefile
└── README.md
```
