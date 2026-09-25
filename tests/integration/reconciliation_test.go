package integration

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// Test 4 (V1): reconciliation detects a deliberately corrupted reserved
// balance and names expected vs observed values.
func TestReconciliationDetectsCorruptedReservedBalance(t *testing.T) {
	api, pool := newAPI(t)
	skuID, code := createSKU(t, api, "reconcile", 2)

	response, body := createOrder(t, api, "checkout", "reconcile-"+uuid.NewString(), []line{{SKU: code, Quantity: 1}})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create order: status %d body %s", response.StatusCode, body)
	}
	if reserved := scalarInt(t, pool, `select reserved from inventory_balances where sku_id = $1`, skuID); reserved != 1 {
		t.Fatalf("expected reserved=1, got %d", reserved)
	}

	if _, err := pool.Exec(context.Background(),
		`update inventory_balances set reserved = reserved - 1 where sku_id = $1`, skuID); err != nil {
		t.Fatalf("inject discrepancy: %v", err)
	}

	response, body = runReconciliation(t, api)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("reconciliation: status %d body %s", response.StatusCode, body)
	}
	run := decodeRun(t, body)
	if run.Status != "findings" {
		t.Fatalf("expected findings status, got %q (findings=%d)", run.Status, run.FindingsCount)
	}

	found := false
	for _, finding := range run.Findings {
		if finding.CheckName != "reserved_matches_active_reservations" || finding.SKUID != skuID {
			continue
		}
		if finding.Expected != "1" || finding.Observed != "0" {
			t.Fatalf("finding expected/observed = %s/%s, want 1/0", finding.Expected, finding.Observed)
		}
		found = true
	}
	if !found {
		t.Fatalf("no reserved_matches_active_reservations finding for sku %s: %s", skuID, body)
	}
}

// Test 5 (V1): reconciliation reports a corrupted row but never repairs it.
func TestReconciliationDoesNotRepairCorruptedRow(t *testing.T) {
	api, pool := newAPI(t)
	skuID, code := createSKU(t, api, "reconcile-norepair", 2)

	response, body := createOrder(t, api, "checkout", "norepair-"+uuid.NewString(), []line{{SKU: code, Quantity: 1}})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create order: status %d body %s", response.StatusCode, body)
	}

	if _, err := pool.Exec(context.Background(),
		`update inventory_balances set reserved = reserved - 1 where sku_id = $1`, skuID); err != nil {
		t.Fatalf("inject discrepancy: %v", err)
	}
	movementsBefore := scalarInt(t, pool, `select count(*) from inventory_movements`)

	response, body = runReconciliation(t, api)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("reconciliation: status %d body %s", response.StatusCode, body)
	}
	run := decodeRun(t, body)

	reserved := scalarInt(t, pool, `select reserved from inventory_balances where sku_id = $1`, skuID)
	if reserved != 0 {
		t.Fatalf("reconciliation repaired reserved to %d; it must be report-only", reserved)
	}
	active := scalarInt(t, pool, `select count(*) from reservations where sku_id = $1 and status = 'active'`, skuID)
	if active != 1 {
		t.Fatalf("reconciliation changed reservations: active=%d", active)
	}
	movementsAfter := scalarInt(t, pool, `select count(*) from inventory_movements`)
	if movementsBefore != movementsAfter {
		t.Fatalf("reconciliation created compensating movements: %d -> %d", movementsBefore, movementsAfter)
	}

	// The run and its findings are persisted and readable.
	response, body = doJSON(t, http.MethodGet, api.URL+"/reconciliation/runs/"+run.ID, nil, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("get run: status %d body %s", response.StatusCode, body)
	}
	persisted := decodeRun(t, body)
	if persisted.ID != run.ID || persisted.FindingsCount == 0 || len(persisted.Findings) == 0 {
		t.Fatalf("run was not persisted with findings: %+v", persisted)
	}
}

func TestReconciliationRunReportsAllChecks(t *testing.T) {
	api, _ := newAPI(t)
	response, body := runReconciliation(t, api)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("reconciliation: status %d body %s", response.StatusCode, body)
	}
	run := decodeRun(t, body)
	if run.ChecksRun != 6 {
		t.Fatalf("expected 6 checks, got %d", run.ChecksRun)
	}
	if run.Status != "clean" && run.Status != "findings" {
		t.Fatalf("unexpected status %q", run.Status)
	}
}
