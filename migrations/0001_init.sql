create table skus (
    id uuid primary key,
    code text not null,
    name text not null,
    created_at timestamptz not null default now(),
    constraint skus_code_unique unique (code),
    constraint skus_code_not_blank check (btrim(code) <> ''),
    constraint skus_name_not_blank check (btrim(name) <> '')
);

create table inventory_balances (
    sku_id uuid primary key references skus (id) on delete restrict,
    on_hand integer not null default 0,
    reserved integer not null default 0,
    available integer generated always as (on_hand - reserved) stored,
    updated_at timestamptz not null default now(),
    constraint inventory_balances_on_hand_non_negative check (on_hand >= 0),
    constraint inventory_balances_reserved_non_negative check (reserved >= 0),
    constraint inventory_balances_available_non_negative check (available >= 0)
);

create table orders (
    id uuid primary key,
    status text not null,
    idempotency_record_id uuid,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    cancelled_at timestamptz,
    constraint orders_status_valid check (status in ('accepted', 'cancelled')),
    constraint orders_cancelled_consistency check (
        (status = 'cancelled' and cancelled_at is not null)
        or (status = 'accepted' and cancelled_at is null)
    )
);

create table order_items (
    id uuid primary key,
    order_id uuid not null references orders (id) on delete cascade,
    sku_id uuid not null references skus (id) on delete restrict,
    quantity integer not null,
    created_at timestamptz not null default now(),
    constraint order_items_quantity_positive check (quantity > 0),
    constraint order_items_order_sku_unique unique (order_id, sku_id)
);

create table reservations (
    id uuid primary key,
    order_id uuid not null references orders (id) on delete cascade,
    order_item_id uuid not null references order_items (id) on delete cascade,
    sku_id uuid not null references skus (id) on delete restrict,
    quantity integer not null,
    status text not null,
    created_at timestamptz not null default now(),
    released_at timestamptz,
    constraint reservations_quantity_positive check (quantity > 0),
    constraint reservations_status_valid check (status in ('active', 'released')),
    constraint reservations_release_consistency check (
        (status = 'released' and released_at is not null)
        or (status = 'active' and released_at is null)
    )
);

create unique index reservations_one_active_per_order_item
    on reservations (order_item_id)
    where status = 'active';

create table inventory_movements (
    id bigint generated always as identity primary key,
    sku_id uuid not null references skus (id) on delete restrict,
    delta_on_hand integer not null default 0,
    delta_reserved integer not null default 0,
    kind text not null,
    actor text not null,
    reason text,
    order_id uuid references orders (id) on delete restrict,
    order_item_id uuid references order_items (id) on delete restrict,
    created_at timestamptz not null default now(),
    constraint inventory_movements_kind_valid check (
        kind in ('receipt', 'adjustment', 'shipment', 'reservation', 'release')
    ),
    constraint inventory_movements_single_effect check (
        (delta_on_hand <> 0 and delta_reserved = 0)
        or (delta_on_hand = 0 and delta_reserved <> 0)
    ),
    constraint inventory_movements_actor_not_blank check (btrim(actor) <> ''),
    constraint inventory_movements_kind_effect check (
        (kind = 'receipt' and delta_on_hand > 0)
        or (kind = 'adjustment')
        or (kind = 'shipment' and delta_on_hand < 0)
        or (kind = 'reservation' and delta_reserved > 0)
        or (kind = 'release' and delta_reserved < 0)
    ),
    constraint inventory_movements_reason_required check (
        kind not in ('receipt', 'adjustment')
        or (reason is not null and btrim(reason) <> '')
    )
);

create index inventory_movements_sku_id_id_idx
    on inventory_movements (sku_id, id desc);

create index inventory_movements_order_id_idx
    on inventory_movements (order_id);

create table idempotency_records (
    id uuid primary key,
    scope text not null,
    key text not null,
    request_hash text not null,
    state text not null,
    response_status integer,
    response_body jsonb,
    order_id uuid references orders (id) on delete set null,
    created_at timestamptz not null default now(),
    completed_at timestamptz,
    constraint idempotency_records_scope_key_unique unique (scope, key),
    constraint idempotency_records_state_valid check (state in ('in_progress', 'completed')),
    constraint idempotency_records_completed_consistency check (
        (state = 'completed' and response_status is not null and response_body is not null and completed_at is not null)
        or (state = 'in_progress' and response_status is null and response_body is null)
    ),
    constraint idempotency_records_scope_not_blank check (btrim(scope) <> ''),
    constraint idempotency_records_key_not_blank check (btrim(key) <> '')
);

alter table orders
    add constraint orders_idempotency_record_fk
    foreign key (idempotency_record_id) references idempotency_records (id) on delete set null;
