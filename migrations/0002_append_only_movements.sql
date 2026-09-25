create or replace function stockflow_reject_movement_mutation()
returns trigger
language plpgsql
as $$
begin
    raise exception 'inventory_movements is append-only; % is not permitted', tg_op
        using errcode = '55000';
end;
$$;

create trigger inventory_movements_append_only
before update or delete on inventory_movements
for each row execute function stockflow_reject_movement_mutation();
