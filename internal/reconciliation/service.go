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
func (s *Service) Run(ctx context.Context) (Run, error) {
	started := timeNow()

	findings, checksRun, err := s.checkAll(ctx)
	if err != nil {
		return Run{}, err
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

	if err := s.persist(ctx, run); err != nil {
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
		checkTerminalHasNoActive,
		checkShippedHasDecrement,
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

func (s *Service) persist(ctx context.Context, run Run) error {
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
		Actor:      "reconciliation",
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

func checkShippedHasDecrement(ctx context.Context, tx pgx.Tx) ([]Finding, error) {
	rows, err := tx.Query(ctx, `
		select oi.order_id, oi.id, oi.sku_id, s.code, oi.quantity,
		       count(m.id), coalesce(sum(m.delta_on_hand), 0)
		from orders o
		join order_items oi on oi.order_id = o.id
		join skus s on s.id = oi.sku_id
		left join inventory_movements m on m.order_item_id = oi.id and m.kind = 'shipment'
		where o.status = 'shipped'
		group by oi.order_id, oi.id, oi.sku_id, s.code, oi.quantity
		having count(m.id) <> 1 or coalesce(sum(m.delta_on_hand), 0) <> -oi.quantity`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	findings := make([]Finding, 0)
	for rows.Next() {
		var (
			orderID   uuid.UUID
			itemID    uuid.UUID
			skuID     uuid.UUID
			code      string
			quantity  int
			movements int
			total     int
		)
		if err := rows.Scan(&orderID, &itemID, &skuID, &code, &quantity, &movements, &total); err != nil {
			return nil, err
		}
		order, sku := orderID, skuID
		ref := code
		findings = append(findings, Finding{
			CheckName: CheckShippedHasDecrement,
			SKUID:     &sku,
			OrderID:   &order,
			EntityRef: &ref,
			Expected:  fmt.Sprintf("1 shipment movement totalling -%d", quantity),
			Observed:  fmt.Sprintf("%d shipment movements totalling %d", movements, total),
			Details:   map[string]any{"code": code, "order_item_id": itemID.String(), "quantity": quantity},
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
