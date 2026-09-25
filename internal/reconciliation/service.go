package reconciliation

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oliaditya05/stockflow/internal/audit"
	"github.com/oliaditya05/stockflow/internal/platform/apperr"
)

type Service struct {
	Pool *pgxpool.Pool
}

// Run executes every check in a read-only transaction, then persists the run
// and its findings in a separate transaction. It never writes to inventory,
// orders, or reservations.
func (s *Service) Run(ctx context.Context, actor string) (Run, error) {
	started := timeNow()

	findings, checksRun, err := s.checkAll(ctx)
	if err != nil {
		return Run{}, err
	}
	if actor == "" {
		actor = "reconciliation"
	}
	finished := timeNow()

	run := Run{
		ID:            uuid.New(),
		Status:        StatusClean,
		ChecksRun:     checksRun,
		FindingsCount: len(findings),
		StartedAt:     started,
		FinishedAt:    finished,
		Findings:      findings,
	}
	if len(findings) > 0 {
		run.Status = StatusFindings
	}

	if err := s.persist(ctx, run, actor); err != nil {
		return Run{}, err
	}
	return run, nil
}

func (s *Service) checkAll(ctx context.Context) ([]Finding, int, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "set transaction read only"); err != nil {
		return nil, 0, err
	}

	checks := []func(context.Context, pgx.Tx) ([]Finding, error){
		checkReservedMatchesActive,
		checkBalanceMatchesLedger,
		checkTerminalHasNoActive,
		checkShippedEvidence,
		checkManualHasActorReason,
		checkBalanceInvariants,
	}

	findings := make([]Finding, 0)
	for _, check := range checks {
		found, err := check(ctx, tx)
		if err != nil {
			return nil, 0, err
		}
		findings = append(findings, found...)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, 0, err
	}
	return findings, len(checks), nil
}

