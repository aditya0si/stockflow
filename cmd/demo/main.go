// Command demo is a deterministic, assertion-based proof of the V1 vertical
// slice. It exits nonzero on the first failed assertion. It truncates the
// database and injects a test-only discrepancy, so it refuses to run unless
// STOCKFLOW_ALLOW_DEMO_FIXTURES=true.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oliaditya05/stockflow/internal/auth"
	"github.com/oliaditya05/stockflow/internal/catalog"
	"github.com/oliaditya05/stockflow/internal/fulfilment"
	"github.com/oliaditya05/stockflow/internal/httpapi"
	"github.com/oliaditya05/stockflow/internal/inventory"
	"github.com/oliaditya05/stockflow/internal/orders"
	"github.com/oliaditya05/stockflow/internal/platform/observability"
	"github.com/oliaditya05/stockflow/internal/platform/postgres"
	"github.com/oliaditya05/stockflow/internal/reconciliation"
	"github.com/oliaditya05/stockflow/migrations"
)

const (
	demoUsername = "operator"
	demoPassword = "stockflow-demo"
)

type demo struct {
	pool           *pgxpool.Pool
	api            *httptest.Server
	reconciliation *reconciliation.Service
	cookie         string
	csrf           string
	step           int
}

func main() {
	if os.Getenv("STOCKFLOW_ALLOW_DEMO_FIXTURES") != "true" {
		fmt.Fprintln(os.Stderr, "refusing to run: set STOCKFLOW_ALLOW_DEMO_FIXTURES=true (the demo truncates data and injects a discrepancy)")
		os.Exit(2)
	}

	databaseURL := firstNonEmpty(os.Getenv("STOCKFLOW_DEMO_DATABASE_URL"), os.Getenv("STOCKFLOW_TEST_DATABASE_URL"), os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		fmt.Fprintln(os.Stderr, "set STOCKFLOW_DEMO_DATABASE_URL or DATABASE_URL")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool, err := postgres.Open(ctx, postgres.Config{URL: databaseURL, MaxConns: 25, ConnectTimeout: 5 * time.Second})
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

	demoEnv := func(key string) string {
		if key == "STOCKFLOW_DEMO_MODE" {
			return "true"
		}
		return os.Getenv(key)
	}
	authConfig, err := auth.LoadConfig(demoEnv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "auth config:", err)
		os.Exit(2)
	}
	authManager, err := auth.NewManager(authConfig)
	if err != nil {
		fmt.Fprintln(os.Stderr, "auth manager:", err)
		os.Exit(2)
	}

	ledger := &inventory.Ledger{Pool: pool}
	repo := &orders.Repository{Pool: pool}
	server := &httpapi.Server{
		Catalog:        &catalog.Service{Pool: pool, Repo: &catalog.Repository{Pool: pool}, Ledger: ledger},
		Inventory:      &inventory.Service{Pool: pool, Ledger: ledger},
		Orders:         &orders.Service{Pool: pool, Ledger: ledger, Repo: repo},
		Fulfilment:     &fulfilment.Service{Pool: pool, Ledger: ledger, Repo: repo},
		Reconciliation: &reconciliation.Service{Pool: pool},
		Health:         pool.Ping,
		Logger:         observability.NewLogger("error"),
		Auth:           authManager,
	}
	api := httptest.NewServer(server.Handler())
	defer api.Close()

	d := &demo{pool: pool, api: api, reconciliation: server.Reconciliation}
	d.login()
	d.run(ctx)
	fmt.Println("\nDEMO PASSED: all assertions held")
}

func (d *demo) login() {
	status, body := d.request(http.MethodPost, "/auth/login", map[string]any{
		"username": demoUsername, "password": demoPassword,
	}, nil)
	d.must(status == http.StatusOK, "login status %d: %s", status, body)
	var session struct {
		CSRFToken string `json:"csrf_token"`
	}
	d.decode(body, &session)
	d.csrf = session.CSRFToken
	d.must(d.csrf != "", "login did not return a csrf token")
	d.must(d.cookie != "", "login did not set a session cookie")
}

