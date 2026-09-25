package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func TestHealthEndpoints(t *testing.T) {
	api, _ := newAPI(t)
	for _, path := range []string{"/healthz", "/readyz"} {
		response, body := doJSON(t, http.MethodGet, api.URL+path, nil, nil)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d body %s", path, response.StatusCode, body)
		}
	}
}

func TestCreateOrderHeaderValidation(t *testing.T) {
	api, _ := newAPI(t)
	_, code := createSKU(t, api, "validation", 1)

	response, body := doJSON(t, http.MethodPost, api.URL+"/orders",
		map[string]any{"lines": []line{{SKU: code, Quantity: 1}}},
		map[string]string{"Idempotency-Key": "no-scope"})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing scope: expected 400, got %d body %s", response.StatusCode, body)
	}
	if problem := decodeProblem(t, body); problem.Code != "missing_caller_scope" {
		t.Fatalf("expected missing_caller_scope, got %q", problem.Code)
	}

	response, body = doJSON(t, http.MethodPost, api.URL+"/orders",
		map[string]any{"lines": []line{{SKU: code, Quantity: 1}}},
		map[string]string{"X-Caller-Scope": "checkout"})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing key: expected 400, got %d body %s", response.StatusCode, body)
	}
	if problem := decodeProblem(t, body); problem.Code != "missing_idempotency_key" {
		t.Fatalf("expected missing_idempotency_key, got %q", problem.Code)
	}
}

func TestCreateOrderValidation(t *testing.T) {
	api, _ := newAPI(t)
	_, code := createSKU(t, api, "validation2", 1)

	cases := []struct {
		name     string
		body     map[string]any
		wantCode string
	}{
		{"empty lines", map[string]any{"lines": []line{}}, "invalid_lines"},
		{"zero quantity", map[string]any{"lines": []line{{SKU: code, Quantity: 0}}}, "invalid_quantity"},
		{"negative quantity", map[string]any{"lines": []line{{SKU: code, Quantity: -2}}}, "invalid_quantity"},
		{"blank sku", map[string]any{"lines": []line{{SKU: "  ", Quantity: 1}}}, "invalid_sku"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			response, body := doJSON(t, http.MethodPost, api.URL+"/orders", testCase.body, map[string]string{
				"X-Caller-Scope":  "checkout",
				"Idempotency-Key": uuid.NewString(),
			})
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d body %s", response.StatusCode, body)
			}
			if problem := decodeProblem(t, body); problem.Code != testCase.wantCode {
				t.Fatalf("expected %s, got %q", testCase.wantCode, problem.Code)
			}
		})
	}
}

func TestUnknownSKU(t *testing.T) {
	api, _ := newAPI(t)
	response, body := createOrder(t, api, "checkout", uuid.NewString(), []line{{SKU: "DOES-NOT-EXIST", Quantity: 1}})
	if response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d body %s", response.StatusCode, body)
	}
	if problem := decodeProblem(t, body); problem.Code != "unknown_sku" {
		t.Fatalf("expected unknown_sku, got %q", problem.Code)
	}
}

func TestDuplicateLinesAreNormalized(t *testing.T) {
	api, _ := newAPI(t)
	skuID, code := createSKU(t, api, "duplicate", 10)

	response, body := createOrder(t, api, "checkout", uuid.NewString(), []line{
		{SKU: code, Quantity: 1},
		{SKU: code, Quantity: 2},
	})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create order: status %d body %s", response.StatusCode, body)
	}
	order := decodeOrder(t, body)
	if len(order.Items) != 1 {
		t.Fatalf("expected 1 normalized item, got %d", len(order.Items))
	}
	if order.Items[0].Quantity != 3 {
		t.Fatalf("expected summed quantity 3, got %d", order.Items[0].Quantity)
	}
	balance := getBalance(t, api, skuID)
	if balance.Reserved != 3 {
		t.Fatalf("expected reserved=3, got %d", balance.Reserved)
	}
}

func TestReceiptRequiresReasonAndIncreasesOnHand(t *testing.T) {
	api, pool := newAPI(t)
	skuID, code := createSKU(t, api, "receipt", 2)

	response, body := doJSON(t, http.MethodPost, api.URL+"/inventory/receipts", map[string]any{
		"sku": code, "quantity": 3, "actor": "operator",
	}, nil)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing reason: expected 400, got %d body %s", response.StatusCode, body)
	}

	response, body = doJSON(t, http.MethodPost, api.URL+"/inventory/receipts", map[string]any{
		"sku": code, "quantity": 3, "actor": "operator", "reason": "supplier delivery",
	}, nil)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("receipt: status %d body %s", response.StatusCode, body)
	}

	balance := getBalance(t, api, skuID)
	if balance.OnHand != 5 || balance.Available != 5 {
		t.Fatalf("unexpected balance after receipt: %+v", balance)
	}

	var movements int
	if err := pool.QueryRow(context.Background(), `
		select count(*) from inventory_movements
		where sku_id = $1 and kind = 'receipt'`, skuID).Scan(&movements); err != nil {
		t.Fatalf("count movements: %v", err)
	}
	if movements != 2 {
		t.Fatalf("expected 2 receipt movements, got %d", movements)
	}
}

func TestMovementHistoryEndpoint(t *testing.T) {
	api, _ := newAPI(t)
	skuID, _ := createSKU(t, api, "history", 4)

	response, body := doJSON(t, http.MethodGet, api.URL+"/inventory/movements?sku_id="+skuID, nil, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("movements: status %d body %s", response.StatusCode, body)
	}
	var decoded struct {
		Movements []struct {
			Kind     string `json:"kind"`
			SKUID    string `json:"sku_id"`
			Actor    string `json:"actor"`
			Quantity int    `json:"delta_on_hand"`
		} `json:"movements"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode movements: %v", err)
	}
	if len(decoded.Movements) == 0 {
		t.Fatal("expected at least one movement")
	}
	if decoded.Movements[0].Kind != "receipt" {
		t.Fatalf("expected newest movement to be receipt, got %q", decoded.Movements[0].Kind)
	}
}
