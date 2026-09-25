package catalog

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	Pool *pgxpool.Pool
}

func (r *Repository) Insert(ctx context.Context, tx pgx.Tx, sku SKU) error {
	_, err := tx.Exec(ctx, `
		insert into skus (id, code, name)
		values ($1, $2, $3)`, sku.ID, sku.Code, sku.Name)
	return err
}

func (r *Repository) GetByID(ctx context.Context, id uuid.UUID) (SKU, error) {
	var sku SKU
	err := r.Pool.QueryRow(ctx, `
		select id, code, name, created_at
		from skus
		where id = $1`, id).Scan(&sku.ID, &sku.Code, &sku.Name, &sku.CreatedAt)
	return sku, err
}

func (r *Repository) GetByCode(ctx context.Context, code string) (SKU, error) {
	var sku SKU
	err := r.Pool.QueryRow(ctx, `
		select id, code, name, created_at
		from skus
		where code = $1`, code).Scan(&sku.ID, &sku.Code, &sku.Name, &sku.CreatedAt)
	return sku, err
}

func (r *Repository) List(ctx context.Context, limit, offset int) ([]SKU, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := r.Pool.Query(ctx, `
		select id, code, name, created_at
		from skus
		order by code
		limit $1 offset $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	skus := make([]SKU, 0)
	for rows.Next() {
		var sku SKU
		if err := rows.Scan(&sku.ID, &sku.Code, &sku.Name, &sku.CreatedAt); err != nil {
			return nil, err
		}
		skus = append(skus, sku)
	}
	return skus, rows.Err()
}
