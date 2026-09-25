-- V2: make reservation and movement order/item/SKU references mutually
-- consistent at the database level, so they cannot disagree even if
-- application logic regresses.
--
-- Forward-only and additive: it adds constraints and unique keys, and does not
-- rewrite or delete existing rows. Valid V1 data already satisfies every
-- constraint, because the only writers set these columns together.
--
-- A composite foreign key is the lightest tool that makes disagreement
-- impossible: it needs a unique key on the referenced columns. We add that key
-- on order_items and reference it from reservations and inventory_movements.
-- This is preferred over hand-written triggers because PostgreSQL enforces it
-- on every write path, including ad-hoc SQL, without application cooperation.
-- The trade-off (extra indexes, a slightly larger write cost on the hot
-- reservation path, and no way to record a deliberately inconsistent
-- historical row) is recorded in docs/adr/0005.

-- A reservation or movement may reference an order item by all three columns
-- at once. (id alone is already unique, so this adds an index, not semantics.)
alter table order_items
    add constraint order_items_identity_unique unique (id, order_id, sku_id);

-- A reservation's order and SKU must equal the referenced order item's.
alter table reservations
    add constraint reservations_item_identity_fk
    foreign key (order_item_id, order_id, sku_id)
    references order_items (id, order_id, sku_id);

-- An order-linked movement must reference both an order and an item, or
-- neither: a movement cannot point at an order without naming the line.
alter table inventory_movements
    add constraint inventory_movements_order_pair check (
        (order_id is null) = (order_item_id is null)
    );

-- Reservation, release, and shipment movements are always order-item effects;
-- receipt and adjustment movements are never linked to an order.
alter table inventory_movements
    add constraint inventory_movements_order_kind check (
        (kind in ('reservation', 'release', 'shipment')
            and order_id is not null and order_item_id is not null)
        or (kind in ('receipt', 'adjustment')
            and order_id is null and order_item_id is null)
    );

-- A movement's order and SKU must equal the referenced order item's. Columns
-- are skipped by the FK when order_item_id is null (manual movements), which
-- the previous check already restricts to order-free movement kinds.
alter table inventory_movements
    add constraint inventory_movements_item_identity_fk
    foreign key (order_item_id, order_id, sku_id)
    references order_items (id, order_id, sku_id);

-- Note: orders.idempotency_record_id -> idempotency_records(id) and
-- idempotency_records.order_id -> orders(id) are single-column foreign keys and
-- cannot disagree, so they need no extra composite constraint here.
