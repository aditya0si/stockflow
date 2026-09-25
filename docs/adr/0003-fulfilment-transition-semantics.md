# ADR 0003: Fulfilment transition semantics

- Status: accepted
- Date: 2026-09-26
- Scope: V1

## Context

An accepted order must move through picking and packing before it is shipped.
Shipment is the point where reserved stock stops being reserved and physically
leaves the shelf, so it must be observable, replay-safe, and race-safe:

- a client can retry a command whose response was lost;
- a cancellation can race a shipment;
- two shipment commands can arrive concurrently;
- an out-of-order command (pack before pick, ship before pack, cancel after
  ship) must not silently corrupt state.

V0 modelled only `accepted` and `cancelled`. V1 adds `picking`, `packed`, and
`shipped` without rewriting existing rows.

## Decision

The legal state machine is a linear path plus a pre-shipment cancellation edge:

```text
accepted --pick--> picking --pack--> packed --ship--> shipped
   |                  |               |
   +------cancel------+----cancel-----+--> cancelled   (pre-shipment only)
```

Each command is implemented as a conditional update guarded by the expected
source status:

```sql
update orders set status = $target, ... where id = $id and status = $expected
```

The order row is locked with `SELECT … FOR UPDATE` at the start of the
transaction, so concurrent commands serialize on that row. The command that
loses observes the new status and returns
`409 illegal_transition` (or replays if the status already equals its target).

Shipment, in one transaction:

1. locks the order and requires `packed`;
2. locks the order's `active` reservations and the affected balance rows in
   stable SKU order;
3. marks every reservation `released` and appends a `release` movement that
   decrements `reserved`;
4. appends a `shipment` movement that decrements `on_hand` once per line;
5. updates the order to `shipped` with `shipped_at`;
6. appends a `fulfilment_events` row and an `audit_entries` row.

A unique index on `fulfilment_events (order_id, to_status)` plus the guarded
update make the linear machine visit each target status at most once. Repeating
a command whose target is already current returns the order unchanged without
writing another event, movement, or audit row.

Because `available = on_hand - reserved` is a database `CHECK (available >= 0)`
and shipment decreases both `on_hand` and `reserved` by the same quantity,
`on_hand` cannot become negative.

## Alternatives rejected

- **Free-form status assignment from the request body:** lets a caller jump
  states and makes illegal-transition tests meaningless.
- **Optimistic version column instead of `FOR UPDATE`:** the order row is a
  single hot row per order, so row locking is simpler and needs less retry code.
- **A separate `shipments` table as the source of truth:** duplicates evidence
  already carried by the movement ledger and fulfilment events; the ledger is
  the append-only authority.
- **Decrementing `on_hand` without releasing `reserved`:** leaves a phantom
  reservation and breaks `available = on_hand - reserved`.
- **Cascade-cancelling a shipped order:** shipment is terminal; arriving at
  `shipped` and then `cancelled` would imply stock was returned, which V1 does
  not model.

## Consequences

- Cancellation and shipment cannot both succeed: the order row serializes them,
  yielding one terminal status and one stock effect.
- Duplicate/concurrent shipment commands coalesce to one decrement.
- Illegal commands return a stable RFC 9457 `409 illegal_transition` problem
  with `from_status` and `to_status` extensions and perform no mutation.
- `orders.shipped_at` and `fulfilment_events` form durable evidence for later
  reconciliation and operator inspection.
- The migration is forward-only: it drops and replaces the V0 status checks and
  adds `shipped_at`, without altering historical rows.
