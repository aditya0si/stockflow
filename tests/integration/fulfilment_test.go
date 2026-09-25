package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"
)

// Test 1 (V1): cancellation racing shipment yields one coherent terminal
// outcome and one stock effect.
func TestCancellationVersusShipmentRace(t *testing.T) {
	api, pool := newAPI(t)
	skuID, code := createSKU(t, api, "race", 1)

	response, body := createOrder(t, api, "checkout", "race-"+uuid.NewString(), []line{{SKU: code, Quantity: 1}})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create order: status %d body %s", response.StatusCode, body)
	}
	order := decodeOrder(t, body)

	for _, action := range []string{"pick", "pack"} {
		if response, body := transition(t, api, order.ID, action, "operator"); response.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d body %s", action, response.StatusCode, body)
		}
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	statuses := make([]int, 2)
	bodies := make([][]byte, 2)
	cancelURL := api.URL + "/orders/" + order.ID + "/cancel"

	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		response, responseBody := doJSON(t, http.MethodPost, cancelURL,
			map[string]any{"actor": "operator", "reason": "customer cancelled"}, nil)
		statuses[0], bodies[0] = response.StatusCode, responseBody
	}()
	go func() {
		defer wg.Done()
		<-start
		response, responseBody := transition(t, api, order.ID, "ship", "operator")
		statuses[1], bodies[1] = response.StatusCode, responseBody
	}()
	close(start)
	wg.Wait()

	winners := 0
	losers := 0
	for i, status := range statuses {
		switch status {
		case http.StatusOK:
			winners++
		case http.StatusConflict:
			losers++
			if problem := decodeProblem(t, bodies[i]); problem.Code != "illegal_transition" {
				t.Fatalf("loser %d expected illegal_transition, got %q", i, problem.Code)
			}
		default:
			t.Fatalf("race participant %d unexpected status %d: %s", i, status, bodies[i])
		}
	}
	if winners != 1 || losers != 1 {
		t.Fatalf("expected one winner and one loser, got winners=%d losers=%d", winners, losers)
	}

	var finalStatus string
	if err := pool.QueryRow(context.Background(), `select status from orders where id = $1`, order.ID).Scan(&finalStatus); err != nil {
		t.Fatalf("query status: %v", err)
	}
	balance := getBalance(t, api, skuID)
	shipments := scalarInt(t, pool, `select count(*) from inventory_movements where order_id = $1 and kind = 'shipment'`, order.ID)
	releases := scalarInt(t, pool, `select count(*) from inventory_movements where order_id = $1 and kind = 'release'`, order.ID)
	active := scalarInt(t, pool, `select count(*) from reservations where order_id = $1 and status = 'active'`, order.ID)

	if active != 0 {
		t.Fatalf("expected no active reservation after the race, got %d", active)
	}
	if balance.Reserved != 0 {
		t.Fatalf("expected reserved=0 after the race, got %d", balance.Reserved)
	}

	switch finalStatus {
	case "cancelled":
		if balance.OnHand != 1 || shipments != 0 || releases != 1 {
			t.Fatalf("cancelled outcome incoherent: balance=%+v shipments=%d releases=%d", balance, shipments, releases)
		}
	case "shipped":
		if balance.OnHand != 0 || shipments != 1 || releases != 1 {
			t.Fatalf("shipped outcome incoherent: balance=%+v shipments=%d releases=%d", balance, shipments, releases)
		}
	default:
		t.Fatalf("unexpected terminal status %q", finalStatus)
	}
}

