# ADR 0001: Atomic reservation with ordered row locks

- Status: accepted
- Date: 2026-09-26
- Scope: V0

## Context

An order is all-or-nothing. Two buyers can observe the same last unit, and a
multi-SKU order can be partially available. The system must reserve stock
atomically and must never let `on_hand - reserved` become negative.

## Decision

`POST /orders` runs in one PostgreSQL transaction that:

1. normalizes and sums duplicate SKU lines, then sorts them by SKU code;
2. resolves SKU codes to IDs and sorts the resulting IDs;
3. locks the relevant `inventory_balances` rows with
   `select ... where sku_id = any($1) order by sku_id for update`;
4. validates that every line has enough `available` before writing anything;
5. inserts the order, order items, active reservations, and `reservation`
   movements, and increments `reserved` for each SKU;
6. completes the idempotency record in the same transaction.

If any line is unavailable, the whole transaction rolls back and the API
returns a `409 insufficient_stock` problem with per-line availability.

Database constraints back the logic:

- `available` is a stored generated column `on_hand - reserved`;
- `check (on_hand >= 0)`, `check (reserved >= 0)`,
  `check (available >= 0)`;
- a partial unique index allows only one `active` reservation per order item;
- `unique (order_id, sku_id)` on `order_items` enforces normalization.

## Alternatives rejected

- **Optimistic versioning:** more application retry code for the same hot-row
  serialization, with no demonstrated throughput gain at this scale.
- **SERIALIZABLE isolation:** correct but forces broader serialization-failure
  handling; explicit row locks keep the behaviour easy to inspect.
- **Redis counters:** a second source of truth to reconcile; rejected in
  favour of PostgreSQL as the sole durable authority.
- **Read-then-write without locks:** cannot decide the last unit safely.

## Consequences

- Hot SKUs serialize on their balance row. This is accepted for a single
  location and documented in the README limitations.
- Stable lock ordering (sort by SKU ID before locking) reduces deadlock risk.
  Recognized transient deadlock/serialization errors (`40P01`, `40001`) get a
  bounded retry (3 attempts) with jittered backoff; other errors do not.
- Validation happens after all locks are held, so a failed multi-SKU order
  writes nothing.
