// Command hot_sku measures contention on a single hot SKU. It seeds a
// documented number of available units, fires a fixed number of concurrent
// single-unit order attempts through the real orders service, and prints a JSON
// result (accepted/conflict/error counts and latency percentiles).
//
// It truncates the target database, so point DATABASE_URL at a disposable
// database (the Compose stockflow_test database by default).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oliaditya05/stockflow/internal/catalog"
	"github.com/oliaditya05/stockflow/internal/inventory"
	"github.com/oliaditya05/stockflow/internal/orders"
	"github.com/oliaditya05/stockflow/internal/platform/apperr"
	"github.com/oliaditya05/stockflow/internal/platform/postgres"
	"github.com/oliaditya05/stockflow/migrations"
)

type result struct {
	Scenario            string  `json:"scenario"`
	Buyers              int     `json:"buyers"`
	Units               int     `json:"units"`
	PoolMaxConns        int     `json:"pool_max_conns"`
	GoVersion           string  `json:"go_version"`
	GoOS                string  `json:"go_os"`
	GoArch              string  `json:"go_arch"`
	NumCPU              int     `json:"num_cpu"`
	WallMs              float64 `json:"wall_ms"`
	Accepted            int     `json:"accepted"`
	Conflicts           int     `json:"conflicts"`
	Errors              int     `json:"errors"`
	ThroughputOpsPerSec float64 `json:"throughput_ops_per_sec"`
	LatencyMsP50        float64 `json:"latency_ms_p50"`
	LatencyMsP95        float64 `json:"latency_ms_p95"`
	LatencyMsMax        float64 `json:"latency_ms_max"`
}

func main() {
	units := flag.Int("units", 1, "available units on the hot SKU")
	buyers := flag.Int("buyers", 200, "concurrent single-unit order attempts")
	maxConns := flag.Int("max-conns", 50, "database pool size")
	flag.Parse()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL is required")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	pool, err := postgres.Open(ctx, postgres.Config{URL: databaseURL, MaxConns: int32(*maxConns), ConnectTimeout: 5 * time.Second})
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

	ledger := &inventory.Ledger{Pool: pool}
	catalogService := &catalog.Service{Pool: pool, Repo: &catalog.Repository{Pool: pool}, Ledger: ledger}
	ordersService := &orders.Service{Pool: pool, Ledger: ledger, Repo: &orders.Repository{Pool: pool}}

	code := "BENCH-HOT-SKU"
	if _, _, err := catalogService.Create(ctx, catalog.CreateSKUInput{
		Code: code, Name: "Bench hot SKU", Actor: "bench", OpeningQuantity: *units, OpeningReason: "benchmark seed",
	}); err != nil {
		fmt.Fprintln(os.Stderr, "seed sku:", err)
		os.Exit(2)
	}

	latencies := make([]float64, *buyers)
	statuses := make([]int, *buyers)
	codes := make([]string, *buyers)

	start := time.Now()
	var wg sync.WaitGroup
	gate := make(chan struct{})
	for i := 0; i < *buyers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-gate
			begin := time.Now()
			_, err := ordersService.CreateOrder(ctx, orders.CreateOrderInput{
				Scope: "bench",
				Key:   uuid.NewString(),
				Lines: []orders.LineRequest{{SKU: code, Quantity: 1}},
			})
			latencies[i] = float64(time.Since(begin).Microseconds()) / 1000.0
			if err == nil {
				statuses[i] = 201
				return
			}
			var domain *apperr.Error
			if errors.As(err, &domain) {
				statuses[i] = domain.Status
				codes[i] = domain.Code
				return
			}
			statuses[i] = 500
			codes[i] = err.Error()
		}(i)
	}
	close(gate)
	wg.Wait()
	wall := time.Since(start)

	res := result{
		Scenario:     "hot_sku_contention",
		Buyers:       *buyers,
		Units:        *units,
		PoolMaxConns: *maxConns,
		GoVersion:    runtime.Version(),
		GoOS:         runtime.GOOS,
		GoArch:       runtime.GOARCH,
		NumCPU:       runtime.NumCPU(),
		WallMs:       float64(wall.Microseconds()) / 1000.0,
	}
	for i, status := range statuses {
		switch status {
		case 201:
			res.Accepted++
		case 409:
			res.Conflicts++
		default:
			res.Errors++
			fmt.Fprintf(os.Stderr, "unexpected outcome %d (%s)\n", status, codes[i])
		}
	}
	if res.WallMs > 0 {
		res.ThroughputOpsPerSec = float64(*buyers) / (res.WallMs / 1000.0)
	}
	sorted := append([]float64(nil), latencies...)
	sort.Float64s(sorted)
	res.LatencyMsP50 = percentile(sorted, 0.50)
	res.LatencyMsP95 = percentile(sorted, 0.95)
	res.LatencyMsMax = sorted[len(sorted)-1]

	encoded, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(encoded))

	if res.Accepted != *units {
		fmt.Fprintf(os.Stderr, "invariant violated: accepted=%d, want exactly %d\n", res.Accepted, *units)
		os.Exit(1)
	}
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	index := int(p*float64(len(sorted)-1) + 0.5)
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
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