// Test 2 (V1): a duplicate shipment command cannot decrement stock twice, even
// when the duplicates run concurrently.
func TestDuplicateShipmentCannotDecrementTwice(t *testing.T) {
	api, pool := newAPI(t)
	skuID, code := createSKU(t, api, "dup-ship", 3)

	response, body := createOrder(t, api, "checkout", "dup-ship-"+uuid.NewString(), []line{{SKU: code, Quantity: 2}})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create order: status %d body %s", response.StatusCode, body)
	}
	order := decodeOrder(t, body)

	for _, action := range []string{"pick", "pack"} {
		if response, body := transition(t, api, order.ID, action, "operator"); response.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d body %s", action, response.StatusCode, body)
		}
	}

	const attempts = 8
	statuses := make([]int, attempts)
	bodies := make([][]byte, attempts)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			response, responseBody := transition(t, api, order.ID, "ship", "operator")
			statuses[i], bodies[i] = response.StatusCode, responseBody
		}(i)
	}
	close(start)
	wg.Wait()

	for i, status := range statuses {
		if status != http.StatusOK {
			t.Fatalf("ship attempt %d: expected 200, got %d body %s", i, status, bodies[i])
		}
	}

	shipments := scalarInt(t, pool, `select count(*) from inventory_movements where order_id = $1 and kind = 'shipment'`, order.ID)
	if shipments != 1 {
		t.Fatalf("expected exactly 1 shipment movement, got %d", shipments)
	}
	balance := getBalance(t, api, skuID)
	if balance.OnHand != 1 || balance.Reserved != 0 || balance.Available != 1 {
		t.Fatalf("duplicate shipment changed stock more than once: %+v", balance)
	}
}

// Test 3 (V1): an illegal transition returns a stable problem and leaves all
// domain tables unchanged.
func TestIllegalTransitionLeavesDomainTablesUnchanged(t *testing.T) {
	api, pool := newAPI(t)
	_, code := createSKU(t, api, "illegal", 2)

	response, body := createOrder(t, api, "checkout", "illegal-"+uuid.NewString(), []line{{SKU: code, Quantity: 1}})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create order: status %d body %s", response.StatusCode, body)
	}
	order := decodeOrder(t, body)

	snapshot := func() map[string]int {
		return map[string]int{
			"orders":            scalarInt(t, pool, `select count(*) from orders`),
			"order_items":       scalarInt(t, pool, `select count(*) from order_items`),
			"reservations":      scalarInt(t, pool, `select count(*) from reservations`),
			"movements":         scalarInt(t, pool, `select count(*) from inventory_movements`),
			"fulfilment_events": scalarInt(t, pool, `select count(*) from fulfilment_events`),
			"audit_entries":     scalarInt(t, pool, `select count(*) from audit_entries`),
		}
	}
	before := snapshot()

	// Skipping pick: pack and ship are illegal from accepted.
	for _, action := range []string{"pack", "ship"} {
		response, body := transition(t, api, order.ID, action, "operator")
		if response.StatusCode != http.StatusConflict {
			t.Fatalf("%s from accepted: expected 409, got %d body %s", action, response.StatusCode, body)
		}
		if problem := decodeProblem(t, body); problem.Code != "illegal_transition" {
			t.Fatalf("%s: expected illegal_transition, got %q", action, problem.Code)
		}
	}

	// A legal pick followed by shipping before packing is illegal.
	if response, body := transition(t, api, order.ID, "pick", "operator"); response.StatusCode != http.StatusOK {
		t.Fatalf("pick: status %d body %s", response.StatusCode, body)
	}
	afterPick := snapshot()
	if response, body := transition(t, api, order.ID, "ship", "operator"); response.StatusCode != http.StatusConflict {
		t.Fatalf("ship from picking: expected 409, got %d body %s", response.StatusCode, body)
	}
	if got := snapshot(); !equalCounts(afterPick, got) {
		t.Fatalf("illegal ship changed tables: before=%v after=%v", afterPick, got)
	}

	// Legal pack, then a second pick is idempotent but a backward move is not.
	if response, body := transition(t, api, order.ID, "pack", "operator"); response.StatusCode != http.StatusOK {
		t.Fatalf("pack: status %d body %s", response.StatusCode, body)
	}
	if response, body := transition(t, api, order.ID, "pick", "operator"); response.StatusCode != http.StatusConflict {
		t.Fatalf("pick from packed: expected 409, got %d body %s", response.StatusCode, body)
	}
	afterPackAndIllegal := snapshot()
	if response, body := transition(t, api, order.ID, "ship", "operator"); response.StatusCode != http.StatusOK {
		t.Fatalf("ship: status %d body %s", response.StatusCode, body)
	}
	afterShip := snapshot()
	if response, body := transition(t, api, order.ID, "pick", "operator"); response.StatusCode != http.StatusConflict {
		t.Fatalf("pick from shipped: expected 409, got %d body %s", response.StatusCode, body)
	}
	if got := snapshot(); !equalCounts(afterShip, got) {
		t.Fatalf("illegal transition after shipment changed tables: before=%v after=%v", afterShip, got)
	}

	// No mutation happened for the earlier illegal attempts beyond the legal
	// pick/pack/ship events themselves.
	if before["orders"] != afterShip["orders"] || before["order_items"] != afterShip["order_items"] {
		t.Fatalf("row counts drifted unexpectedly: before=%v after=%v", before, afterShip)
	}
	if afterPackAndIllegal["movements"] != afterShip["movements"]-2 {
		t.Fatalf("unexpected movement count progression: %v -> %v", afterPackAndIllegal, afterShip)
	}
}

