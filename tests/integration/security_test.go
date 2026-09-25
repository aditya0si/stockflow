package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oliaditya05/stockflow/internal/auth"
)

func doAuthenticatedWithoutCSRF(t *testing.T, method, url string, payload any) (*http.Response, []byte) {
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
	request.Header.Set("Cookie", testCookie)
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

func doWithCookie(t *testing.T, method, url, cookie string) (*http.Response, []byte) {
	t.Helper()
	request, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if cookie != "" {
		request.Header.Set("Cookie", cookie)
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

func TestUnauthenticatedRequestsAreRejected(t *testing.T) {
	api, _ := newAPI(t)

	cases := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"mutation create sku", http.MethodPost, "/skus", map[string]any{"code": "X", "name": "X"}},
		{"mutation receipt", http.MethodPost, "/inventory/receipts", map[string]any{"sku": "X", "quantity": 1, "reason": "x"}},
		{"order creation", http.MethodPost, "/orders", map[string]any{"lines": []line{{SKU: "X", Quantity: 1}}}},
		{"read balances", http.MethodGet, "/inventory/balances", nil},
		{"reconciliation", http.MethodPost, "/reconciliation/runs", map[string]any{}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			response, body := doJSONNoAuth(t, testCase.method, api.URL+testCase.path, testCase.body, nil)
			if response.StatusCode != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d body %s", response.StatusCode, body)
			}
			if problem := decodeProblem(t, body); problem.Code != "unauthenticated" {
				t.Fatalf("expected unauthenticated, got %q", problem.Code)
			}
		})
	}
}

func TestMutationsRequireCSRF(t *testing.T) {
	api, pool := newAPI(t)
	skuID, code := createSKU(t, api, "csrf", 0)

	before := scalarInt(t, pool, `select count(*) from inventory_movements where sku_id = $1`, skuID)
	response, body := doAuthenticatedWithoutCSRF(t, http.MethodPost, api.URL+"/inventory/receipts",
		map[string]any{"sku": code, "quantity": 3, "reason": "csrf attempt"})
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body %s", response.StatusCode, body)
	}
	if problem := decodeProblem(t, body); problem.Code != "csrf_failed" {
		t.Fatalf("expected csrf_failed, got %q", problem.Code)
	}
	if after := scalarInt(t, pool, `select count(*) from inventory_movements where sku_id = $1`, skuID); after != before {
		t.Fatalf("CSRF-rejected request mutated movements: %d -> %d", before, after)
	}
}

func TestForgedActorIsIgnored(t *testing.T) {
	api, pool := newAPI(t)
	skuID, code := createSKU(t, api, "forged-actor", 0)

	response, body := doJSON(t, http.MethodPost, api.URL+"/inventory/receipts", map[string]any{
		"sku": code, "quantity": 1, "actor": "attacker", "reason": "forged actor attempt",
	}, nil)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("receipt: status %d body %s", response.StatusCode, body)
	}

	var actor string
	if err := pool.QueryRow(context.Background(), `
		select actor from inventory_movements
		where sku_id = $1 and kind = 'receipt'
		order by id desc limit 1`, skuID).Scan(&actor); err != nil {
		t.Fatalf("read movement actor: %v", err)
	}
	if actor != testOperatorUsername {
		t.Fatalf("audit actor = %q, want the authenticated operator %q", actor, testOperatorUsername)
	}
}