func (d *demo) run(ctx context.Context) {
	d.header("1. receipt and one-unit balance")
	skuID := d.stepReceipt(ctx)

	d.header("2. contention has one winner")
	winner := d.stepContention(ctx, skuID)

	d.header("3. idempotent replay returns the same order")
	d.stepReplay(ctx, skuID, winner)

	d.header("4. pick -> pack -> ship succeeds")
	d.stepShip(ctx, winner)

	d.header("5. shipment decrements on-hand once and clears the reservation")
	d.stepShipmentEffect(ctx, skuID, winner)

	d.header("6. illegal cancellation after shipment fails without mutation")
	d.stepIllegalCancel(ctx, skuID, winner)

	d.header("7. seeded discrepancy is detected without automatic repair")
	d.stepReconciliation(ctx)
}

type winner struct {
	orderID string
	scope   string
	key     string
	lines   []line
}

func (d *demo) stepReceipt(ctx context.Context) string {
	status, body := d.post("/skus", map[string]any{
		"code": "DEMO-V1-TEE", "name": "Demo V1 T-Shirt", "actor": "demo", "opening_quantity": 0,
	}, nil)
	d.must(status == http.StatusCreated, "create sku status %d: %s", status, body)
	var created struct {
		SKU struct {
			ID string `json:"id"`
		} `json:"sku"`
	}
	d.decode(body, &created)
	skuID := created.SKU.ID

	balance := d.balance(skuID)
	d.must(balance.OnHand == 0 && balance.Available == 0, "opening balance should be zero, got %+v", balance)

	status, body = d.post("/inventory/receipts", map[string]any{
		"sku": "DEMO-V1-TEE", "quantity": 1, "actor": "demo-owner", "reason": "supplier delivery",
	}, nil)
	d.must(status == http.StatusCreated, "receipt status %d: %s", status, body)

	balance = d.balance(skuID)
	d.must(balance.OnHand == 1 && balance.Reserved == 0 && balance.Available == 1,
		"after receipt expected on_hand=1 reserved=0 available=1, got %+v", balance)
	d.pass("receipt created exactly one unit on hand")

	return skuID
}

func (d *demo) stepContention(ctx context.Context, skuID string) winner {
	const buyers = 25
	type result struct {
		status int
		body   []byte
		key    string
	}
	results := make([]result, buyers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < buyers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("demo-contention-%s-%d", uuid.NewString(), i)
			<-start
			status, body := d.post("/orders", map[string]any{
				"lines": []line{{SKU: "DEMO-V1-TEE", Quantity: 1}},
			}, map[string]string{"X-Caller-Scope": "demo-buyer-" + fmt.Sprint(i), "Idempotency-Key": key})
			results[i] = result{status: status, body: body, key: key}
		}(i)
	}
	close(start)
	wg.Wait()

	accepted := 0
	conflicts := 0
	var winnerID, winnerKey, winnerScope string
	for i, result := range results {
		switch result.status {
		case http.StatusCreated:
			accepted++
			var order struct {
				ID string `json:"id"`
			}
			d.decode(result.body, &order)
			winnerID, winnerKey = order.ID, result.key
			winnerScope = "demo-buyer-" + fmt.Sprint(i)
		case http.StatusConflict:
			conflicts++
		default:
			d.fail("unexpected contention status %d: %s", result.status, result.body)
		}
	}
	d.must(accepted == 1, "expected exactly one accepted order, got %d", accepted)
	d.must(conflicts == buyers-1, "expected %d conflicts, got %d", buyers-1, conflicts)

	balance := d.balance(skuID)
	d.must(balance.OnHand == 1 && balance.Reserved == 1 && balance.Available == 0,
		"after contention expected on_hand=1 reserved=1 available=0, got %+v", balance)
	d.pass("25 simultaneous buyers produced one winner and no negative stock")

	return winner{orderID: winnerID, scope: winnerScope, key: winnerKey, lines: []line{{SKU: "DEMO-V1-TEE", Quantity: 1}}}
}

