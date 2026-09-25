package inventory

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Ledger struct {
	Pool *pgxpool.Pool
}

func (l *Ledger) InitializeBalance(ctx context.Context, tx pgx.Tx, skuID uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		insert into inventory_balances (sku_id, on_hand, reserved)
		values ($1, 0, 0)
		on conflict (sku_id) do nothing`, skuID)
	return err
}

func (l *Ledger) LockBalances(ctx context.Context, tx pgx.Tx, skuIDs []uuid.UUID) ([]Balance, error) {
	rows, err := tx.Query(ctx, `
		select sku_id, on_hand, reserved, available, updated_at
		from inventory_balances
		where sku_id = any($1)
		order by sku_id
		for update`, skuIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	balances := make([]Balance, 0, len(skuIDs))
	for rows.Next() {
		var balance Balance
		if err := rows.Scan(&balance.SKUID, &balance.OnHand, &balance.Reserved, &balance.Available, &balance.UpdatedAt); err != nil {
			return nil, err
		}
		balances = append(balances, balance)
	}
	return balances, rows.Err()
}

func (l *Ledger) Apply(ctx context.Context, tx pgx.Tx, in MovementInput) (Movement, Balance, error) {
	var movement Movement
	err := tx.QueryRow(ctx, `
		insert into inventory_movements
			(sku_id, delta_on_hand, delta_reserved, kind, actor, reason, order_id, order_item_id)
		values ($1, $2, $3, $4, $5, $6, $7, $8)
		returning id, sku_id, delta_on_hand, delta_reserved, kind, actor, reason, order_id, order_item_id, created_at`,
		in.SKUID, in.DeltaOnHand, in.DeltaReserved, in.Kind, in.Actor, in.Reason, in.OrderID, in.OrderItemID,
	).Scan(
		&movement.ID, &movement.SKUID, &movement.DeltaOnHand, &movement.DeltaReserved,
		&movement.Kind, &movement.Actor, &movement.Reason, &movement.OrderID, &movement.OrderItemID, &movement.CreatedAt,
	)
	if err != nil {
		return Movement{}, Balance{}, err
	}

	var balance Balance
	err = tx.QueryRow(ctx, `
		update inventory_balances
		set on_hand = on_hand + $2,
		    reserved = reserved + $3,
		    updated_at = now()
		where sku_id = $1
		returning sku_id, on_hand, reserved, available, updated_at`,
		in.SKUID, in.DeltaOnHand, in.DeltaReserved,
	).Scan(&balance.SKUID, &balance.OnHand, &balance.Reserved, &balance.Available, &balance.UpdatedAt)
	if err != nil {
		return Movement{}, Balance{}, err
	}
	return movement, balance, nil
}

func (l *Ledger) Balance(ctx context.Context, skuID uuid.UUID) (Balance, error) {
	var balance Balance
	err := l.Pool.QueryRow(ctx, `
		select sku_id, on_hand, reserved, available, updated_at
		from inventory_balances
		where sku_id = $1`, skuID,
	).Scan(&balance.SKUID, &balance.OnHand, &balance.Reserved, &balance.Available, &balance.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Balance{}, fmt.Errorf("balance for sku %s: %w", skuID, err)
	}
	return balance, err
}

func (l *Ledger) Movements(ctx context.Context, filter MovementFilter) ([]Movement, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := l.Pool.Query(ctx, `
		select id, sku_id, delta_on_hand, delta_reserved, kind, actor, reason, order_id, order_item_id, created_at
		from inventory_movements
		where ($1::uuid is null or sku_id = $1)
		  and ($2::uuid is null or order_id = $2)
		order by id desc
		limit $3 offset $4`, filter.SKUID, filter.OrderID, limit, filter.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	movements := make([]Movement, 0)
	for rows.Next() {
		var movement Movement
		if err := rows.Scan(
			&movement.ID, &movement.SKUID, &movement.DeltaOnHand, &movement.DeltaReserved,
			&movement.Kind, &movement.Actor, &movement.Reason, &movement.OrderID, &movement.OrderItemID, &movement.CreatedAt,
		); err != nil {
			return nil, err
		}
		movements = append(movements, movement)
	}
	return movements, rows.Err()
}
