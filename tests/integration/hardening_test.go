package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// execAsReplica runs a statement with session_replication_role set to replica,
// which disables foreign-key and user triggers for that statement. It is only
// used to inject the historical/legacy corruption that reconciliation must
// detect, since the V2 constraints otherwise make such rows impossible. It
// requires a superuser connection (the test database owner is one).
func execAsReplica(t *testing.T, pool *pgxpool.Pool, query string, args ...any) {
	t.Helper()
	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire replica connection: %v", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "set session_replication_role = replica"); err != nil {
		t.Fatalf("set replica role: %v", err)
	}
	if _, err := conn.Exec(ctx, query, args...); err != nil {
		_, _ = conn.Exec(ctx, "set session_replication_role = origin")
		t.Fatalf("replica exec %q: %v", query, err)
	}
	if _, err := conn.Exec(ctx, "set session_replication_role = origin"); err != nil {
		t.Fatalf("reset replica role: %v", err)
	}
}

func injectShippedOrder(t *testing.T, pool *pgxpool.Pool, skuID string, quantity int) (uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	orderID := uuid.New()
	itemID := uuid.New()
	if _, err := pool.Exec(ctx, `
		insert into orders (id, status, created_at, updated_at, shipped_at)
		values ($1, 'shipped', now(), now(), now())`, orderID); err != nil {
		t.Fatalf("inject shipped order: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		insert into order_items (id, order_id, sku_id, quantity)
		values ($1, $2, $3, $4)`, itemID, orderID, skuID, quantity); err != nil {
		t.Fatalf("inject shipped order item: %v", err)
	}
	return orderID, itemID
}

func insertShipment(t *testing.T, pool *pgxpool.Pool, skuID, orderID, itemID string, delta int) {
	t.Helper()
	execAsReplica(t, pool, `
		insert into inventory_movements
			(sku_id, delta_on_hand, delta_reserved, kind, actor, reason, order_id, order_item_id)
		values ($1, $2, 0, 'shipment', 'legacy', 'legacy shipment evidence', $3, $4)`,
		skuID, delta, orderID, itemID)
}

func reconciliationFindings(t *testing.T, api *httptest.Server, checkName string) []findingBody {
	t.Helper()
	response, body := runReconciliation(t, api)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("reconciliation: status %d body %s", response.StatusCode, body)
	}
	run := decodeRun(t, body)
	found := make([]findingBody, 0)
	for _, finding := range run.Findings {
		if finding.CheckName == checkName {
			found = append(found, finding)
		}
	}
	return found
}

func TestReconciliationDetectsOnHandDriftAgainstLedger(t *testing.T) {
	api, pool := newAPI(t)
	skuID, _ := createSKU(t, api, "drift", 5)

	if _, err := pool.Exec(context.Background(),
		`update inventory_balances set on_hand = on_hand + 3 where sku_id = $1`, skuID); err != nil {
		t.Fatalf("inject on-hand drift: %v", err)
	}

	findings := reconciliationFindings(t, api, "balance_matches_movement_ledger")
	matched := false
	for _, finding := range findings {
		if finding.SKUID != skuID {
			continue
		}
		if !strings.Contains(finding.Expected, "on_hand=5") || !strings.Contains(finding.Observed, "on_hand=8") {
			t.Fatalf("unexpected drift finding: expected=%q observed=%q", finding.Expected, finding.Observed)
		}
		matched = true
	}
	if !matched {
		t.Fatalf("no balance_matches_movement_ledger finding for sku %s", skuID)
	}

	// Report-only: the drifted balance must be untouched.
	var onHand int
	if err := pool.QueryRow(context.Background(),
		`select on_hand from inventory_balances where sku_id = $1`, skuID).Scan(&onHand); err != nil {
		t.Fatalf("read drifted balance: %v", err)
	}
	if onHand != 8 {
		t.Fatalf("reconciliation repaired on_hand to %d; it must be report-only", onHand)
	}
}

func TestReconciliationDetectsMissingBalanceRow(t *testing.T) {
	api, pool := newAPI(t)
	skuID, code := createSKU(t, api, "missing-balance", 4)

	response, body := createOrder(t, api, "checkout", "missing-balance-"+uuid.NewString(), []line{{SKU: code, Quantity: 1}})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create order: status %d body %s", response.StatusCode, body)
	}
	if reserved := scalarInt(t, pool, `select reserved from inventory_balances where sku_id = $1`, skuID); reserved != 1 {
		t.Fatalf("expected reserved=1, got %d", reserved)
	}

	if _, err := pool.Exec(context.Background(),
		`delete from inventory_balances where sku_id = $1`, skuID); err != nil {
		t.Fatalf("delete balance row: %v", err)
	}

	findings := reconciliationFindings(t, api, "balance_matches_movement_ledger")
	matched := false
	for _, finding := range findings {
		if finding.SKUID != skuID {
			continue
		}
		if finding.Observed != "no balance row" {
			t.Fatalf("expected a missing-balance observation, got %q", finding.Observed)
		}
		if !strings.Contains(finding.Expected, "on_hand=4") || !strings.Contains(finding.Expected, "reserved=1") {
			t.Fatalf("expected ledger-derived values, got %q", finding.Expected)
		}
		matched = true
	}
	if !matched {
		t.Fatalf("reconciliation did not report the missing balance row with movement evidence")
	}
}

func TestReconciliationDetectsShipmentEvidenceCorruption(t *testing.T) {
	api, pool := newAPI(t)
	skuID, _ := createSKU(t, api, "evidence", 10)
	otherSKUID, _ := createSKU(t, api, "evidence-other", 0)

	missingOrder, _ := injectShippedOrder(t, pool, skuID, 2)

	duplicateOrder, duplicateItem := injectShippedOrder(t, pool, skuID, 2)
	insertShipment(t, pool, skuID, duplicateOrder.String(), duplicateItem.String(), -2)
	insertShipment(t, pool, skuID, duplicateOrder.String(), duplicateItem.String(), -2)

	wrongQuantityOrder, wrongQuantityItem := injectShippedOrder(t, pool, skuID, 2)
	insertShipment(t, pool, skuID, wrongQuantityOrder.String(), wrongQuantityItem.String(), -3)

	wrongSKUOrder, wrongSKUItem := injectShippedOrder(t, pool, skuID, 2)
	insertShipment(t, pool, otherSKUID, wrongSKUOrder.String(), wrongSKUItem.String(), -2)

	wrongOrderOrder, wrongOrderItem := injectShippedOrder(t, pool, skuID, 2)
	insertShipment(t, pool, skuID, duplicateOrder.String(), wrongOrderItem.String(), -2)

	findings := reconciliationFindings(t, api, "shipped_line_has_one_matching_decrement")
	if len(findings) == 0 {
		t.Fatal("reconciliation reported no shipment evidence findings")
	}

	has := func(orderID uuid.UUID, substr string) bool {
		for _, finding := range findings {
			if finding.OrderID == orderID.String() && strings.Contains(finding.Observed, substr) {
				return true
			}
		}
		return false
	}

	if !has(missingOrder, "0 shipment movements") {
		t.Fatalf("missing shipment evidence not reported: %+v", findings)
	}
	if !has(duplicateOrder, "2 shipment movements") {
		t.Fatalf("duplicate shipment evidence not reported: %+v", findings)
	}
	if !has(wrongQuantityOrder, "totalling -3") {
		t.Fatalf("wrong-quantity shipment evidence not reported: %+v", findings)
	}
	if !has(wrongSKUOrder, "0 matching") {
		t.Fatalf("wrong-SKU shipment evidence not reported: %+v", findings)
	}
	if !has(wrongOrderOrder, "0 matching") {
		t.Fatalf("wrong-order shipment evidence not reported: %+v", findings)
	}

	// The wrong-SKU and wrong-order movements are also flagged by the identity
	// reference check.
	flaggedWrongSKU := false
	flaggedWrongOrder := false
	for _, finding := range findings {
		if strings.Contains(finding.Observed, otherSKUID) {
			flaggedWrongSKU = true
		}
		if strings.Contains(finding.Observed, "order="+duplicateOrder.String()) {
			flaggedWrongOrder = true
		}
	}
	if !flaggedWrongSKU {
		t.Fatalf("wrong-SKU movement was not flagged by reference check: %+v", findings)
	}
	if !flaggedWrongOrder {
		t.Fatalf("wrong-order movement was not flagged by reference check: %+v", findings)
	}
}

func preparePackedOrder(t *testing.T, api *httptest.Server, prefix string) (string, string, string) {
	t.Helper()
	skuID, code := createSKU(t, api, prefix, 5)
	response, body := createOrder(t, api, "checkout", prefix+"-"+uuid.NewString(), []line{{SKU: code, Quantity: 2}})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create order: status %d body %s", response.StatusCode, body)
	}
	order := decodeOrder(t, body)
	for _, action := range []string{"pick", "pack"} {
		if response, body := transition(t, api, order.ID, action, "operator"); response.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d body %s", action, response.StatusCode, body)
		}
	}
	return skuID, order.ID, order.Items[0].ID
}

func assertShipRejectedWithoutMutation(t *testing.T, api *httptest.Server, pool *pgxpool.Pool, skuID, orderID string) {
	t.Helper()
	beforeOrderStatus := "packed"
	beforeMovements := scalarInt(t, pool, `select count(*) from inventory_movements where order_id = $1`, orderID)
	beforeBalance := getBalance(t, api, skuID)

	response, body := transition(t, api, orderID, "ship", "operator")
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("ship with corrupt reservations: expected 409, got %d body %s", response.StatusCode, body)
	}
	if problem := decodeProblem(t, body); problem.Code != "reservation_inconsistent" {
		t.Fatalf("expected reservation_inconsistent, got %q", problem.Code)
	}

	var status string
	if err := pool.QueryRow(context.Background(), `select status from orders where id = $1`, orderID).Scan(&status); err != nil {
		t.Fatalf("read order status: %v", err)
	}
	if status != beforeOrderStatus {
		t.Fatalf("rejected shipment changed order status to %q", status)
	}
	if after := scalarInt(t, pool, `select count(*) from inventory_movements where order_id = $1`, orderID); after != beforeMovements {
		t.Fatalf("rejected shipment wrote movements: %d -> %d", beforeMovements, after)
	}
	afterBalance := getBalance(t, api, skuID)
	if afterBalance != beforeBalance {
		t.Fatalf("rejected shipment changed balance: %+v -> %+v", beforeBalance, afterBalance)
	}
}

func TestShipRejectsMissingReservation(t *testing.T) {
	api, pool := newAPI(t)
	skuID, orderID, itemID := preparePackedOrder(t, api, "ship-missing")
	if _, err := pool.Exec(context.Background(), `delete from reservations where order_item_id = $1`, itemID); err != nil {
		t.Fatalf("delete reservation: %v", err)
	}
	assertShipRejectedWithoutMutation(t, api, pool, skuID, orderID)
}

func TestShipRejectsReleasedReservation(t *testing.T) {
	api, pool := newAPI(t)
	skuID, orderID, itemID := preparePackedOrder(t, api, "ship-released")
	if _, err := pool.Exec(context.Background(),
		`update reservations set status = 'released', released_at = now() where order_item_id = $1`, itemID); err != nil {
		t.Fatalf("release reservation: %v", err)
	}
	assertShipRejectedWithoutMutation(t, api, pool, skuID, orderID)
}

func TestShipRejectsAlteredReservation(t *testing.T) {
	api, pool := newAPI(t)
	skuID, orderID, itemID := preparePackedOrder(t, api, "ship-altered")
	if _, err := pool.Exec(context.Background(),
		`update reservations set quantity = quantity + 1 where order_item_id = $1`, itemID); err != nil {
		t.Fatalf("alter reservation: %v", err)
	}
	assertShipRejectedWithoutMutation(t, api, pool, skuID, orderID)
}

func TestCrossTableConstraintsRejectMismatchedReferences(t *testing.T) {
	api, pool := newAPI(t)
	skuAID, codeA := createSKU(t, api, "constraint-a", 5)
	skuBID, codeB := createSKU(t, api, "constraint-b", 5)

	response, body := createOrder(t, api, "checkout", "constraint-a-"+uuid.NewString(), []line{{SKU: codeA, Quantity: 1}})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create order A: status %d body %s", response.StatusCode, body)
	}
	orderA := decodeOrder(t, body)

	response, body = createOrder(t, api, "checkout", "constraint-b-"+uuid.NewString(), []line{{SKU: codeB, Quantity: 1}})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create order B: status %d body %s", response.StatusCode, body)
	}
	orderB := decodeOrder(t, body)

	var itemAID string
	if err := pool.QueryRow(context.Background(),
		`select id::text from order_items where order_id = $1`, orderA.ID).Scan(&itemAID); err != nil {
		t.Fatalf("read order A item: %v", err)
	}
	codeChecks := []struct {
		name  string
		query string
		args  []any
	}{
		{
			name: "reservation cross-order reference",
			query: `
				insert into reservations (id, order_id, order_item_id, sku_id, quantity, status)
				values ($1, $2, $3, $4, 1, 'active')`,
			args: []any{uuid.NewString(), orderB.ID, itemAID, skuAID},
		},
		{
			name:  "reservation cross-sku update",
			query: `update reservations set sku_id = $2 where order_item_id = $1`,
			args:  []any{itemAID, skuBID},
		},
		{
			name: "movement cross-reference",
			query: `
				insert into inventory_movements
					(sku_id, delta_on_hand, delta_reserved, kind, actor, reason, order_id, order_item_id)
				values ($1, -1, 0, 'shipment', 'tester', 'bad', $2, $3)`,
			args: []any{skuBID, orderB.ID, itemAID},
		},
	}
	for _, check := range codeChecks {
		t.Run(check.name, func(t *testing.T) {
			if _, err := pool.Exec(context.Background(), check.query, check.args...); err == nil {
				t.Fatalf("expected constraint violation for %s", check.name)
			}
		})
	}

	// No mismatched reservation or movement was written.
	crossOrder := scalarInt(t, pool, `
		select count(*) from reservations
		where order_item_id = $1 and order_id <> (select order_id from order_items where id = $1)`, itemAID)
	if crossOrder != 0 {
		t.Fatalf("found %d cross-order reservations after rejected inserts", crossOrder)
	}
}