func (d *demo) stepReplay(ctx context.Context, skuID string, w winner) {
	status, body := d.post("/orders", map[string]any{"lines": w.lines},
		map[string]string{"X-Caller-Scope": w.scope, "Idempotency-Key": w.key})
	d.must(status == http.StatusCreated, "replay status %d: %s", status, body)
	var replayed struct {
		ID string `json:"id"`
	}
	d.decode(body, &replayed)
	d.must(replayed.ID == w.orderID, "replay returned %s, want %s", replayed.ID, w.orderID)

	balance := d.balance(skuID)
	d.must(balance.Reserved == 1, "replay must not reserve again, reserved=%d", balance.Reserved)
	orderCount := d.scalar(ctx, `select count(*) from orders`)
	d.must(orderCount == 1, "replay must not create an order, count=%d", orderCount)
	d.pass("same key and body returned the committed order without a second reservation")
}

func (d *demo) stepShip(ctx context.Context, w winner) {
	expected := map[string]string{"pick": "picking", "pack": "packed", "ship": "shipped"}
	for _, action := range []string{"pick", "pack", "ship"} {
		status, body := d.post("/orders/"+w.orderID+"/"+action,
			map[string]any{"actor": "demo-operator", "reason": "demo " + action}, nil)
		d.must(status == http.StatusOK, "%s status %d: %s", action, status, body)
		var order struct {
			Status string `json:"status"`
		}
		d.decode(body, &order)
		d.must(order.Status == expected[action], "after %s expected %q, got %q", action, expected[action], order.Status)
	}
	d.pass("pick, pack, and ship each returned the expected state")
}

func (d *demo) stepShipmentEffect(ctx context.Context, skuID string, w winner) {
	balance := d.balance(skuID)
	d.must(balance.OnHand == 0 && balance.Reserved == 0 && balance.Available == 0,
		"after shipment expected all zero, got %+v", balance)

	shipments := d.scalar(ctx, `select count(*) from inventory_movements where order_id = $1 and kind = 'shipment'`, w.orderID)
	d.must(shipments == 1, "expected exactly 1 shipment movement, got %d", shipments)
	releases := d.scalar(ctx, `select count(*) from inventory_movements where order_id = $1 and kind = 'release'`, w.orderID)
	d.must(releases == 1, "expected exactly 1 release movement, got %d", releases)
	active := d.scalar(ctx, `select count(*) from reservations where order_id = $1 and status = 'active'`, w.orderID)
	d.must(active == 0, "expected no active reservation after shipment, got %d", active)
	d.pass("on-hand decremented once, reservation released once")
}

func (d *demo) stepIllegalCancel(ctx context.Context, skuID string, w winner) {
	before := d.scalar(ctx, `select count(*) from inventory_movements where order_id = $1`, w.orderID)
	status, body := d.post("/orders/"+w.orderID+"/cancel",
		map[string]any{"actor": "demo-operator", "reason": "too late"}, nil)
	d.must(status == http.StatusConflict, "expected 409 cancelling a shipped order, got %d: %s", status, body)

	var problem struct {
		Code string `json:"code"`
	}
	d.decode(body, &problem)
	d.must(problem.Code == "illegal_transition", "expected illegal_transition, got %q", problem.Code)

	after := d.scalar(ctx, `select count(*) from inventory_movements where order_id = $1`, w.orderID)
	d.must(before == after, "illegal cancellation mutated movements: %d -> %d", before, after)
	balance := d.balance(skuID)
	d.must(balance.OnHand == 0 && balance.Reserved == 0, "illegal cancellation changed stock: %+v", balance)
	var orderStatus string
	d.must(d.pool.QueryRow(ctx, `select status from orders where id = $1`, w.orderID).Scan(&orderStatus) == nil, "read order status")
	d.must(orderStatus == "shipped", "order status changed to %q", orderStatus)
	d.pass("illegal cancellation returned a stable problem and mutated nothing")
}

