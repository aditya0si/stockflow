package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"
)

// Test 1: fifty simultaneous buyers cannot oversell one unit.
func TestOneUnitFiftyBuyers(t *testing.T) {
	api, pool := newAPI(t)
	skuID, code := createSKU(t, api, "contention", 1)

	const buyers = 50
	statuses := make([]int, buyers)
	bodies := make([][]byte, buyers)
	orderIDs := make([]string, buyers)
	start := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < buyers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			response, body := createOrder(t, api, "buyer", fmt.Sprintf("contention-%s-%d", uuid.NewString(), i), []line{{SKU: code, Quantity: 1}})
			statuses[i] = response.StatusCode
			bodies[i] = body
			if response.StatusCode == http.StatusCreated {
				var order orderBody
				if err := json.Unmarshal(body, &order); err == nil {
					orderIDs[i] = order.ID
				}
			}
		}(i)
	}
	close(start)
	wg.Wait()

	accepted := 0
	for i, status := range statuses {
		switch status {
		case http.StatusCreated:
			accepted++
		case http.StatusConflict:
		default:
			t.Fatalf("buyer %d received unexpected status %d: %s", i, status, bodies[i])
		}
	}
	if accepted != 1 {
		t.Fatalf("expected exactly 1 accepted order, got %d", accepted)
	}

	balance := getBalance(t, api, skuID)
	if balance.OnHand != 1 || balance.Reserved != 1 || balance.Available != 0 {
		t.Fatalf("unexpected balance after contention: %+v", balance)
	}

	persistedOrders := scalarInt(t, pool, `
		select count(*) from orders o
		join order_items oi on oi.order_id = o.id
		where oi.sku_id = $1`, skuID)
	if persistedOrders != 1 {
		t.Fatalf("expected 1 persisted order row, got %d", persistedOrders)
	}
	persistedItems := scalarInt(t, pool, `select count(*) from order_items where sku_id = $1`, skuID)
	if persistedItems != 1 {
		t.Fatalf("expected 1 persisted order item, got %d", persistedItems)
	}
	acceptedItems := scalarInt(t, pool, `
		select count(*) from orders o
		join order_items oi on oi.order_id = o.id
		where oi.sku_id = $1 and o.status = 'accepted' and oi.quantity = 1`, skuID)
	if acceptedItems != 1 {
		t.Fatalf("expected 1 accepted order with quantity 1, got %d", acceptedItems)
	}
	var winnerID string
	for i, status := range statuses {
		if status == http.StatusCreated {
			winnerID = orderIDs[i]
			break
		}
	}
	var persistedID string
	if err := pool.QueryRow(context.Background(), `
		select o.id::text from orders o
		join order_items oi on oi.order_id = o.id
		where oi.sku_id = $1`, skuID).Scan(&persistedID); err != nil {
		t.Fatalf("query persisted order: %v", err)
	}
	if persistedID != winnerID {
		t.Fatalf("persisted order %s does not match accepted response %s", persistedID, winnerID)
	}

	active := scalarInt(t, pool, `select count(*) from reservations where sku_id = $1 and status = 'active'`, skuID)
	if active != 1 {
		t.Fatalf("expected 1 active reservation, got %d", active)
	}
	reservations := scalarInt(t, pool, `select count(*) from inventory_movements where sku_id = $1 and kind = 'reservation'`, skuID)
	if reservations != 1 {
		t.Fatalf("expected 1 reservation movement, got %d", reservations)
	}
	var minAvailable int
	if err := pool.QueryRow(context.Background(), `select min(available) from inventory_balances where sku_id = $1`, skuID).Scan(&minAvailable); err != nil {
		t.Fatalf("query available: %v", err)
	}
	if minAvailable < 0 {
		t.Fatalf("available went negative: %d", minAvailable)
	}
}

