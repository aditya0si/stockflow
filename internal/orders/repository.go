package orders

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oliaditya05/stockflow/internal/platform/postgres"
)

type Repository struct {
	Pool *pgxpool.Pool
}

func (r *Repository) Get(ctx context.Context, db postgres.DBTX, orderID uuid.UUID) (Order, error) {
	var order Order
	err := db.QueryRow(ctx, `
		select id, status, created_at, updated_at, cancelled_at
		from orders
		where id = $1`, orderID,
	).Scan(&order.ID, &order.Status, &order.CreatedAt, &order.UpdatedAt, &order.CancelledAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, pgx.ErrNoRows
	}
	if err != nil {
		return Order{}, err
	}

	items, err := r.itemsForOrder(ctx, db, order.ID)
	if err != nil {
		return Order{}, err
	}
	order.Items = items
	return order, nil
}

func (r *Repository) List(ctx context.Context, limit, offset int) ([]Order, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := r.Pool.Query(ctx, `
		select id, status, created_at, updated_at, cancelled_at
		from orders
		order by created_at desc, id desc
		limit $1 offset $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	orders := make([]Order, 0)
	for rows.Next() {
		var order Order
		if err := rows.Scan(&order.ID, &order.Status, &order.CreatedAt, &order.UpdatedAt, &order.CancelledAt); err != nil {
			return nil, err
		}
		orders = append(orders, order)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range orders {
		items, err := r.itemsForOrder(ctx, r.Pool, orders[i].ID)
		if err != nil {
			return nil, err
		}
		orders[i].Items = items
	}
	return orders, nil
}

func (r *Repository) itemsForOrder(ctx context.Context, db postgres.DBTX, orderID uuid.UUID) ([]OrderItem, error) {
	rows, err := db.Query(ctx, `
		select oi.id, oi.sku_id, s.code, oi.quantity, r.status
		from order_items oi
		join skus s on s.id = oi.sku_id
		join reservations r on r.order_item_id = oi.id
		where oi.order_id = $1
		order by s.code`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]OrderItem, 0)
	for rows.Next() {
		var item OrderItem
		if err := rows.Scan(&item.ID, &item.SKUID, &item.SKU, &item.Quantity, &item.ReservationStatus); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
