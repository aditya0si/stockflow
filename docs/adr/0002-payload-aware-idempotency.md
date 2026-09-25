# ADR 0002: Payload-aware idempotency for order creation

- Status: accepted
- Date: 2026-09-26
- Scope: V0

## Context

A client can time out after the server has committed an order. Retrying must
return the committed outcome instead of creating a second order. A client can
also reuse a key with a different body, which must not silently return a
mismatched response or mutate stock. Concurrent retries of the same request
must not create two orders.

## Decision

`POST /orders` requires `Idempotency-Key` and an authenticated session. The
scope is derived from the authenticated operator identity (V2), not from a
caller-supplied `X-Caller-Scope` header, so two operators cannot collide on a
key or forge another's scope. Inside the same transaction as the reservation,
the service writes an `idempotency_records` row keyed by `unique (scope, key)`
that stores:

- the canonical request hash,
- state (`in_progress` or `completed`),
- the definitive HTTP status and JSON response body.

The request hash is computed over the canonicalized lines (duplicates summed,
sorted by SKU), so line reordering does not change the outcome.

Acquisition logic:

1. `insert ... on conflict (scope, key) do nothing returning ...`.
   If this inserts, we own the key.
2. On conflict, `select ... for update` the existing row. The unique-index
   conflict blocks a concurrent duplicate until the first transaction commits
   or rolls back, so this lock observes a final state.
   - different `request_hash` -> `409 idempotency_key_reuse`, no mutation;
   - `completed` -> return the stored status and body (the original order);
3. The record is marked `completed` with the response in the same transaction
   that creates the order.

## Alternatives rejected

- **In-memory key cache:** lost on restart and not shared; cannot survive
  "process dies after commit."
- **Redis idempotency store:** a second durable authority the spec forbids.
- **Separate request/response tables with status polling:** more moving parts
  for the same guarantee.
- **Hashing raw JSON:** reordering or equivalent duplicate lines would produce
  false conflicts.

## Consequences

- The idempotency record lifecycle is atomic with the mutation. If the server
  dies before commit, the record and order roll back together; there are no
  abandoned `in_progress` rows to garbage-collect.
- A concurrent duplicate waits for the first transaction rather than failing,
  so concurrent retries all return the same order.
- The stored response is a capture of the committed outcome. If the response
  schema changes, old records still replay the bytes that were returned
  originally. This is intentional: the promise is a stable committed outcome,
  not a delivery guarantee.