// Test 2: same idempotency key and body returns the original order without a second reservation.
func TestIdempotentRetryReturnsOriginalOrder(t *testing.T) {
	api, pool := newAPI(t)
	skuID, code := createSKU(t, api, "idem-retry", 5)

	key := "retry-" + uuid.NewString()
	firstResponse, firstBody := createOrder(t, api, "checkout", key, []line{{SKU: code, Quantity: 2}})
	if firstResponse.StatusCode != http.StatusCreated {
		t.Fatalf("first create: status %d body %s", firstResponse.StatusCode, firstBody)
	}
	secondResponse, secondBody := createOrder(t, api, "checkout", key, []line{{SKU: code, Quantity: 2}})
	if secondResponse.StatusCode != http.StatusCreated {
		t.Fatalf("replay create: status %d body %s", secondResponse.StatusCode, secondBody)
	}

	first := decodeOrder(t, firstBody)
	second := decodeOrder(t, secondBody)
	if first.ID != second.ID {
		t.Fatalf("expected identical order id, got %s and %s", first.ID, second.ID)
	}

	reserved := scalarInt(t, pool, `select reserved from inventory_balances where sku_id = $1`, skuID)
	if reserved != 2 {
		t.Fatalf("expected reserved=2 after replay, got %d", reserved)
	}
	active := scalarInt(t, pool, `
		select count(*) from reservations r
		join order_items oi on oi.id = r.order_item_id
		where oi.sku_id = $1 and r.status = 'active'`, skuID)
	if active != 1 {
		t.Fatalf("expected 1 active reservation after replay, got %d", active)
	}
	orderCount := scalarInt(t, pool, `
		select count(distinct o.id) from orders o
		join order_items oi on oi.order_id = o.id
		where oi.sku_id = $1`, skuID)
	if orderCount != 1 {
		t.Fatalf("expected 1 order after replay, got %d", orderCount)
	}
}

// Test 3: same idempotency key with a changed body returns 409 and performs no mutation.
func TestIdempotencyPayloadConflict(t *testing.T) {
	api, pool := newAPI(t)
	skuID, code := createSKU(t, api, "idem-conflict", 5)

	key := "conflict-" + uuid.NewString()
	firstResponse, firstBody := createOrder(t, api, "checkout", key, []line{{SKU: code, Quantity: 2}})
	if firstResponse.StatusCode != http.StatusCreated {
		t.Fatalf("first create: status %d body %s", firstResponse.StatusCode, firstBody)
	}

	conflictResponse, conflictBody := createOrder(t, api, "checkout", key, []line{{SKU: code, Quantity: 3}})
	if conflictResponse.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d body %s", conflictResponse.StatusCode, conflictBody)
	}
	if contentType := conflictResponse.Header.Get("Content-Type"); contentType != "application/problem+json" {
		t.Fatalf("expected problem+json content type, got %q", contentType)
	}
	problem := decodeProblem(t, conflictBody)
	if problem.Code != "idempotency_key_reuse" {
		t.Fatalf("expected idempotency_key_reuse, got %q", problem.Code)
	}

	reserved := scalarInt(t, pool, `select reserved from inventory_balances where sku_id = $1`, skuID)
	if reserved != 2 {
		t.Fatalf("conflicting payload mutated stock: reserved=%d", reserved)
	}
	orderCount := scalarInt(t, pool, `
		select count(distinct o.id) from orders o
		join order_items oi on oi.order_id = o.id
		where oi.sku_id = $1`, skuID)
	if orderCount != 1 {
		t.Fatalf("conflicting payload created an order: count=%d", orderCount)
	}
}

