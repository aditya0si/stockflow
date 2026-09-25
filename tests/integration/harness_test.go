package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oliaditya05/stockflow/internal/catalog"
	"github.com/oliaditya05/stockflow/internal/httpapi"
	"github.com/oliaditya05/stockflow/internal/inventory"
	"github.com/oliaditya05/stockflow/internal/orders"
	"github.com/oliaditya05/stockflow/internal/platform/observability"
	"github.com/oliaditya05/stockflow/internal/platform/postgres"
	"github.com/oliaditya05/stockflow/migrations"
)

const defaultTestDatabaseURL = "postgres://stockflow:stockflow@localhost:5432/stockflow_test?sslmode=disable"

var (
	setupOnce sync.Once
	setupErr  error
	testPool  *pgxpool.Pool
	testAPI   *httptest.Server
)

func newAPI(t *testing.T) (*httptest.Server, *pgxpool.Pool) {
	t.Helper()
	setupOnce.Do(setup)
	if setupErr != nil {
		t.Skipf("postgres integration tests unavailable: %v", setupErr)
	}
	return testAPI, testPool
}

func setup() {
	databaseURL := os.Getenv("STOCKFLOW_TEST_DATABASE_URL")
	if databaseURL == "" {
		databaseURL = defaultTestDatabaseURL
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := postgres.Open(ctx, postgres.Config{URL: databaseURL, MaxConns: 20, ConnectTimeout: 5 * time.Second})
	if err != nil {
		setupErr = err
		return
	}
	if err := postgres.Migrate(ctx, pool, migrations.FS); err != nil {
		pool.Close()
		setupErr = err
		return
	}

	ledger := &inventory.Ledger{Pool: pool}
	catalogService := &catalog.Service{Pool: pool, Repo: &catalog.Repository{Pool: pool}, Ledger: ledger}
	inventoryService := &inventory.Service{Pool: pool, Ledger: ledger}
	ordersService := &orders.Service{Pool: pool, Ledger: ledger, Repo: &orders.Repository{Pool: pool}}

	server := &httpapi.Server{
		Catalog:   catalogService,
		Inventory: inventoryService,
		Orders:    ordersService,
		Health:    pool.Ping,
		Logger:    observability.NewLogger("error"),
	}
	testPool = pool
	testAPI = httptest.NewServer(server.Handler())
}

type line struct {
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
}

type problemBody struct {
	Type       string         `json:"type"`
	Title      string         `json:"title"`
	Status     int            `json:"status"`
	Code       string         `json:"code"`
	Detail     string         `json:"detail"`
	Extensions map[string]any `json:"extensions"`
}

type balanceBody struct {
	SKUID     string `json:"sku_id"`
	OnHand    int    `json:"on_hand"`
	Reserved  int    `json:"reserved"`
	Available int    `json:"available"`
}

type skuBody struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

type createSKUResponse struct {
	SKU     skuBody     `json:"sku"`
	Balance balanceBody `json:"balance"`
}

type orderItemBody struct {
	ID                string `json:"id"`
	SKU               string `json:"sku"`
	Quantity          int    `json:"quantity"`
	ReservationStatus string `json:"reservation_status"`
}

type orderBody struct {
	ID     string          `json:"id"`
	Status string          `json:"status"`
	Items  []orderItemBody `json:"items"`
}

func doJSON(t *testing.T, method, url string, payload any, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("http %s %s: %v", method, url, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return response, body
}

func createSKU(t *testing.T, api *httptest.Server, prefix string, opening int) (string, string) {
	t.Helper()
	code := fmt.Sprintf("%s-%s", prefix, strings.ToUpper(uuid.NewString()[:8]))
	response, body := doJSON(t, http.MethodPost, api.URL+"/skus", map[string]any{
		"code":             code,
		"name":             "Test " + code,
		"actor":            "test",
		"opening_quantity": opening,
		"opening_reason":   "integration test fixture",
	}, nil)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create sku %s: status %d body %s", code, response.StatusCode, body)
	}
	var decoded createSKUResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode create sku: %v", err)
	}
	return decoded.SKU.ID, code
}

func createOrder(t *testing.T, api *httptest.Server, scope, key string, lines []line) (*http.Response, []byte) {
	t.Helper()
	return doJSON(t, http.MethodPost, api.URL+"/orders", map[string]any{"lines": lines}, map[string]string{
		"X-Caller-Scope":  scope,
		"Idempotency-Key": key,
	})
}

func getBalance(t *testing.T, api *httptest.Server, skuID string) balanceBody {
	t.Helper()
	response, body := doJSON(t, http.MethodGet, api.URL+"/skus/"+skuID+"/balance", nil, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("get balance: status %d body %s", response.StatusCode, body)
	}
	var decoded balanceBody
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode balance: %v", err)
	}
	return decoded
}

func decodeOrder(t *testing.T, body []byte) orderBody {
	t.Helper()
	var order orderBody
	if err := json.Unmarshal(body, &order); err != nil {
		t.Fatalf("decode order: %v", err)
	}
	return order
}

func decodeProblem(t *testing.T, body []byte) problemBody {
	t.Helper()
	var problem problemBody
	if err := json.Unmarshal(body, &problem); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	return problem
}

func scalarInt(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var value int
	if err := pool.QueryRow(context.Background(), query, args...).Scan(&value); err != nil {
		t.Fatalf("scalar query %q: %v", query, err)
	}
	return value
}
