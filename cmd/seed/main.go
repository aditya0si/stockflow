package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oliaditya05/stockflow/internal/platform/postgres"
	"github.com/oliaditya05/stockflow/migrations"
)

type seedSKU struct {
	Code    string
	Name    string
	Opening int
}

var seedSKUs = []seedSKU{
	{Code: "DEMO-TEE", Name: "Demo T-Shirt", Opening: 10},
	{Code: "DEMO-MUG", Name: "Demo Mug", Opening: 5},
	{Code: "DEMO-CAP", Name: "Demo Cap", Opening: 0},
}

func main() {
	reset := flag.Bool("reset", false, "delete all existing rows before seeding")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL is required")
		os.Exit(1)
	}

	pool, err := postgres.Open(ctx, postgres.Config{URL: databaseURL, ConnectTimeout: 5 * time.Second})
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := postgres.Migrate(ctx, pool, migrations.FS); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}

	if *reset {
		if err := resetDatabase(ctx, pool); err != nil {
			fmt.Fprintln(os.Stderr, "reset:", err)
			os.Exit(1)
		}
		fmt.Println("reset: removed all existing rows")
	}

	for _, sku := range seedSKUs {
		created, err := ensureSKU(ctx, pool, sku)
		if err != nil {
			fmt.Fprintln(os.Stderr, "seed", sku.Code+":", err)
			os.Exit(1)
		}
		state := "exists"
		if created {
			state = "created"
		}
		fmt.Printf("%-10s %-16s opening=%d (%s)\n", sku.Code, sku.Name, sku.Opening, state)
	}
	fmt.Println("seed complete")
}

func resetDatabase(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `
		truncate table
			inventory_movements,
			reservations,
			order_items,
			orders,
			idempotency_records,
			inventory_balances,
			skus
		restart identity cascade`)
	return err
}

func ensureSKU(ctx context.Context, pool *pgxpool.Pool, sku seedSKU) (bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	var existing uuid.UUID
	err = tx.QueryRow(ctx, `select id from skus where code = $1`, sku.Code).Scan(&existing)
	if err == nil {
		return false, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}

	id := uuid.NewSHA1(uuid.NameSpaceURL, []byte("stockflow/sku/"+sku.Code))
	if _, err := tx.Exec(ctx, `
		insert into skus (id, code, name)
		values ($1, $2, $3)`, id, sku.Code, sku.Name); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `
		insert into inventory_balances (sku_id, on_hand, reserved)
		values ($1, 0, 0)`, id); err != nil {
		return false, err
	}
	if sku.Opening > 0 {
		reason := "seed opening balance"
		if _, err := tx.Exec(ctx, `
			insert into inventory_movements
				(sku_id, delta_on_hand, delta_reserved, kind, actor, reason)
			values ($1, $2, 0, 'receipt', 'seed', $3)`, id, sku.Opening, reason); err != nil {
			return false, err
		}
		if _, err := tx.Exec(ctx, `
			update inventory_balances
			set on_hand = on_hand + $2, updated_at = now()
			where sku_id = $1`, id, sku.Opening); err != nil {
			return false, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}