func TestForgedCallerScopeIsIgnored(t *testing.T) {
	api, pool := newAPI(t)
	_, code := createSKU(t, api, "forged-scope", 5)

	key := "forged-scope-" + uuid.NewString()
	response, body := doJSON(t, http.MethodPost, api.URL+"/orders",
		map[string]any{"lines": []line{{SKU: code, Quantity: 1}}},
		map[string]string{"X-Caller-Scope": "admin", "Idempotency-Key": key})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("first order: status %d body %s", response.StatusCode, body)
	}
	first := decodeOrder(t, body)

	// A different forged scope header with the same idempotency key must replay
	// the same order, proving the scope comes from the session, not the header.
	response, body = doJSON(t, http.MethodPost, api.URL+"/orders",
		map[string]any{"lines": []line{{SKU: code, Quantity: 1}}},
		map[string]string{"X-Caller-Scope": "different", "Idempotency-Key": key})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("replay: status %d body %s", response.StatusCode, body)
	}
	replayed := decodeOrder(t, body)
	if replayed.ID != first.ID {
		t.Fatalf("forged scope changed idempotency identity: %s != %s", replayed.ID, first.ID)
	}

	var scope string
	if err := pool.QueryRow(context.Background(),
		`select scope from idempotency_records where key = $1`, key).Scan(&scope); err != nil {
		t.Fatalf("read idempotency scope: %v", err)
	}
	if scope != testOperatorUsername {
		t.Fatalf("idempotency scope = %q, want authenticated operator %q", scope, testOperatorUsername)
	}
}

func TestInvalidSessionRejected(t *testing.T) {
	api, _ := newAPI(t)
	response, body := doWithCookie(t, http.MethodGet, api.URL+"/inventory/balances", "stockflow_session=not-a-real-session")
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for a forged cookie, got %d body %s", response.StatusCode, body)
	}
	if problem := decodeProblem(t, body); problem.Code != "unauthenticated" {
		t.Fatalf("expected unauthenticated, got %q", problem.Code)
	}
}

func TestLoginThrottling(t *testing.T) {
	_, pool := newAPI(t)
	server := newTestServer(pool, testAuthManager, auth.NewLoginLimiter(3, time.Minute))
	defer server.Close()

	login := func(password string) int {
		payload, err := json.Marshal(map[string]string{"username": testOperatorUsername, "password": password})
		if err != nil {
			t.Fatalf("marshal login: %v", err)
		}
		response, err := http.Post(server.URL+"/auth/login", "application/json", bytes.NewReader(payload))
		if err != nil {
			t.Fatalf("login request: %v", err)
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, response.Body)
		return response.StatusCode
	}

	for attempt := 1; attempt <= 3; attempt++ {
		if status := login("wrong-password"); status != http.StatusUnauthorized {
			t.Fatalf("failed login %d: expected 401, got %d", attempt, status)
		}
	}
	if status := login("wrong-password"); status != http.StatusTooManyRequests {
		t.Fatalf("fourth failed login: expected 429, got %d", status)
	}
	// Even the correct password is throttled while the window is open.
	if status := login(testOperatorPassword); status != http.StatusTooManyRequests {
		t.Fatalf("correct login while throttled: expected 429, got %d", status)
	}
}

func TestLogoutClearsSession(t *testing.T) {
	api, _ := newAPI(t)
	response, body := doJSON(t, http.MethodPost, api.URL+"/auth/logout", map[string]any{}, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("logout: status %d body %s", response.StatusCode, body)
	}
	var cleared string
	for _, cookie := range response.Cookies() {
		cleared = cookie.Name + "=" + cookie.Value
	}
	response, body = doWithCookie(t, http.MethodGet, api.URL+"/inventory/balances", cleared)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 after logout, got %d body %s", response.StatusCode, body)
	}
}

func TestSessionEndpointReportsAuthenticatedIdentity(t *testing.T) {
	api, _ := newAPI(t)
	response, body := doWithCookie(t, http.MethodGet, api.URL+"/auth/session", testCookie)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("session: status %d body %s", response.StatusCode, body)
	}
	var session struct {
		Authenticated bool   `json:"authenticated"`
		Username      string `json:"username"`
		CSRFToken     string `json:"csrf_token"`
	}
	if err := json.Unmarshal(body, &session); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if !session.Authenticated || session.Username != testOperatorUsername || session.CSRFToken == "" {
		t.Fatalf("unexpected session response: %+v", session)
	}

	response, body = doWithCookie(t, http.MethodGet, api.URL+"/auth/session", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("anonymous session: status %d body %s", response.StatusCode, body)
	}
	if err := json.Unmarshal(body, &session); err != nil {
		t.Fatalf("decode anonymous session: %v", err)
	}
	if session.Authenticated {
		t.Fatal("anonymous session reported authenticated")
	}
}
