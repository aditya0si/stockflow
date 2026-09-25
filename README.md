# StockFlow — inventory reservation and fulfilment workbench

StockFlow is an engineering case study in **transactional inventory
correctness**. It asks one question:

> How can an order API reserve limited stock atomically, remain safe under
> retries and races, and preserve enough evidence to explain or detect every
> discrepancy?

This repository currently contains the **V0 executable domain spike**. It is
not a storefront, not a marketplace, and not a production product. There is no
UI and no fulfilment stage yet.

## What exists now

A Go modular monolith (standard library HTTP, `pgx` PostgreSQL driver) with:

| Capability | Status |
| --- | --- |
| SKU creation with optional opening stock | yes |
| Stock receipts (positive quantity, actor, reason) | yes |
| `POST /orders` with canonical hashing and atomic multi-SKU reservation | yes |
| Payload-aware idempotency (`Idempotency-Key` + `X-Caller-Scope`) | yes |
| `POST /orders/{id}/cancel` replay-safe release of active reservations | yes |
| Read endpoints for SKUs, balance, order, movement history | yes |
| Immutable append-only movement ledger | yes (DB trigger) |
| RFC 9457 `application/problem+json` errors | yes |
| Health/readiness endpoints | yes |
| Deterministic seed/reset command | yes |
| Concurrency tests against real PostgreSQL | yes |
| Operator UI, pick/pack/ship, reconciliation, auth | not yet (V1) |

### Modules

- `catalog` — SKU identity and metadata
- `inventory` — balances, receipts, immutable movements
- `orders` — creation, idempotency, atomic reservation, cancellation
- `internal/platform` — PostgreSQL pool/migrations, HTTP problems, logging
- `cmd/api`, `cmd/seed` — process entrypoints

## Quick start

Requires Docker. One command starts PostgreSQL, applies migrations, and serves
the API:

```sh
docker compose up --build -d
docker compose exec api /stockflow-seed
curl -s localhost:8080/healthz
```

The API listens on `http://localhost:8080`. The health check is `GET /healthz`;
`GET /readyz` additionally pings PostgreSQL.

To run the API directly against a local database:

```sh
cp .env.example .env
go run ./cmd/seed        # apply migrations + deterministic demo data
go run ./cmd/api
```

If you cannot use `make`, the equivalent commands are in `Makefile`
(`make db-up`, `make test`, `make reset`, and so on).

## Sample workflow

```sh
# 1. list seeded SKUs and take DEMO-TEE's id
curl -s localhost:8080/skus

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

# 4. retry the exact same request: the original order is returned, no double reservation
curl -s -X POST localhost:8080/orders \
  -H 'Content-Type: application/json' -H 'X-Caller-Scope: operator' \
  -H 'Idempotency-Key: demo-1' \
  -d '{"lines":[{"sku":"DEMO-TEE","quantity":2}]}'

# 5. reuse the key with a different body: 409, no mutation
curl -s -o /dev/null -w '%{http_code}\n' -X POST localhost:8080/orders \
  -H 'Content-Type: application/json' -H 'X-Caller-Scope: operator' \
  -H 'Idempotency-Key: demo-1' \
  -d '{"lines":[{"sku":"DEMO-TEE","quantity":3}]}'

# 6. cancel and observe the released reservation
curl -s -X POST localhost:8080/orders/<order-id>/cancel \
  -H 'Content-Type: application/json' -d '{"actor":"operator","reason":"demo"}'

# 7. inspect the append-only movement ledger
curl -s "localhost:8080/inventory/movements?sku_id=<sku-id>"
```

### Endpoints

| Method | Path | Notes |
| --- | --- | --- |
| GET | `/healthz` | liveness |
| GET | `/readyz` | database readiness |
| POST | `/skus` | `{code,name,actor,opening_quantity,opening_reason}` |
| GET | `/skus` | paginated with `limit`/`offset` |
| GET | `/skus/{id}` | |
| GET | `/skus/{id}/balance` | `on_hand`, `reserved`, `available` |
| POST | `/inventory/receipts` | `{sku,quantity,actor,reason}` |
| GET | `/inventory/movements` | filter by `sku_id`, `order_id` |
| POST | `/orders` | requires `X-Caller-Scope` and `Idempotency-Key` |
| GET | `/orders` | paginated |
| GET | `/orders/{id}` | |
| POST | `/orders/{id}/cancel` | `{actor,reason}`; idempotent |

## Tests