func (s *Service) persist(ctx context.Context, run Run, actor string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		insert into reconciliation_runs (id, status, checks_run, findings_count, started_at, finished_at)
		values ($1, $2, $3, $4, $5, $6)`,
		run.ID, run.Status, run.ChecksRun, run.FindingsCount, run.StartedAt, run.FinishedAt); err != nil {
		return err
	}

	for i := range run.Findings {
		finding := &run.Findings[i]
		finding.RunID = run.ID
		if finding.ID == uuid.Nil {
			finding.ID = uuid.New()
		}
		if _, err := tx.Exec(ctx, `
			insert into reconciliation_findings
				(id, run_id, check_name, sku_id, order_id, entity_ref, expected, observed, details)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			finding.ID, run.ID, finding.CheckName, finding.SKUID, finding.OrderID,
			finding.EntityRef, finding.Expected, finding.Observed, finding.Details); err != nil {
			return err
		}
	}

	if err := audit.Record(ctx, tx, audit.EntryInput{
		Actor:      actor,
		Action:     audit.ActionReconciliationRun,
		EntityType: audit.EntityReconciliationRun,
		EntityID:   run.ID.String(),
		Details:    map[string]any{"status": run.Status, "findings": run.FindingsCount},
	}); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (s *Service) ListRuns(ctx context.Context, limit, offset int) ([]Run, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.Pool.Query(ctx, `
		select id, status, checks_run, findings_count, started_at, finished_at
		from reconciliation_runs
		order by started_at desc, id desc
		limit $1 offset $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	runs := make([]Run, 0)
	for rows.Next() {
		var run Run
		if err := rows.Scan(&run.ID, &run.Status, &run.ChecksRun, &run.FindingsCount, &run.StartedAt, &run.FinishedAt); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (s *Service) GetRun(ctx context.Context, id uuid.UUID) (Run, error) {
	var run Run
	err := s.Pool.QueryRow(ctx, `
		select id, status, checks_run, findings_count, started_at, finished_at
		from reconciliation_runs
		where id = $1`, id,
	).Scan(&run.ID, &run.Status, &run.ChecksRun, &run.FindingsCount, &run.StartedAt, &run.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, apperr.NotFound("reconciliation_run_not_found", "no reconciliation run with id "+id.String())
	}
	if err != nil {
		return Run{}, err
	}

	findings, err := s.findingsForRun(ctx, id)
	if err != nil {
		return Run{}, err
	}
	run.Findings = findings
	return run, nil
}

func (s *Service) findingsForRun(ctx context.Context, runID uuid.UUID) ([]Finding, error) {
	rows, err := s.Pool.Query(ctx, `
		select id, run_id, check_name, sku_id::text, order_id::text, entity_ref,
		       expected, observed, details, created_at
		from reconciliation_findings
		where run_id = $1
		order by id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	findings := make([]Finding, 0)
	for rows.Next() {
		var (
			finding   Finding
			skuText   *string
			orderText *string
		)
		if err := rows.Scan(
			&finding.ID, &finding.RunID, &finding.CheckName, &skuText, &orderText,
			&finding.EntityRef, &finding.Expected, &finding.Observed, &finding.Details, &finding.CreatedAt,
		); err != nil {
			return nil, err
		}
		if skuText != nil {
			if id, err := uuid.Parse(*skuText); err == nil {
				finding.SKUID = &id
			}
		}
		if orderText != nil {
			if id, err := uuid.Parse(*orderText); err == nil {
				finding.OrderID = &id
			}
		}
		findings = append(findings, finding)
	}
	return findings, rows.Err()
}

func checkReservedMatchesActive(ctx context.Context, tx pgx.Tx) ([]Finding, error) {
	rows, err := tx.Query(ctx, `
		select b.sku_id, s.code, b.reserved,
		       coalesce(sum(r.quantity) filter (where r.status = 'active'), 0)
		from inventory_balances b
		join skus s on s.id = b.sku_id
		left join reservations r on r.sku_id = b.sku_id and r.status = 'active'
		group by b.sku_id, s.code, b.reserved
		having b.reserved <> coalesce(sum(r.quantity) filter (where r.status = 'active'), 0)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	findings := make([]Finding, 0)
	for rows.Next() {
		var (
			skuID    uuid.UUID
			code     string
			observed int
			expected int
		)
		if err := rows.Scan(&skuID, &code, &observed, &expected); err != nil {
			return nil, err
		}
		sku, code := skuID, code
		findings = append(findings, Finding{
			CheckName: CheckReservedMatchesActive,
			SKUID:     &sku,
			EntityRef: &code,
			Expected:  fmt.Sprintf("%d", expected),
			Observed:  fmt.Sprintf("%d", observed),
			Details:   map[string]any{"code": code, "expected_reserved": expected, "observed_reserved": observed},
		})
	}
	return findings, rows.Err()
}

func checkTerminalHasNoActive(ctx context.Context, tx pgx.Tx) ([]Finding, error) {
	rows, err := tx.Query(ctx, `
		select r.id, r.order_id, o.status, r.sku_id, s.code, r.quantity
		from reservations r
		join orders o on o.id = r.order_id
		join skus s on s.id = r.sku_id
		where r.status = 'active' and o.status in ('shipped', 'cancelled')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	findings := make([]Finding, 0)
	for rows.Next() {
		var (
			reservationID uuid.UUID
			orderID       uuid.UUID
			orderStatus   string
			skuID         uuid.UUID
			code          string
			quantity      int
		)
		if err := rows.Scan(&reservationID, &orderID, &orderStatus, &skuID, &code, &quantity); err != nil {
			return nil, err
		}
		order, sku := orderID, skuID
		ref := code
		findings = append(findings, Finding{
			CheckName: CheckTerminalHasNoActive,
			SKUID:     &sku,
			OrderID:   &order,
			EntityRef: &ref,
			Expected:  "no active reservation",
			Observed:  fmt.Sprintf("active reservation %s for %d units on %s order", reservationID, quantity, orderStatus),
			Details:   map[string]any{"code": code, "reservation_id": reservationID.String(), "order_status": orderStatus},
		})
	}
	return findings, rows.Err()
}

// checkShippedEvidence requires, for every line of a shipped order, exactly one
// shipment movement whose order ID, order-item ID, SKU ID, and on-hand effect
// all match the line. It reports missing, duplicate, wrong-order, wrong-SKU, and
// wrong-quantity evidence, plus any shipment movement whose references disagree
// with the order item it names. It never repairs.
func checkShippedEvidence(ctx context.Context, tx pgx.Tx) ([]Finding, error) {
	findings := make([]Finding, 0)

	rows, err := tx.Query(ctx, `
		with shipped_items as (
			select oi.id as item_id, oi.order_id, oi.sku_id, oi.quantity, s.code
			from orders o
			join order_items oi on oi.order_id = o.id
			join skus s on s.id = oi.sku_id
			where o.status = 'shipped'
		),
		evidence as (
			select m.order_item_id as item_id,
			       count(*) as movements,
			       count(*) filter (
			           where m.order_id = si.order_id
			             and m.sku_id = si.sku_id
			             and m.delta_on_hand = -si.quantity
			       ) as matching,
			       coalesce(sum(m.delta_on_hand), 0) as total
			from inventory_movements m
			join shipped_items si on si.item_id = m.order_item_id
			where m.kind = 'shipment'
			group by m.order_item_id
		)
		select si.item_id, si.order_id, si.sku_id, si.code, si.quantity,
		       coalesce(e.movements, 0), coalesce(e.matching, 0), coalesce(e.total, 0)
		from shipped_items si
		left join evidence e on e.item_id = si.item_id
		where coalesce(e.movements, 0) <> 1
		   or coalesce(e.matching, 0) <> 1
		   or coalesce(e.total, 0) <> -si.quantity`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var (
			itemID    uuid.UUID
			orderID   uuid.UUID
			skuID     uuid.UUID
			code      string
			quantity  int
			movements int
			matching  int
			total     int
		)
		if err := rows.Scan(&itemID, &orderID, &skuID, &code, &quantity, &movements, &matching, &total); err != nil {
			rows.Close()
			return nil, err
		}
		order, sku := orderID, skuID
		ref := code
		findings = append(findings, Finding{
			CheckName: CheckShippedEvidence,
			SKUID:     &sku,
			OrderID:   &order,
			EntityRef: &ref,
			Expected:  fmt.Sprintf("exactly 1 shipment movement for order %s item %s sku %s of -%d", orderID, itemID, skuID, quantity),
			Observed:  fmt.Sprintf("%d shipment movements, %d matching the line, totalling %d", movements, matching, total),
			Details: map[string]any{
				"code":                code,
				"order_item_id":       itemID.String(),
				"quantity":            quantity,
				"shipment_movements":  movements,
				"matching_movements":  matching,
				"total_delta_on_hand": total,
			},
		})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	// A shipment movement may name an item but carry a different order or SKU,
	// or name no item at all. The identity foreign key prevents this for new
	// writes; this catches legacy or directly-injected rows.
	mismatched, err := tx.Query(ctx, `
		select m.id, m.order_id, m.order_item_id, m.sku_id, oi.order_id, oi.sku_id
		from inventory_movements m
		left join order_items oi on oi.id = m.order_item_id
		where m.kind = 'shipment'
		  and (oi.id is null
		       or m.order_id is distinct from oi.order_id
		       or m.sku_id <> oi.sku_id)`)
	if err != nil {
		return nil, err
	}
	defer mismatched.Close()
	for mismatched.Next() {
		var (
			movementID  int64
			orderID     *uuid.UUID
			itemID      *uuid.UUID
			skuID       uuid.UUID
			itemOrderID *uuid.UUID
			itemSKUID   *uuid.UUID
		)
		if err := mismatched.Scan(&movementID, &orderID, &itemID, &skuID, &itemOrderID, &itemSKUID); err != nil {
			return nil, err
		}
		sku := skuID
		expectedRef := "matching order/item/sku"
		observedRef := fmt.Sprintf("movement %d order=%s item=%s sku=%s", movementID, uuidText(orderID), uuidText(itemID), skuID)
		finding := Finding{
			CheckName: CheckShippedEvidence,
			SKUID:     &sku,
			EntityRef: &expectedRef,
			Expected:  fmt.Sprintf("shipment movement references order %s item %s sku %s", uuidText(itemOrderID), uuidText(itemID), uuidText(itemSKUID)),
			Observed:  observedRef,
			Details:   map[string]any{"movement_id": movementID},
		}
		if itemOrderID != nil {
			order := *itemOrderID
			finding.OrderID = &order
		}
		findings = append(findings, finding)
	}
	return findings, mismatched.Err()
}

func uuidText(id *uuid.UUID) string {
	if id == nil {
		return "null"
	}
	return id.String()
}

// checkBalanceMatchesLedger compares every SKU's stored balance with the sums of
// its immutable movements. It includes SKUs whose balance row is missing, and
// reports expected and observed values without repairing anything.
func checkBalanceMatchesLedger(ctx context.Context, tx pgx.Tx) ([]Finding, error) {
	rows, err := tx.Query(ctx, `
		with ledger as (
			select sku_id,
			       coalesce(sum(delta_on_hand), 0) as on_hand,
			       coalesce(sum(delta_reserved), 0) as reserved
			from inventory_movements
			group by sku_id
		)
		select s.id, s.code,
		       b.sku_id is not null as has_balance,
		       coalesce(b.on_hand, 0) as observed_on_hand,
		       coalesce(l.on_hand, 0) as expected_on_hand,
		       coalesce(b.reserved, 0) as observed_reserved,
		       coalesce(l.reserved, 0) as expected_reserved
		from skus s
		left join inventory_balances b on b.sku_id = s.id
		left join ledger l on l.sku_id = s.id
		where b.sku_id is null
		   or b.on_hand <> coalesce(l.on_hand, 0)
		   or b.reserved <> coalesce(l.reserved, 0)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	findings := make([]Finding, 0)
	for rows.Next() {
		var (
			skuID            uuid.UUID
			code             string
			hasBalance       bool
			observedOnHand   int
			expectedOnHand   int
			observedReserved int
			expectedReserved int
		)
		if err := rows.Scan(&skuID, &code, &hasBalance, &observedOnHand, &expectedOnHand, &observedReserved, &expectedReserved); err != nil {
			return nil, err
		}
		sku := skuID
		ref := code
		observed := fmt.Sprintf("on_hand=%d reserved=%d", observedOnHand, observedReserved)
		if !hasBalance {
			observed = "no balance row"
		}
		findings = append(findings, Finding{
			CheckName: CheckBalanceMatchesLedger,
			SKUID:     &sku,
			EntityRef: &ref,
			Expected:  fmt.Sprintf("on_hand=%d reserved=%d (sum of movements)", expectedOnHand, expectedReserved),
			Observed:  observed,
			Details: map[string]any{
				"code":              code,
				"balance_present":   hasBalance,
				"expected_on_hand":  expectedOnHand,
				"observed_on_hand":  observedOnHand,
				"expected_reserved": expectedReserved,
				"observed_reserved": observedReserved,
			},
		})
	}
	return findings, rows.Err()
}

func checkManualHasActorReason(ctx context.Context, tx pgx.Tx) ([]Finding, error) {
	rows, err := tx.Query(ctx, `
		select m.id, m.sku_id, s.code, m.kind, m.actor, m.reason
		from inventory_movements m
		join skus s on s.id = m.sku_id
		where m.kind in ('receipt', 'adjustment')
		  and (btrim(m.actor) = '' or m.reason is null or btrim(m.reason) = '')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	findings := make([]Finding, 0)
	for rows.Next() {
		var (
			movementID int64
			skuID      uuid.UUID
			code       string
			kind       string
			actor      string
			reason     *string
		)
		if err := rows.Scan(&movementID, &skuID, &code, &kind, &actor, &reason); err != nil {
			return nil, err
		}
		sku := skuID
		ref := fmt.Sprintf("%s/%d", code, movementID)
		reasonText := ""
		if reason != nil {
			reasonText = *reason
		}
		findings = append(findings, Finding{
			CheckName: CheckManualHasActorReason,
			SKUID:     &sku,
			EntityRef: &ref,
			Expected:  "non-blank actor and reason",
			Observed:  fmt.Sprintf("actor=%q reason=%q", actor, reasonText),
			Details:   map[string]any{"code": code, "movement_id": movementID, "kind": kind},
		})
	}
	return findings, rows.Err()
}

func checkBalanceInvariants(ctx context.Context, tx pgx.Tx) ([]Finding, error) {
	rows, err := tx.Query(ctx, `
		select b.sku_id, s.code, b.on_hand, b.reserved, b.available
		from inventory_balances b
		join skus s on s.id = b.sku_id
		where b.on_hand < 0 or b.reserved < 0 or b.available < 0
		   or b.available <> b.on_hand - b.reserved`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	findings := make([]Finding, 0)
	for rows.Next() {
		var (
			skuID     uuid.UUID
			code      string
			onHand    int
			reserved  int
			available int
		)
		if err := rows.Scan(&skuID, &code, &onHand, &reserved, &available); err != nil {
			return nil, err
		}
		sku := skuID
		ref := code
		findings = append(findings, Finding{
			CheckName: CheckBalanceInvariants,
			SKUID:     &sku,
			EntityRef: &ref,
			Expected:  "on_hand>=0, reserved>=0, available=on_hand-reserved>=0",
			Observed:  fmt.Sprintf("on_hand=%d reserved=%d available=%d", onHand, reserved, available),
			Details:   map[string]any{"code": code},
		})
	}
	return findings, rows.Err()
}
