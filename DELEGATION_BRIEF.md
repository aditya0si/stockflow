# Delegation brief — StockFlow V0

## Goal
Build the first verified vertical slice of StockFlow in a new repository. This stage must prove inventory reservation correctness under contention. Do not build optional portfolio theatre.

## Read first
1. `C:/Users/oliad/Desktop/Resume/role-tailored-2026-09-25/specs/SPEC_myntra_stockflow.md`
2. This brief.

## Working repository
`C:/Users/oliad/Desktop/stockflow`

## Required deliverable
A runnable Go modular-monolith backend with PostgreSQL that supports:

1. Migrations for SKUs, inventory balances, immutable inventory movements, orders, order items, reservations, and idempotency records.
2. A stock-receipt operation with positive quantity, actor, and reason.
3. `POST /orders` with:
   - required caller scope and `Idempotency-Key`;
   - canonical request hashing;
   - same key + same payload returns the original response;
   - same key + different payload returns HTTP 409;
   - duplicate SKU lines normalized;
   - all-or-nothing multi-SKU reservation;
   - stable SKU lock ordering and PostgreSQL row locking;
   - domain conflict when any SKU lacks availability.
4. `POST /orders/{id}/cancel` that releases each active reservation once.
5. Read endpoints sufficient to inspect SKU balance, order, and movement history.
6. Database CHECK/UNIQUE/FK constraints that enforce the important invariants.
7. RFC 9457-style JSON errors.
8. Health endpoint.
9. Docker Compose for app + PostgreSQL, Makefile or equivalent commands, `.env.example`, and deterministic seed/demo script.
10. Tests against real PostgreSQL, including the five critical V0 tests below.

## Five critical tests
1. Fifty simultaneous order attempts for one available unit produce exactly one accepted order and no negative balance.
2. Same idempotency key and same body returns the original order without a second reservation.
3. Same idempotency key with changed body returns 409 and performs no mutation.
4. Multi-SKU order with one unavailable item rolls back every reservation and order write.
5. Repeating cancellation does not release stock twice.

## Architecture constraints
- Go modular monolith.
- PostgreSQL is the sole durable authority.
- Use explicit SQL or a thin query layer where transaction/locking behavior must remain visible.
- Keep domain code organized by capability (`catalog`, `inventory`, `orders`, platform adapters), not global MVC folders.
- Prefer standard library plus small, justified dependencies.
- Add no frontend in V0.

## DO NOT BUILD
Redis, Kafka/Redpanda, outbox, SSE/WebSockets, microservices, Kubernetes, returns, multiple warehouses, allocation strategies, payments, recommendations, AI, fake dashboards, or unmeasured performance claims.

## Documentation
README must state what exists now, why it exists, exact local setup, sample curl workflow, test commands, transaction strategy, failure behavior, non-goals, and known limitations. Add ADRs for reservation locking and idempotency behavior. Do not describe future features as shipped.

## Quality rules
- Run `gofmt`, `go vet`, unit tests, and PostgreSQL integration tests.
- Avoid mocks for reservation correctness.
- Never claim tests passed unless the command actually ran.
- Do not weaken assertions to make tests green.
- Do not fabricate benchmark numbers.
- Keep generated/build artifacts out of git.

## Git
Make coherent commits. Final commit message for the completed V0 should be:
`feat: build StockFlow transactional reservation core`

## Completion report
Return:
- files and architecture created;
- exact commands run and their real results;
- commit hash;
- remaining known limitations or failures.