Every test that touches reservation correctness runs against **real
PostgreSQL**; no mocks are used for concurrency claims. `go test ./...`
**requires** the Compose test database: if it cannot be reached the integration
suite fails hard instead of skipping, so a green run always means the real
database was exercised.

```sh
docker compose up -d postgres        # or: make db-up
STOCKFLOW_TEST_DATABASE_URL='postgres://stockflow:stockflow@localhost:5432/stockflow_test?sslmode=disable' \
  go test ./...
go vet ./...
```

`STOCKFLOW_TEST_DATABASE_URL` defaults to that Compose test database, so with
the container running `go test ./...` needs no extra environment.

What the critical tests prove:

1. `TestOneUnitFiftyBuyers` — fifty concurrent orders for one unit yield a
   single `201`; the persisted order, item, reservation, and movement match
   that response and no balance goes negative.
2. `TestIdempotentRetryReturnsOriginalOrder` — same key and body replays the
   original order without a second reservation.
3. `TestIdempotencyPayloadConflict` — same key, changed body returns `409` and
   mutates nothing.
4. `TestMultiSKURollback` — one unavailable line rolls back the order row,
   items, reservations, movements, and the idempotency record.
5. `TestRepeatedCancellationReleasesOnce` — cancelling twice is replay-safe:
   the release movement is persisted once and no active reservation remains.

Additional coverage: concurrent same-key requests collapse to one order;
opposite-direction multi-SKU contention (`[A,B]` versus `[B,A]`) leaves durable
balances, orders, items, and reservation counts correct with no partial writes;
concurrent cancellation of one order produces coherent responses and releases
stock once; the movement ledger rejects `UPDATE`/`DELETE`; duplicate lines are
normalized; and problem responses use the documented codes.

### Race detector

`go test -race` requires a C toolchain. On Windows the race detector may be
unavailable with the local MinGW GCC toolchain, so a local Windows `-race`
build failure is a toolchain limitation rather than a test result. CI runs
`go test -race ./...` against the Compose database on Linux; the race detector
is never silently omitted.

## Transaction strategy

- `POST /orders`: one transaction normalizes and sorts lines, resolves SKUs,
  locks balance rows with `SELECT … FOR UPDATE` in stable SKU order, validates
  every line, then writes order/items/reservations/movements and completes the
  idempotency record.
- `POST /orders/{id}/cancel`: locks the order `FOR UPDATE`, releases every
  `active` reservation, appends `release` movements, decrements `reserved`, and
  marks the order cancelled. Repeating the call is a no-op.
- `inventory_balances.available` is a stored generated column with a
  `CHECK (available >= 0)`, so the database refuses to go negative even if
  application logic regresses.
- Deadlocks/serialization failures (`40P01`, `40001`) get at most three
  attempts with jittered backoff. Other errors are not retried.

See `docs/adr/0001-atomic-reservation-locking.md` and
`docs/adr/0002-payload-aware-idempotency.md`.

## Failure behaviour

- Two orders compete for the final unit: one commits, the rest get
  `409 insufficient_stock`.
- A multi-SKU order with an unavailable line writes nothing.
- A retry after a dropped response returns the committed order.
- A reused key with a changed payload returns `409`, no mutation.
- A duplicate shipment command is not applicable in V0 (no fulfilment yet).
- A disconnect before commit leaves no partial order; the idempotency record
  rolls back with it.
- A process death after commit leaves a `completed` idempotency record whose
  response is replayable.
- Movements cannot be updated or deleted; corrections would be compensating
  entries.

## Non-goals (V0)

No storefront, payments, multiple warehouses, allocation strategies, Redis
inventory, Kafka/Redpanda, outbox, SSE/WebSockets, microservices, Kubernetes,
offline writes, returns, AI, or dashboards. PostgreSQL is the only durable
authority.

## Known limitations

- Single physical location, one currency, all-or-nothing orders.
- No pick/pack/ship or reconciliation yet (planned for V1).
- Authentication is demo-grade: `X-Caller-Scope` is caller-asserted.
- Hot SKUs serialize on a balance row; this bounds throughput for that SKU.
- PostgreSQL is a single availability boundary; there is no second source of
  truth to fail over to.
- Benchmarks are not included in V0, so no performance numbers are claimed.

## Repository layout

```text
stockflow/
├── cmd/{api,seed}/
├── internal/{catalog,inventory,orders,httpapi}/
├── internal/platform/{apperr,httpx,observability,postgres}/
├── migrations/
├── tests/integration/
├── deploy/postgres/
├── docs/adr/
├── compose.yaml
├── Dockerfile
├── Makefile
└── README.md
```