// Test 4: a multi-SKU order with one unavailable line rolls back every write.
func TestMultiSKURollback(t *testing.T) {
	api, pool := newAPI(t)
	availableID, availableCode := createSKU(t, api, "rollback-a", 5)
	emptyID, emptyCode := createSKU(t, api, "rollback-b", 0)

	scope := "checkout"
	key := "rollback-" + uuid.NewString()
	response, body := createOrder(t, api, scope, key, []line{
		{SKU: availableCode, Quantity: 1},
		{SKU: emptyCode, Quantity: 1},
	})
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d body %s", response.StatusCode, body)
	}
	problem := decodeProblem(t, body)
	if problem.Code != "insufficient_stock" {
		t.Fatalf("expected insufficient_stock, got %q", problem.Code)
	}

	idempotencyResidue := scalarInt(t, pool, `
		select count(*) from idempotency_records
		where scope = $1 and key = $2`, scope, key)
	if idempotencyResidue != 0 {
		t.Fatalf("failed order left idempotency residue: %d", idempotencyResidue)
	}
	orderResidue := scalarInt(t, pool, `
		select count(*) from orders o
		join order_items oi on oi.order_id = o.id
		where oi.sku_id = any($1)`, []string{availableID, emptyID})
	if orderResidue != 0 {
		t.Fatalf("failed order left order rows behind: %d", orderResidue)
	}

	for _, skuID := range []string{availableID, emptyID} {
		reserved := scalarInt(t, pool, `select reserved from inventory_balances where sku_id = $1`, skuID)
		if reserved != 0 {
			t.Fatalf("sku %s kept a reservation after rollback: %d", skuID, reserved)
		}
		active := scalarInt(t, pool, `select count(*) from reservations where sku_id = $1`, skuID)
		if active != 0 {
			t.Fatalf("sku %s has reservations after rollback: %d", skuID, active)
		}
	}
	for _, skuID := range []string{availableID, emptyID} {
		items := scalarInt(t, pool, `select count(*) from order_items where sku_id = $1`, skuID)
		if items != 0 {
			t.Fatalf("sku %s has order items after rollback: %d", skuID, items)
		}
	}
	available := getBalance(t, api, availableID)
	if available.OnHand != 5 || available.Reserved != 0 || available.Available != 5 {
		t.Fatalf("available sku changed during rollback: %+v", available)
	}
	reservationMovements := scalarInt(t, pool, `
		select count(*) from inventory_movements
		where kind = 'reservation' and sku_id = any($1)`, []string{availableID, emptyID})
	if reservationMovements != 0 {
		t.Fatalf("reservation movements written during rollback: %d", reservationMovements)
	}
}

// Test 5: repeating cancellation does not release stock twice.
func TestRepeatedCancellationReleasesOnce(t *testing.T) {
	api, pool := newAPI(t)
	skuID, code := createSKU(t, api, "cancel", 5)

	response, body := createOrder(t, api, "checkout", "cancel-"+uuid.NewString(), []line{{SKU: code, Quantity: 2}})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create order: status %d body %s", response.StatusCode, body)
	}
	order := decodeOrder(t, body)

	cancelURL := api.URL + "/orders/" + order.ID + "/cancel"
	firstCancel, firstCancelBody := doJSON(t, http.MethodPost, cancelURL, map[string]any{"actor": "operator", "reason": "customer changed mind"}, nil)
	if firstCancel.StatusCode != http.StatusOK {
		t.Fatalf("first cancel: status %d body %s", firstCancel.StatusCode, firstCancelBody)
	}
	firstCancelled := decodeOrder(t, firstCancelBody)
	if firstCancelled.Status != "cancelled" {
		t.Fatalf("expected cancelled order, got %q", firstCancelled.Status)
	}

	secondCancel, secondCancelBody := doJSON(t, http.MethodPost, cancelURL, map[string]any{"actor": "operator"}, nil)
	if secondCancel.StatusCode != http.StatusOK {
		t.Fatalf("second cancel: status %d body %s", secondCancel.StatusCode, secondCancelBody)
	}

	balance := getBalance(t, api, skuID)
	if balance.OnHand != 5 || balance.Reserved != 0 || balance.Available != 5 {
		t.Fatalf("unexpected balance after cancellation: %+v", balance)
	}
	releases := scalarInt(t, pool, `
		select count(*) from inventory_movements
		where order_id = $1 and kind = 'release'`, order.ID)
	if releases != 1 {
		t.Fatalf("expected exactly 1 release movement, got %d", releases)
	}
	active := scalarInt(t, pool, `select count(*) from reservations where order_id = $1 and status = 'active'`, order.ID)
	if active != 0 {
		t.Fatalf("expected no active reservations after cancel, got %d", active)
	}
	released := scalarInt(t, pool, `select count(*) from reservations where order_id = $1 and status = 'released'`, order.ID)
	if released != 1 {
		t.Fatalf("expected exactly 1 released reservation, got %d", released)
	}
}

