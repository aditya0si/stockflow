# ADR 0005: Cross-table reference consistency

- Status: accepted
- Date: 2026-09-26
- Scope: V2

## Context

Three tables describe the same reservation fact:

- `order_items` owns a line: `(id, order_id, sku_id, quantity)`;
- `reservations` names a line and repeats `order_id` and `sku_id` so it can be
  queried without a join;
- `inventory_movements` names a line and repeats `order_id` and `sku_id` for the
  same reason.

In V1 each column had its own single-column foreign key. PostgreSQL therefore
accepted a reservation or movement that pointed at line `L` but carried a
different `order_id` or `sku_id` than `L`. Application code set them together,
but nothing at the database level prevented an ad-hoc `UPDATE`, a future code
path, or a manual intervention from making them disagree. Reconciliation could
detect some of this, but only after the fact.

The V2 brief requires that reservation and movement order/item/SKU references
cannot disagree, while preserving existing valid data.

## Decision

Add forward-only composite constraints in `0004_cross_table_consistency.sql`:

1. `order_items` gains `unique (id, order_id, sku_id)` so it can be referenced by
   all three columns.
2. `reservations` gains
   `foreign key (order_item_id, order_id, sku_id) references order_items (id, order_id, sku_id)`.
3. `inventory_movements` gains the same composite foreign key plus two checks:
   - `(order_id is null) = (order_item_id is null)` — a linked movement names
     both, a manual one names neither;
   - reservation/release/shipment movements must be order-linked, while
     receipt/adjustment movements must not be.

A composite foreign key is preferred over a hand-written trigger because
PostgreSQL enforces it on every write path, including direct SQL, with no
application cooperation. `MATCH SIMPLE` skips the constraint when
`order_item_id` is null, which is exactly the manual-movement case the new check
already restricts.

## Consequences

- A reservation or movement whose `order_id` or `sku_id` disagrees with its line
  is rejected by the database (`23503`), not merely reported later.
- Existing valid data satisfies the constraints, because the only V1 writers set
  these columns together; the migration neither rewrites nor deletes rows.
- The `order_items` index grows slightly and the reservation insert path pays
  for one extra index/fk check. This is negligible beside the existing
  `inventory_balances` lock that serializes a hot SKU, and it is a correctness
  win on the path that matters.
- Reconciliation still checks order/line/SKU/quantity agreement for shipment
  evidence. Constraints prevent new corruption; the reconciliation check is the
  detection mechanism for legacy rows or rows written outside the constraints
  (for example, by a superuser with `session_replication_role = replica`, which
  integration tests use deliberately to prove detection works).
- A deliberately inconsistent historical row cannot be inserted without
  disabling replication triggers as a superuser. That is the intended trade-off:
  easy to prevent, hard to fake.
