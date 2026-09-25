-- V1: fulfilment state machine, audit history, and report-only reconciliation.
-- Forward-only: extends the V0 order status model without rewriting history.

-- Orders gain picking/packed/shipped and a shipment timestamp.
alter table orders drop constraint orders_status_valid;
alter table orders drop constraint orders_cancelled_consistency;
alter table orders add column shipped_at timestamptz;

alter table orders
    add constraint orders_status_valid check (
        status in ('accepted', 'picking', 'packed', 'shipped', 'cancelled')
    );

alter table orders
    add constraint orders_terminal_consistency check (
        (status = 'cancelled' and cancelled_at is not null and shipped_at is null)
        or (status = 'shipped' and shipped_at is not null and cancelled_at is null)
        or (status in ('accepted', 'picking', 'packed')
            and cancelled_at is null and shipped_at is null)
    );

-- Append-only fulfilment evidence. The unique index makes a repeated command a
-- no-op at the database level, so a linear state machine can visit each target
-- status at most once.
create table fulfilment_events (
    id bigint generated always as identity primary key,
    order_id uuid not null references orders (id) on delete cascade,
    from_status text not null,
    to_status text not null,
    actor text not null,
    reason text,
    created_at timestamptz not null default now(),
    constraint fulfilment_events_from_status_valid check (
        from_status in ('accepted', 'picking', 'packed')
    ),
    constraint fulfilment_events_to_status_valid check (
        to_status in ('picking', 'packed', 'shipped', 'cancelled')
    ),
    constraint fulfilment_events_actor_not_blank check (btrim(actor) <> ''),
    constraint fulfilment_events_transition_valid check (
        (from_status = 'accepted' and to_status in ('picking', 'cancelled'))
        or (from_status = 'picking' and to_status in ('packed', 'cancelled'))
        or (from_status = 'packed' and to_status in ('shipped', 'cancelled'))
    )
);

create unique index fulfilment_events_order_to_unique
    on fulfilment_events (order_id, to_status);

create index fulfilment_events_order_idx
    on fulfilment_events (order_id, id);

-- Generic actor/action history for operator-visible mutations.
create table audit_entries (
    id bigint generated always as identity primary key,
    actor text not null,
    action text not null,
    entity_type text not null,
    entity_id text not null,
    reason text,
    details jsonb,
    created_at timestamptz not null default now(),
    constraint audit_entries_actor_not_blank check (btrim(actor) <> ''),
    constraint audit_entries_action_not_blank check (btrim(action) <> ''),
    constraint audit_entries_entity_type_not_blank check (btrim(entity_type) <> ''),
    constraint audit_entries_entity_id_not_blank check (btrim(entity_id) <> '')
);

create index audit_entries_entity_idx
    on audit_entries (entity_type, entity_id, id desc);

-- Report-only reconciliation: runs and findings are persisted, never repaired.
create table reconciliation_runs (
    id uuid primary key,
    status text not null,
    checks_run integer not null,
    findings_count integer not null,
    started_at timestamptz not null,
    finished_at timestamptz not null,
    constraint reconciliation_runs_status_valid check (status in ('clean', 'findings')),
    constraint reconciliation_runs_counts_non_negative check (
        checks_run >= 0 and findings_count >= 0
    )
);

create table reconciliation_findings (
    id uuid primary key,
    run_id uuid not null references reconciliation_runs (id) on delete cascade,
    check_name text not null,
    sku_id uuid references skus (id) on delete set null,
    order_id uuid references orders (id) on delete set null,
    entity_ref text,
    expected text not null,
    observed text not null,
    details jsonb,
    created_at timestamptz not null default now(),
    constraint reconciliation_findings_check_name_not_blank check (btrim(check_name) <> ''),
    constraint reconciliation_findings_expected_not_blank check (btrim(expected) <> ''),
    constraint reconciliation_findings_observed_not_blank check (btrim(observed) <> '')
);

create index reconciliation_findings_run_idx
    on reconciliation_findings (run_id, id);

-- fulfilments, audit entries, and findings are evidence: no UPDATE/DELETE.
create or replace function stockflow_reject_append_only_mutation()
returns trigger
language plpgsql
as $$
begin
    raise exception '% is append-only; % is not permitted', tg_table_name, tg_op
        using errcode = '55000';
end;
$$;

create trigger fulfilment_events_append_only
before update or delete on fulfilment_events
for each row execute function stockflow_reject_append_only_mutation();

create trigger audit_entries_append_only
before update or delete on audit_entries
for each row execute function stockflow_reject_append_only_mutation();

create trigger reconciliation_findings_append_only
before update or delete on reconciliation_findings
for each row execute function stockflow_reject_append_only_mutation();
