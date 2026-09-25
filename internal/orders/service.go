package orders

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oliaditya05/stockflow/internal/audit"
	"github.com/oliaditya05/stockflow/internal/catalog"
	"github.com/oliaditya05/stockflow/internal/inventory"
	"github.com/oliaditya05/stockflow/internal/platform/apperr"
	"github.com/oliaditya05/stockflow/internal/platform/postgres"
)

const (
	idempotencyStateInProgress = "in_progress"
	idempotencyStateCompleted  = "completed"

	createResponseStatus = 201
	maxCreateAttempts    = 3
)

type Service struct {
	Pool   *pgxpool.Pool
	Ledger *inventory.Ledger
	Repo   *Repository
}

type idempotencyRecord struct {
	ID             uuid.UUID
	RequestHash    string
	State          string
	ResponseStatus *int
	ResponseBody   []byte
}

func (s *Service) CreateOrder(ctx context.Context, in CreateOrderInput) (Order, error) {
	in.Scope = strings.TrimSpace(in.Scope)
	in.Key = strings.TrimSpace(in.Key)
	if in.Scope == "" {
		return Order{}, apperr.BadRequest("missing_caller_scope", "the X-Caller-Scope header is required")
	}
	if in.Key == "" {
		return Order{}, apperr.BadRequest("missing_idempotency_key", "the Idempotency-Key header is required")
	}

	lines, err := Canonicalize(in.Lines)
	if err != nil {
		return Order{}, err
	}
	hash := RequestHash(lines)

	var lastErr error
	for attempt := 0; attempt < maxCreateAttempts; attempt++ {
		order, err := s.createOnce(ctx, in, lines, hash)
		if err == nil {
			return order, nil
		}
		if !isRetryableDB(err) {
			return Order{}, err
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return Order{}, ctx.Err()
		case <-time.After(retryDelay(attempt)):
		}
	}
	return Order{}, lastErr
}