func (d *demo) stepReconciliation(ctx context.Context) {
	status, body := d.post("/skus", map[string]any{
		"code": "DEMO-V1-MUG", "name": "Demo V1 Mug", "actor": "demo", "opening_quantity": 1,
		"opening_reason": "demo opening",
	}, nil)
	d.must(status == http.StatusCreated, "create second sku status %d: %s", status, body)
	var created struct {
		SKU struct {
			ID string `json:"id"`
		} `json:"sku"`
	}
	d.decode(body, &created)
	skuID := created.SKU.ID

	status, body = d.post("/orders", map[string]any{"lines": []line{{SKU: "DEMO-V1-MUG", Quantity: 1}}},
		map[string]string{"X-Caller-Scope": "demo", "Idempotency-Key": "demo-discrepancy-" + uuid.NewString()})
	d.must(status == http.StatusCreated, "create discrepancy order status %d: %s", status, body)

	before := d.balance(skuID)
	d.must(before.Reserved == 1, "expected reserved=1 before injection, got %+v", before)

	if _, err := d.pool.Exec(ctx, `update inventory_balances set reserved = reserved - 1 where sku_id = $1`, skuID); err != nil {
		d.fail("inject discrepancy: %v", err)
	}

	run, err := d.reconciliation.Run(ctx, "demo-operator")
	d.must(err == nil, "run reconciliation: %v", err)
	d.must(run.Status == reconciliation.StatusFindings, "expected findings status, got %q", run.Status)

	found := false
	for _, finding := range run.Findings {
		if finding.CheckName != reconciliation.CheckReservedMatchesActive {
			continue
		}
		if finding.SKUID == nil || finding.SKUID.String() != skuID {
			continue
		}
		d.must(finding.Expected == "1" && finding.Observed == "0",
			"finding expected/observed = %s/%s", finding.Expected, finding.Observed)
		found = true
	}
	d.must(found, "reconciliation did not report the injected reserved discrepancy")

	after := d.balance(skuID)
	d.must(after.Reserved == 0, "reconciliation must not repair the row, reserved=%d", after.Reserved)
	active := d.scalar(ctx, `select count(*) from reservations where sku_id = $1 and status = 'active'`, skuID)
	d.must(active == 1, "expected the active reservation to remain, count=%d", active)
	d.pass("discrepancy reported as expected=1 observed=0 and left unrepaired")
}

// --- helpers ---

type line struct {
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
}

type balance struct {
	OnHand    int `json:"on_hand"`
	Reserved  int `json:"reserved"`
	Available int `json:"available"`
}

func (d *demo) balance(skuID string) balance {
	status, body := d.get("/skus/" + skuID + "/balance")
	d.must(status == http.StatusOK, "get balance status %d: %s", status, body)
	var b balance
	d.decode(body, &b)
	return b
}

func (d *demo) scalar(ctx context.Context, query string, args ...any) int {
	var value int
	if err := d.pool.QueryRow(ctx, query, args...).Scan(&value); err != nil {
		d.fail("query %q: %v", query, err)
	}
	return value
}

func (d *demo) post(path string, payload any, headers map[string]string) (int, []byte) {
	return d.request(http.MethodPost, path, payload, headers)
}

func (d *demo) get(path string) (int, []byte) {
	return d.request(http.MethodGet, path, nil, nil)
}

func (d *demo) request(method, path string, payload any, headers map[string]string) (int, []byte) {
	var reader io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			d.fail("marshal payload: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, d.api.URL+path, reader)
	if err != nil {
		d.fail("build request: %v", err)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	if d.cookie != "" {
		request.Header.Set("Cookie", d.cookie)
	}
	if d.csrf != "" && method != http.MethodGet {
		request.Header.Set("X-CSRF-Token", d.csrf)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		d.fail("http %s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	for _, cookie := range response.Cookies() {
		d.cookie = cookie.Name + "=" + cookie.Value
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		d.fail("read response: %v", err)
	}
	return response.StatusCode, body
}

func (d *demo) decode(body []byte, target any) {
	if err := json.Unmarshal(body, target); err != nil {
		d.fail("decode response %s: %v", body, err)
	}
}

func (d *demo) header(text string) {
	d.step++
	fmt.Printf("\n[%d] %s\n", d.step, text)
}

func (d *demo) pass(text string) {
	fmt.Printf("    ok  %s\n", text)
}

func (d *demo) must(ok bool, format string, args ...any) {
	if !ok {
		d.fail(format, args...)
	}
}

func (d *demo) fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "    FAIL %s\n", fmt.Sprintf(format, args...))
	os.Exit(1)
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