func TestConcurrentSameKeyCreatesOneOrder(t *testing.T) {
	api, pool := newAPI(t)
	skuID, code := createSKU(t, api, "idem-concurrent", 5)

	key := "concurrent-" + uuid.NewString()
	const attempts = 12
	ids := make([]string, attempts)
	bodies := make([][]byte, attempts)
	statuses := make([]int, attempts)
	start := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			response, body := createOrder(t, api, "checkout", key, []line{{SKU: code, Quantity: 2}})
			statuses[i] = response.StatusCode
			bodies[i] = body
			if response.StatusCode == http.StatusCreated {
				var order orderBody
				if err := json.Unmarshal(body, &order); err == nil {
					ids[i] = order.ID
				}
			}
		}(i)
	}
	close(start)
	wg.Wait()

	for i, status := range statuses {
		if status != http.StatusCreated {
			t.Fatalf("attempt %d: expected 201, got %d body %s", i, status, bodies[i])
		}
		if ids[i] != ids[0] {
			t.Fatalf("attempt %d returned order %s, want %s", i, ids[i], ids[0])
		}
	}

	reserved := scalarInt(t, pool, `select reserved from inventory_balances where sku_id = $1`, skuID)
	if reserved != 2 {
		t.Fatalf("expected reserved=2, got %d", reserved)
	}
	orderCount := scalarInt(t, pool, `
		select count(distinct o.id) from orders o
		join order_items oi on oi.order_id = o.id
		where oi.sku_id = $1`, skuID)
	if orderCount != 1 {
		t.Fatalf("expected 1 order from concurrent same-key requests, got %d", orderCount)
	}
}

func TestMovementsAreAppendOnly(t *testing.T) {
	api, pool := newAPI(t)
	_, code := createSKU(t, api, "immutable", 3)

	var movementID int64
	if err := pool.QueryRow(context.Background(), `
		select m.id from inventory_movements m
		join skus s on s.id = m.sku_id
		where s.code = $1
		order by m.id
		limit 1`, code).Scan(&movementID); err != nil {
		t.Fatalf("select movement: %v", err)
	}

	if _, err := pool.Exec(context.Background(),
		`update inventory_movements set actor = 'tamper' where id = $1`, movementID); err == nil {
		t.Fatal("expected update of append-only movement to fail")
	}
	if _, err := pool.Exec(context.Background(),
		`delete from inventory_movements where id = $1`, movementID); err == nil {
		t.Fatal("expected delete of append-only movement to fail")
	}
}

func TestOppositeOrderMultiSKUContention(t *testing.T) {
	api, pool := newAPI(t)
	skuAID, codeA := createSKU(t, api, "opposite-a", 10)
	skuBID, codeB := createSKU(t, api, "opposite-b", 10)

	const opening = 10
	const attempts = 28
	const scope = "opposite"

	statuses := make([]int, attempts)
	bodies := make([][]byte, attempts)
	orderIDs := make([]string, attempts)
	start := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			lines := []line{{SKU: codeA, Quantity: 1}, {SKU: codeB, Quantity: 1}}
			if i%2 == 1 {
				lines = []line{{SKU: codeB, Quantity: 1}, {SKU: codeA, Quantity: 1}}
			}
			<-start
			response, body := createOrder(t, api, scope, fmt.Sprintf("opposite-%s-%d", uuid.NewString(), i), lines)
			statuses[i] = response.StatusCode
			bodies[i] = body
			if response.StatusCode == http.StatusCreated {
				var order orderBody
				if err := json.Unmarshal(body, &order); err == nil {
					orderIDs[i] = order.ID
				}
			}
		}(i)
	}
	close(start)
	wg.Wait()

	accepted := 0
	for i, status := range statuses {
		switch status {
		case http.StatusCreated:
			accepted++
			if orderIDs[i] == "" {
				t.Fatalf("attempt %d: accepted without decodable order: %s", i, bodies[i])
			}
		case http.StatusConflict:
			if problem := decodeProblem(t, bodies[i]); problem.Code != "insufficient_stock" {
				t.Fatalf("attempt %d: expected insufficient_stock, got %q", i, problem.Code)
			}
		default:
			t.Fatalf("attempt %d: unexpected status %d: %s", i, status, bodies[i])
		}
	}
	if accepted != opening {
		t.Fatalf("expected %d accepted orders, got %d", opening, accepted)
	}

	for _, skuID := range []string{skuAID, skuBID} {
		balance := getBalance(t, api, skuID)
		if balance.OnHand != opening || balance.Reserved != opening || balance.Available != 0 {
			t.Fatalf("sku %s unexpected balance after contention: %+v", skuID, balance)
		}
		active := scalarInt(t, pool, `select count(*) from reservations where sku_id = $1 and status = 'active'`, skuID)
		if active != opening {
			t.Fatalf("sku %s expected %d active reservations, got %d", skuID, opening, active)
		}
		movements := scalarInt(t, pool, `select count(*) from inventory_movements where sku_id = $1 and kind = 'reservation'`, skuID)
		if movements != opening {
			t.Fatalf("sku %s expected %d reservation movements, got %d", skuID, opening, movements)
		}
	}

	skuIDs := []string{skuAID, skuBID}
	partialOrders := scalarInt(t, pool, `
		select count(*) from (
			select oi.order_id
			from order_items oi
			where oi.sku_id = any($1)
			group by oi.order_id
			having count(*) <> 2
		) partial`, skuIDs)
	if partialOrders != 0 {
		t.Fatalf("found %d partially written multi-SKU orders", partialOrders)
	}
	partialReservations := scalarInt(t, pool, `
		select count(*) from (
			select r.order_id
			from reservations r
			where r.sku_id = any($1) and r.status = 'active'
			group by r.order_id
			having count(*) <> 2
		) partial`, skuIDs)
	if partialReservations != 0 {
		t.Fatalf("found %d orders with partial reservations", partialReservations)
	}
}