func equalCounts(a, b map[string]int) bool {
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

// Test 6 (V1): the end-to-end operator journey receipt -> order -> pick ->
// pack -> ship, with evidence visible through the read endpoints.
func TestOperatorJourneyReceiptOrderPickPackShip(t *testing.T) {
	api, pool := newAPI(t)
	skuID, code := createSKU(t, api, "journey", 0)

	if response, body := doJSON(t, http.MethodPost, api.URL+"/inventory/receipts", map[string]any{
		"sku": code, "quantity": 2, "actor": "operator", "reason": "supplier delivery",
	}, nil); response.StatusCode != http.StatusCreated {
		t.Fatalf("receipt: status %d body %s", response.StatusCode, body)
	}
	if balance := getBalance(t, api, skuID); balance.OnHand != 2 || balance.Available != 2 {
		t.Fatalf("balance after receipt: %+v", balance)
	}

	response, body := createOrder(t, api, "operator", "journey-"+uuid.NewString(), []line{{SKU: code, Quantity: 1}})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("order: status %d body %s", response.StatusCode, body)
	}
	order := decodeOrder(t, body)

	for _, action := range []string{"pick", "pack", "ship"} {
		if response, body := transition(t, api, order.ID, action, "operator"); response.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d body %s", action, response.StatusCode, body)
		}
	}

	balance := getBalance(t, api, skuID)
	if balance.OnHand != 1 || balance.Reserved != 0 || balance.Available != 1 {
		t.Fatalf("balance after shipment: %+v", balance)
	}

	response, body = doJSON(t, http.MethodGet, api.URL+"/orders/"+order.ID, nil, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("get order: status %d body %s", response.StatusCode, body)
	}
	var finalOrder struct {
		Status    string `json:"status"`
		ShippedAt string `json:"shipped_at"`
	}
	if err := json.Unmarshal(body, &finalOrder); err != nil {
		t.Fatalf("decode order: %v", err)
	}
	if finalOrder.Status != "shipped" || finalOrder.ShippedAt == "" {
		t.Fatalf("unexpected final order: %+v", finalOrder)
	}

	response, body = doJSON(t, http.MethodGet, api.URL+"/orders/"+order.ID+"/events", nil, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("events: status %d body %s", response.StatusCode, body)
	}
	var events struct {
		Events []eventBody `json:"events"`
	}
	if err := json.Unmarshal(body, &events); err != nil {
		t.Fatalf("decode events: %v", err)
	}
	if len(events.Events) != 3 {
		t.Fatalf("expected 3 fulfilment events, got %d: %s", len(events.Events), body)
	}
	wantStates := []string{"picking", "packed", "shipped"}
	for i, event := range events.Events {
		if event.ToStatus != wantStates[i] {
			t.Fatalf("event %d: expected %s, got %s", i, wantStates[i], event.ToStatus)
		}
	}

	movements := scalarInt(t, pool, `
		select count(*) from inventory_movements
		where order_id = $1 and kind in ('reservation', 'release', 'shipment')`, order.ID)
	if movements != 3 {
		t.Fatalf("expected reservation, release, and shipment movements, got %d", movements)
	}
	audit := scalarInt(t, pool, `select count(*) from audit_entries where entity_id = $1`, order.ID)
	if audit < 4 {
		t.Fatalf("expected order_created plus three transitions in audit, got %d", audit)
	}
}
