// Command reconcile measures reconciliation over a documented seeded history
// size. It seeds a fixed number of SKUs and receipt movements with balances
// that agree with the ledger, then times one full report-only reconciliation
// run. It prints a JSON result.
//
// It truncates the target database, so point DATABASE_URL at a disposable
// database (the Compose stockflow_test database by default).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oliaditya05/stockflow/internal/platform/postgres"
	"github.com/oliaditya05/stockflow/internal/reconciliation"
	"github.com/oliaditya05/stockflow/migrations"
)

type result struct {
	Scenario        string  `json:"scenario"`
	SeededSkus      int     `json:"seeded_skus"`
	SeededMovements int     `json:"seeded_movements"`
	GoVersion       string  `json:"go_version"`
	GoOS            string  `json:"go_os"`
	GoArch          string  `json:"go_arch"`
	NumCPU          int     `json:"num_cpu"`
	SeedMs          float64 `json:"seed_ms"`
	WallMs          float64 `json:"wall_ms"`
	MovementsPerSec float64 `json:"movements_per_sec"`
	ChecksRun       int     `json:"checks_run"`
	FindingsCount   int     `json:"findings_count"`
	RunStatus       string  `json:"run_status"`
}

func main() {
	movements := flag.Int("movements", 100_000, "number of receipt movements to seed")
	skus := flag.Int("skus", 50, "number of SKUs the history is spread across")
	flag.Parse()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL is required")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	pool, err := postgres.Open(ctx, postgres.Config{URL: databaseURL, MaxConns: 10, ConnectTimeout: 5 * time.Second})
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		os.Exit(2)
	}
	defer pool.Close()

	if err := postgres.Migrate(ctx, pool, migrations.FS); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(2)
	}
	if err := reset(ctx, pool); err != nil {
		fmt.Fprintln(os.Stderr, "reset:", err)
		os.Exit(2)
	}

	seedStart := time.Now()
	if err := seed(ctx, pool, *skus, *movements); err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(2)
	}
	seedWall := time.Since(seedStart)

	runStart := time.Now()
	run, err := (&reconciliation.Service{Pool: pool}).Run(ctx, "bench")
	if err != nil {
		fmt.Fprintln(os.Stderr, "reconcile:", err)
		os.Exit(2)
	}
	wall := time.Since(runStart)

	res := result{
		Scenario:        "reconciliation_over_seeded_history",
		SeededSkus:      *skus,
		SeededMovements: *movements,
		GoVersion:       runtime.Version(),
		GoOS:            runtime.GOOS,
		GoArch:          runtime.GOARCH,
		NumCPU:          runtime.NumCPU(),
		SeedMs:          float64(seedWall.Microseconds()) / 1000.0,
		WallMs:          float64(wall.Microseconds()) / 1000.0,
		ChecksRun:       run.ChecksRun,
		FindingsCount:   run.FindingsCount,
		RunStatus:       run.Status,
	}
	if res.WallMs > 0 {
		res.MovementsPerSec = float64(*movements) / (res.WallMs / 1000.0)
	}

	encoded, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(encoded))

	if run.FindingsCount != 0 {
		fmt.Fprintf(os.Stderr, "unexpected findings over a consistent seeded history: %d\n", run.FindingsCount)
		os.Exit(1)
	}
}

func seed(ctx context.Context, pool *pgxpool.Pool, skus, movements int) error {
	skuIDs := make([]uuid.UUID, 0, skus)
	for i := 0; i < skus; i++ {
		id := uuid.NewSHA1(uuid.NameSpaceURL, []byte(fmt.Sprintf("stockflow/bench/sku/%d", i)))
		skuIDs = append(skuIDs, id)
		if _, err := pool.Exec(ctx, `
			insert into skus (id, code, name) values ($1, $2, $3)`,
			id, fmt.Sprintf("BENCH-%04d", i), fmt.Sprintf("Bench SKU %d", i)); err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, `
			insert into inventory_balances (sku_id, on_hand, reserved) values ($1, 0, 0)`, id); err != nil {
			return err
		}
	}

	// Distribute movements across the seeded SKUs with a server-side series,
	// then set every balance to the ledger sum so the history is consistent.
	if _, err := pool.Exec(ctx, `
		with sku_list as (select array_agg(id order by id) as ids from skus),
		     gen as (select g from generate_series(0, $1 - 1) g)
		insert into inventory_movements
			(sku_id, delta_on_hand, delta_reserved, kind, actor, reason)
		select sk.ids[1 + (gen.g % array_length(sk.ids, 1))], 1, 0, 'receipt', 'bench', 'seeded benchmark history'
		from gen, sku_list sk`, movements); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `
		update inventory_balances b
		set on_hand = totals.total, updated_at = now()
		from (select sku_id, sum(delta_on_hand) as total from inventory_movements group by sku_id) totals
		where totals.sku_id = b.sku_id`); err != nil {
		return err
	}
	return nil
}

func reset(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `
		truncate table
			reconciliation_findings,
			reconciliation_runs,
			audit_entries,
			fulfilment_events,
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