func TestConcurrentCancellationReleasesOnce(t *testing.T) {
	api, pool := newAPI(t)
	skuID, code := createSKU(t, api, "cancel-race", 5)

	response, body := createOrder(t, api, "checkout", "cancel-race-"+uuid.NewString(), []line{{SKU: code, Quantity: 3}})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create order: status %d body %s", response.StatusCode, body)
	}
	order := decodeOrder(t, body)

	const attempts = 12
	statuses := make([]int, attempts)
	bodies := make([][]byte, attempts)
	start := make(chan struct{})
	var wg sync.WaitGroup

	cancelURL := api.URL + "/orders/" + order.ID + "/cancel"
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			response, responseBody := doJSON(t, http.MethodPost, cancelURL, map[string]any{"actor": "operator", "reason": "concurrent cancellation"}, nil)
			statuses[i] = response.StatusCode
			bodies[i] = responseBody
		}(i)
	}
	close(start)
	wg.Wait()

	for i, status := range statuses {
		if status != http.StatusOK {
			t.Fatalf("cancel attempt %d: expected 200, got %d body %s", i, status, bodies[i])
		}
		cancelled := decodeOrder(t, bodies[i])
		if cancelled.ID != order.ID || cancelled.Status != "cancelled" {
			t.Fatalf("cancel attempt %d returned incoherent order: %+v", i, cancelled)
		}
	}

	balance := getBalance(t, api, skuID)
	if balance.OnHand != 5 || balance.Reserved != 0 || balance.Available != 5 {
		t.Fatalf("unexpected balance after concurrent cancellation: %+v", balance)
	}
	var status string
	if err := pool.QueryRow(context.Background(), `select status from orders where id = $1`, order.ID).Scan(&status); err != nil {
		t.Fatalf("query order status: %v", err)
	}
	if status != "cancelled" {
		t.Fatalf("expected cancelled order, got %q", status)
	}
	active := scalarInt(t, pool, `select count(*) from reservations where order_id = $1 and status = 'active'`, order.ID)
	if active != 0 {
		t.Fatalf("expected no active reservations after cancellation, got %d", active)
	}
	released := scalarInt(t, pool, `select count(*) from reservations where order_id = $1 and status = 'released'`, order.ID)
	if released != 1 {
		t.Fatalf("expected 1 released reservation, got %d", released)
	}
	releases := scalarInt(t, pool, `select count(*) from inventory_movements where order_id = $1 and kind = 'release'`, order.ID)
	if releases != 1 {
		t.Fatalf("expected 1 release movement, got %d", releases)
	}
}
