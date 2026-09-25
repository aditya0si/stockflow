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
	"golang.org/x/crypto/bcrypt"
)

const defaultTestDatabaseURL = "postgres://stockflow:stockflow@localhost:5432/stockflow_test?sslmode=disable"

const (
	testOperatorUsername = "operator"
	testOperatorPassword = "integration-test-password"
)

var (
	setupOnce       sync.Once
	setupErr        error
	testPool        *pgxpool.Pool
	testAPI         *httptest.Server
	testAuthManager *auth.Manager
	testCookie      string
	testCSRF        string
)

// newTestServer builds the same application wiring used by cmd/api against the
// shared test pool, with a caller-supplied session manager and login limiter.
func newTestServer(pool *pgxpool.Pool, manager *auth.Manager, limiter *auth.LoginLimiter) *httptest.Server {
	ledger := &inventory.Ledger{Pool: pool}
	ordersRepo := &orders.Repository{Pool: pool}
	server := &httpapi.Server{
		Catalog:        &catalog.Service{Pool: pool, Repo: &catalog.Repository{Pool: pool}, Ledger: ledger},
		Inventory:      &inventory.Service{Pool: pool, Ledger: ledger},
		Orders:         &orders.Service{Pool: pool, Ledger: ledger, Repo: ordersRepo},
		Fulfilment:     &fulfilment.Service{Pool: pool, Ledger: ledger, Repo: ordersRepo},
		Reconciliation: &reconciliation.Service{Pool: pool},
		Health:         pool.Ping,
		Logger:         observability.NewLogger("error"),
		Auth:           manager,
		LoginLimiter:   limiter,
	}
	return httptest.NewServer(server.Handler())
}

func newAPI(t *testing.T) (*httptest.Server, *pgxpool.Pool) {
	t.Helper()
	setupOnce.Do(setup)
	if setupErr != nil {
		t.Fatalf("postgres integration test database unavailable: %v\n"+
			"start it with `docker compose up -d postgres` or set STOCKFLOW_TEST_DATABASE_URL; "+
			"`go test ./...` requires the real PostgreSQL test database", setupErr)
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

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(testOperatorPassword), bcrypt.MinCost)
	if err != nil {
		setupErr = err
		return
	}
	authConfig, err := auth.LoadConfig(func(key string) string {
		switch key {
		case "STOCKFLOW_OPERATOR_USERNAME":
			return testOperatorUsername
		case "STOCKFLOW_OPERATOR_PASSWORD_HASH":
			return string(passwordHash)
		case "STOCKFLOW_SESSION_SECRET":
			return "integration-test-session-secret"
		default:
			return ""
		}
	})
	if err != nil {
		setupErr = err
		return
	}
	authManager, err := auth.NewManager(authConfig)
	if err != nil {
		setupErr = err
		return
	}

	testPool = pool
	testAuthManager = authManager
	testAPI = newTestServer(pool, authManager, auth.NewLoginLimiter(1000, time.Minute))

	if err := loginTestOperator(testAPI.URL); err != nil {
		testAPI.Close()
		setupErr = err
		return
	}
}

func loginTestOperator(baseURL string) error {
	payload, err := json.Marshal(map[string]string{
		"username": testOperatorUsername,
		"password": testOperatorPassword,
	})
	if err != nil {
		return err
	}
	response, err := http.Post(baseURL+"/auth/login", "application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		return fmt.Errorf("login status %d: %s", response.StatusCode, body)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	var session struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(body, &session); err != nil {
		return err
	}
	if session.CSRFToken == "" {
		return fmt.Errorf("login did not return a csrf token")
	}
	testCSRF = session.CSRFToken
	for _, cookie := range response.Cookies() {
		testCookie = cookie.Name + "=" + cookie.Value
	}
	if testCookie == "" {
		return fmt.Errorf("login did not set a session cookie")
	}
	return nil
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

// doJSON issues an authenticated operator request: the session cookie and CSRF
// token captured at login are attached automatically.
func doJSON(t *testing.T, method, url string, payload any, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	return doJSONAs(t, method, url, payload, headers, true)
}

// doJSONNoAuth issues a request with no session cookie and no CSRF token, for
// security tests.
func doJSONNoAuth(t *testing.T, method, url string, payload any, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	return doJSONAs(t, method, url, payload, headers, false)
}

func doJSONAs(t *testing.T, method, url string, payload any, headers map[string]string, authenticated bool) (*http.Response, []byte) {
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
	if authenticated {
		request.Header.Set("Cookie", testCookie)
		if method != http.MethodGet && method != http.MethodHead {
			request.Header.Set("X-CSRF-Token", testCSRF)
		}
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

type eventBody struct {
	ID         int64  `json:"id"`
	OrderID    string `json:"order_id"`
	FromStatus string `json:"from_status"`
	ToStatus   string `json:"to_status"`
	Actor      string `json:"actor"`
}

type findingBody struct {
	ID        string `json:"id"`
	CheckName string `json:"check_name"`
	SKUID     string `json:"sku_id"`
	OrderID   string `json:"order_id"`
	Expected  string `json:"expected"`
	Observed  string `json:"observed"`
}

type runBody struct {
	ID            string        `json:"id"`
	Status        string        `json:"status"`
	ChecksRun     int           `json:"checks_run"`
	FindingsCount int           `json:"findings_count"`
	Findings      []findingBody `json:"findings"`
}

func transition(t *testing.T, api *httptest.Server, orderID, action string, actor string) (*http.Response, []byte) {
	t.Helper()
	return doJSON(t, http.MethodPost, api.URL+"/orders/"+orderID+"/"+action,
		map[string]any{"actor": actor, "reason": "integration " + action}, nil)
}

func runReconciliation(t *testing.T, api *httptest.Server) (*http.Response, []byte) {
	t.Helper()
	return doJSON(t, http.MethodPost, api.URL+"/reconciliation/runs", map[string]any{}, nil)
}

func decodeRun(t *testing.T, body []byte) runBody {
	t.Helper()
	var run runBody
	if err := json.Unmarshal(body, &run); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	return run
}

func scalarInt(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var value int
	if err := pool.QueryRow(context.Background(), query, args...).Scan(&value); err != nil {
		t.Fatalf("scalar query %q: %v", query, err)
	}
	return value
}