func (s *Service) createOnce(ctx context.Context, in CreateOrderInput, lines []normalizedLine, hash string) (Order, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Order{}, err
	}
	defer tx.Rollback(ctx)

	record, replayed, err := s.acquireIdempotency(ctx, tx, in.Scope, in.Key, hash)
	if err != nil {
		return Order{}, err
	}
	if replayed {
		var order Order
		if err := json.Unmarshal(record.ResponseBody, &order); err != nil {
			return Order{}, fmt.Errorf("decode stored idempotent response: %w", err)
		}
		return order, nil
	}

	skus, err := s.resolveSKUs(ctx, tx, lines)
	if err != nil {
		return Order{}, err
	}

	type resolvedLine struct {
		code     string
		skuID    uuid.UUID
		quantity int
	}
	resolved := make([]resolvedLine, 0, len(lines))
	skuIDs := make([]uuid.UUID, 0, len(lines))
	for _, line := range lines {
		sku := skus[line.SKU]
		resolved = append(resolved, resolvedLine{code: line.SKU, skuID: sku.ID, quantity: line.Quantity})
		skuIDs = append(skuIDs, sku.ID)
	}

	balances, err := s.Ledger.LockBalances(ctx, tx, skuIDs)
	if err != nil {
		return Order{}, err
	}
	balanceBySKU := make(map[uuid.UUID]inventory.Balance, len(balances))
	for _, balance := range balances {
		balanceBySKU[balance.SKUID] = balance
	}

	shortages := make([]map[string]any, 0)
	for _, line := range resolved {
		balance, ok := balanceBySKU[line.skuID]
		available := 0
		if ok {
			available = balance.Available
		}
		if !ok || available < line.quantity {
			shortages = append(shortages, map[string]any{
				"sku":       line.code,
				"requested": line.quantity,
				"available": available,
			})
		}
	}
	if len(shortages) > 0 {
		return Order{}, apperr.
			Conflict("insufficient_stock", "Insufficient stock", "one or more lines cannot be reserved").
			With("shortages", shortages)
	}

	orderID := uuid.New()
	now := time.Now().UTC()
	if _, err := tx.Exec(ctx, `
		insert into orders (id, status, idempotency_record_id, created_at, updated_at)
		values ($1, $2, $3, $4, $4)`, orderID, StatusAccepted, record.ID, now); err != nil {
		return Order{}, err
	}

	items := make([]OrderItem, 0, len(resolved))
	for _, line := range resolved {
		itemID := uuid.New()
		if _, err := tx.Exec(ctx, `
			insert into order_items (id, order_id, sku_id, quantity)
			values ($1, $2, $3, $4)`, itemID, orderID, line.skuID, line.quantity); err != nil {
			return Order{}, err
		}
		if _, err := tx.Exec(ctx, `
			insert into reservations (id, order_id, order_item_id, sku_id, quantity, status)
			values ($1, $2, $3, $4, $5, 'active')`, uuid.New(), orderID, itemID, line.skuID, line.quantity); err != nil {
			return Order{}, err
		}

		sourceOrderID := orderID
		sourceItemID := itemID
		if _, _, err := s.Ledger.Apply(ctx, tx, inventory.MovementInput{
			SKUID:         line.skuID,
			DeltaReserved: line.quantity,
			Kind:          inventory.KindReservation,
			Actor:         in.Scope,
			OrderID:       &sourceOrderID,
			OrderItemID:   &sourceItemID,
		}); err != nil {
			return Order{}, err
		}

		items = append(items, OrderItem{
			ID:                itemID,
			SKUID:             line.skuID,
			SKU:               line.code,
			Quantity:          line.quantity,
			ReservationStatus: ReservationActive,
		})
	}

	order := Order{
		ID:        orderID,
		Status:    StatusAccepted,
		Items:     items,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := audit.Record(ctx, tx, audit.EntryInput{
		Actor:      in.Scope,
		Action:     audit.ActionOrderCreated,
		EntityType: audit.EntityOrder,
		EntityID:   orderID.String(),
		Details:    map[string]any{"lines": len(items)},
	}); err != nil {
		return Order{}, err
	}
	body, err := json.Marshal(order)
	if err != nil {
		return Order{}, err
	}
	if _, err := tx.Exec(ctx, `
		update idempotency_records
		set state = $2, response_status = $3, response_body = $4, order_id = $5, completed_at = now()
		where id = $1`, record.ID, idempotencyStateCompleted, createResponseStatus, body, orderID); err != nil {
		return Order{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Order{}, err
	}
	return order, nil
}

func (s *Service) acquireIdempotency(ctx context.Context, tx pgx.Tx, scope, key, hash string) (idempotencyRecord, bool, error) {
	var record idempotencyRecord
	err := tx.QueryRow(ctx, `
		insert into idempotency_records (id, scope, key, request_hash, state)
		values ($1, $2, $3, $4, $5)
		on conflict (scope, key) do nothing
		returning id, request_hash, state`,
		uuid.New(), scope, key, hash, idempotencyStateInProgress,
	).Scan(&record.ID, &record.RequestHash, &record.State)
	if err == nil {
		return record, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return record, false, err
	}

	err = tx.QueryRow(ctx, `
		select id, request_hash, state, response_status, response_body
		from idempotency_records
		where scope = $1 and key = $2
		for update`, scope, key,
	).Scan(&record.ID, &record.RequestHash, &record.State, &record.ResponseStatus, &record.ResponseBody)
	if err != nil {
		return record, false, err
	}

	if record.RequestHash != hash {
		return record, false, apperr.
			Conflict("idempotency_key_reuse", "Idempotency key reused with a different payload",
				"this idempotency key is already bound to a different request body").
			With("key", key)
	}
	if record.State == idempotencyStateCompleted {
		return record, true, nil
	}
	return record, false, apperr.Conflict("idempotency_in_progress", "Request already in progress",
		"another request with the same idempotency key is still in progress")
}

func (s *Service) resolveSKUs(ctx context.Context, tx pgx.Tx, lines []normalizedLine) (map[string]catalog.SKU, error) {
	codes := make([]string, 0, len(lines))
	for _, line := range lines {
		codes = append(codes, line.SKU)
	}

	rows, err := tx.Query(ctx, `
		select id, code, name, created_at
		from skus
		where code = any($1)`, codes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	found := make(map[string]catalog.SKU, len(codes))
	for rows.Next() {
		var sku catalog.SKU
		if err := rows.Scan(&sku.ID, &sku.Code, &sku.Name, &sku.CreatedAt); err != nil {
			return nil, err
		}
		found[sku.Code] = sku
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	missing := make([]string, 0)
	for _, code := range codes {
		if _, ok := found[code]; !ok {
			missing = append(missing, code)
		}
	}
	if len(missing) > 0 {
		return nil, apperr.
			Unprocessable("unknown_sku", "unknown sku codes: "+strings.Join(missing, ", ")).
			With("skus", missing)
	}
	return found, nil
}

func (s *Service) CancelOrder(ctx context.Context, orderID uuid.UUID, in CancelInput) (Order, error) {
	in.Actor = strings.TrimSpace(in.Actor)
	in.Reason = strings.TrimSpace(in.Reason)
	if in.Actor == "" {
		return Order{}, apperr.BadRequest("invalid_actor", "actor is required to cancel an order")
	}

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Order{}, err
	}
	defer tx.Rollback(ctx)

	var status string
	var cancelledAt *time.Time
	err = tx.QueryRow(ctx, `
		select status, cancelled_at
		from orders
		where id = $1
		for update`, orderID).Scan(&status, &cancelledAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, apperr.NotFound("order_not_found", "no order with id "+orderID.String())
	}
	if err != nil {
		return Order{}, err
	}

	if status == StatusCancelled {
		order, err := s.Repo.Get(ctx, tx, orderID)
		if err != nil {
			return Order{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return Order{}, err
		}
		return order, nil
	}
	if status == StatusShipped {
		return Order{}, illegalTransition(status, StatusCancelled)
	}

	reservations, err := lockActiveReservations(ctx, tx, orderID)
	if err != nil {
		return Order{}, err
	}
	if len(reservations) > 0 {
		skuIDs := make([]uuid.UUID, 0, len(reservations))
		for _, reservation := range reservations {
			skuIDs = append(skuIDs, reservation.skuID)
		}
		if _, err := s.Ledger.LockBalances(ctx, tx, skuIDs); err != nil {
			return Order{}, err
		}

		reason := in.Reason
		if reason == "" {
			reason = "order cancelled"
		}
		for _, reservation := range reservations {
			if _, err := tx.Exec(ctx, `
				update reservations
				set status = 'released', released_at = now()
				where id = $1 and status = 'active'`, reservation.id); err != nil {
				return Order{}, err
			}

			sourceOrderID := orderID
			sourceItemID := reservation.orderItemID
			if _, _, err := s.Ledger.Apply(ctx, tx, inventory.MovementInput{
				SKUID:         reservation.skuID,
				DeltaReserved: -reservation.quantity,
				Kind:          inventory.KindRelease,
				Actor:         in.Actor,
				Reason:        &reason,
				OrderID:       &sourceOrderID,
				OrderItemID:   &sourceItemID,
			}); err != nil {
				return Order{}, err
			}
		}
	}

	if _, err := tx.Exec(ctx, `
		update orders
		set status = $2, cancelled_at = now(), updated_at = now()
		where id = $1`, orderID, StatusCancelled); err != nil {
		return Order{}, err
	}

	eventReason := in.Reason
	if eventReason == "" {
		eventReason = "order cancelled"
	}
	if err := audit.RecordFulfilmentEvent(ctx, tx, audit.EventInput{
		OrderID:    orderID,
		FromStatus: status,
		ToStatus:   StatusCancelled,
		Actor:      in.Actor,
		Reason:     &eventReason,
	}); err != nil {
		return Order{}, err
	}
	if err := audit.Record(ctx, tx, audit.EntryInput{
		Actor:      in.Actor,
		Action:     audit.ActionOrderCancelled,
		EntityType: audit.EntityOrder,
		EntityID:   orderID.String(),
		Reason:     &eventReason,
		Details:    map[string]any{"from_status": status},
	}); err != nil {
		return Order{}, err
	}

	order, err := s.Repo.Get(ctx, tx, orderID)
	if err != nil {
		return Order{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Order{}, err
	}
	return order, nil
}

type activeReservation struct {
	id          uuid.UUID
	orderItemID uuid.UUID
	skuID       uuid.UUID
	quantity    int
}

func lockActiveReservations(ctx context.Context, tx pgx.Tx, orderID uuid.UUID) ([]activeReservation, error) {
	rows, err := tx.Query(ctx, `
		select id, order_item_id, sku_id, quantity
		from reservations
		where order_id = $1 and status = 'active'
		order by sku_id
		for update`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	reservations := make([]activeReservation, 0)
	for rows.Next() {
		var reservation activeReservation
		if err := rows.Scan(&reservation.id, &reservation.orderItemID, &reservation.skuID, &reservation.quantity); err != nil {
			return nil, err
		}
		reservations = append(reservations, reservation)
	}
	return reservations, rows.Err()
}

func (s *Service) GetOrder(ctx context.Context, orderID uuid.UUID) (Order, error) {
	order, err := s.Repo.Get(ctx, s.Pool, orderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, apperr.NotFound("order_not_found", "no order with id "+orderID.String())
	}
	return order, err
}

func (s *Service) ListOrders(ctx context.Context, limit, offset int) ([]Order, error) {
	return s.Repo.List(ctx, limit, offset)
}

// IllegalTransition returns the stable problem used when a requested
// fulfilment transition is not legal from the order's current status.
func IllegalTransition(from, to string) *apperr.Error {
	return apperr.
		Conflict("illegal_transition", "Illegal order transition",
			"cannot move order from "+from+" to "+to).
		With("from_status", from).
		With("to_status", to)
}

func illegalTransition(from, to string) *apperr.Error {
	return IllegalTransition(from, to)
}

func isRetryableDB(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "40P01" || pgErr.Code == "40001"
	}
	return false
}

func retryDelay(attempt int) time.Duration {
	base := time.Duration(1<<attempt) * 10 * time.Millisecond
	return base + time.Duration(rand.Int63n(int64(base)+1))
}

var _ postgres.DBTX = (*pgxpool.Pool)(nil)
