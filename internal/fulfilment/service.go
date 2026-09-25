package fulfilment

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oliaditya05/stockflow/internal/audit"
	"github.com/oliaditya05/stockflow/internal/inventory"
	"github.com/oliaditya05/stockflow/internal/orders"
	"github.com/oliaditya05/stockflow/internal/platform/apperr"
)

type Service struct {
	Pool   *pgxpool.Pool
	Ledger *inventory.Ledger
	Repo   *orders.Repository
}

type activeReservation struct {
	id          uuid.UUID
	orderID     uuid.UUID
	orderItemID uuid.UUID
	skuID       uuid.UUID
	quantity    int
}

type orderItem struct {
	id       uuid.UUID
	skuID    uuid.UUID
	quantity int
}

func (s *Service) Pick(ctx context.Context, orderID uuid.UUID, in TransitionInput) (orders.Order, error) {
	return s.transition(ctx, orderID, StatusPicking, in)
}

func (s *Service) Pack(ctx context.Context, orderID uuid.UUID, in TransitionInput) (orders.Order, error) {
	return s.transition(ctx, orderID, StatusPacked, in)
}

func (s *Service) Ship(ctx context.Context, orderID uuid.UUID, in TransitionInput) (orders.Order, error) {
	in.Actor = strings.TrimSpace(in.Actor)
	in.Reason = strings.TrimSpace(in.Reason)
	if in.Actor == "" {
		return orders.Order{}, apperr.BadRequest("invalid_actor", "actor is required to ship an order")
	}

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return orders.Order{}, err
	}
	defer tx.Rollback(ctx)

	status, err := lockOrder(ctx, tx, orderID)
	if err != nil {
		return orders.Order{}, err
	}
	if status == orders.StatusShipped {
		return s.finish(ctx, tx, orderID)
	}
	if status != orders.StatusPacked {
		return orders.Order{}, orders.IllegalTransition(status, orders.StatusShipped)
	}

	items, err := lockOrderItems(ctx, tx, orderID)
	if err != nil {
		return orders.Order{}, err
	}
	reservations, err := lockReservationsForOrder(ctx, tx, orderID)
	if err != nil {
		return orders.Order{}, err
	}
	if err := validateReservations(orderID, items, reservations); err != nil {
		return orders.Order{}, err
	}

	skuIDs := make([]uuid.UUID, 0, len(reservations))
	for _, reservation := range reservations {
		skuIDs = append(skuIDs, reservation.skuID)
	}
	if _, err := s.Ledger.LockBalances(ctx, tx, skuIDs); err != nil {
		return orders.Order{}, err
	}

	reason := in.Reason
	if reason == "" {
		reason = "order shipped"
	}
	for _, reservation := range reservations {
		if _, err := tx.Exec(ctx, `
			update reservations
			set status = 'released', released_at = now()
			where id = $1 and status = 'active'`, reservation.id); err != nil {
			return orders.Order{}, err
		}

		sourceOrderID := orderID
		sourceItemID := reservation.orderItemID
		releaseReason := reason
		if _, _, err := s.Ledger.Apply(ctx, tx, inventory.MovementInput{
			SKUID:         reservation.skuID,
			DeltaReserved: -reservation.quantity,
			Kind:          inventory.KindRelease,
			Actor:         in.Actor,
			Reason:        &releaseReason,
			OrderID:       &sourceOrderID,
			OrderItemID:   &sourceItemID,
		}); err != nil {
			return orders.Order{}, err
		}
		if _, _, err := s.Ledger.Apply(ctx, tx, inventory.MovementInput{
			SKUID:       reservation.skuID,
			DeltaOnHand: -reservation.quantity,
			Kind:        inventory.KindShipment,
			Actor:       in.Actor,
			Reason:      &reason,
			OrderID:     &sourceOrderID,
			OrderItemID: &sourceItemID,
		}); err != nil {
			return orders.Order{}, err
		}
	}

	tag, err := tx.Exec(ctx, `
		update orders
		set status = $2, shipped_at = now(), updated_at = now()
		where id = $1 and status = $3`, orderID, orders.StatusShipped, orders.StatusPacked)
	if err != nil {
		return orders.Order{}, err
	}
	if tag.RowsAffected() != 1 {
		return orders.Order{}, orders.IllegalTransition(orders.StatusPacked, orders.StatusShipped)
	}

	if err := audit.RecordFulfilmentEvent(ctx, tx, audit.EventInput{
		OrderID:    orderID,
		FromStatus: orders.StatusPacked,
		ToStatus:   orders.StatusShipped,
		Actor:      in.Actor,
		Reason:     &reason,
	}); err != nil {
		return orders.Order{}, err
	}
	if err := audit.Record(ctx, tx, audit.EntryInput{
		Actor:      in.Actor,
		Action:     audit.ActionFulfilmentChange,
		EntityType: audit.EntityOrder,
		EntityID:   orderID.String(),
		Reason:     &reason,
		Details:    map[string]any{"from_status": orders.StatusPacked, "to_status": orders.StatusShipped},
	}); err != nil {
		return orders.Order{}, err
	}

	return s.finish(ctx, tx, orderID)
}

func (s *Service) transition(ctx context.Context, orderID uuid.UUID, to string, in TransitionInput) (orders.Order, error) {
	in.Actor = strings.TrimSpace(in.Actor)
	in.Reason = strings.TrimSpace(in.Reason)
	if in.Actor == "" {
		return orders.Order{}, apperr.BadRequest("invalid_actor", "actor is required for a fulfilment transition")
	}

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return orders.Order{}, err
	}
	defer tx.Rollback(ctx)

	status, err := lockOrder(ctx, tx, orderID)
	if err != nil {
		return orders.Order{}, err
	}
	if status == to {
		return s.finish(ctx, tx, orderID)
	}
	from, ok := legalFrom[to]
	if !ok || status != from {
		return orders.Order{}, orders.IllegalTransition(status, to)
	}

	tag, err := tx.Exec(ctx, `
		update orders
		set status = $2, updated_at = now()
		where id = $1 and status = $3`, orderID, to, from)
	if err != nil {
		return orders.Order{}, err
	}
	if tag.RowsAffected() != 1 {
		return orders.Order{}, orders.IllegalTransition(from, to)
	}

	reason := in.Reason
	if err := audit.RecordFulfilmentEvent(ctx, tx, audit.EventInput{
		OrderID:    orderID,
		FromStatus: from,
		ToStatus:   to,
		Actor:      in.Actor,
		Reason:     optionalReason(reason),
	}); err != nil {
		return orders.Order{}, err
	}
	if err := audit.Record(ctx, tx, audit.EntryInput{
		Actor:      in.Actor,
		Action:     audit.ActionFulfilmentChange,
		EntityType: audit.EntityOrder,
		EntityID:   orderID.String(),
		Reason:     optionalReason(reason),
		Details:    map[string]any{"from_status": from, "to_status": to},
	}); err != nil {
		return orders.Order{}, err
	}

	return s.finish(ctx, tx, orderID)
}

func (s *Service) finish(ctx context.Context, tx pgx.Tx, orderID uuid.UUID) (orders.Order, error) {
	order, err := s.Repo.Get(ctx, tx, orderID)
	if err != nil {
		return orders.Order{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return orders.Order{}, err
	}
	return order, nil
}

func lockOrder(ctx context.Context, tx pgx.Tx, orderID uuid.UUID) (string, error) {
	var status string
	err := tx.QueryRow(ctx, `select status from orders where id = $1 for update`, orderID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", apperr.NotFound("order_not_found", "no order with id "+orderID.String())
	}
	if err != nil {
		return "", err
	}
	return status, nil
}

func lockOrderItems(ctx context.Context, tx pgx.Tx, orderID uuid.UUID) ([]orderItem, error) {
	rows, err := tx.Query(ctx, `
		select id, sku_id, quantity
		from order_items
		where order_id = $1
		order by sku_id
		for update`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]orderItem, 0)
	for rows.Next() {
		var item orderItem
		if err := rows.Scan(&item.id, &item.skuID, &item.quantity); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// lockReservationsForOrder locks every active reservation that either names
// this order or names one of its order items. Querying by item as well as order
// lets validateReservations detect a reservation whose order_id disagrees with
// the item it points at.
func lockReservationsForOrder(ctx context.Context, tx pgx.Tx, orderID uuid.UUID) ([]activeReservation, error) {
	rows, err := tx.Query(ctx, `
		select id, order_id, order_item_id, sku_id, quantity
		from reservations
		where status = 'active'
		  and (order_id = $1
		       or order_item_id in (select id from order_items where order_id = $1))
		order by order_item_id
		for update`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	reservations := make([]activeReservation, 0)
	for rows.Next() {
		var reservation activeReservation
		if err := rows.Scan(&reservation.id, &reservation.orderID, &reservation.orderItemID, &reservation.skuID, &reservation.quantity); err != nil {
			return nil, err
		}
		reservations = append(reservations, reservation)
	}
	return reservations, rows.Err()
}

// validateReservations requires exactly one active reservation per order item,
// with matching order, SKU, and quantity. A missing, duplicate, released,
// altered, cross-order, or cross-SKU reservation is a stable conflict and the
// caller must mutate nothing.
func validateReservations(orderID uuid.UUID, items []orderItem, reservations []activeReservation) error {
	byItem := make(map[uuid.UUID]activeReservation, len(reservations))
	for _, reservation := range reservations {
		if reservation.orderID != orderID {
			return reservationConflict(orderID, "reservation %s belongs to order %s, not %s", reservation.id, reservation.orderID, orderID)
		}
		if _, duplicate := byItem[reservation.orderItemID]; duplicate {
			return reservationConflict(orderID, "order item %s has more than one active reservation", reservation.orderItemID)
		}
		byItem[reservation.orderItemID] = reservation
	}

	if len(byItem) != len(items) {
		return reservationConflict(orderID, "expected one active reservation per order item, got %d reservations for %d items", len(byItem), len(items))
	}
	for _, item := range items {
		reservation, ok := byItem[item.id]
		if !ok {
			return reservationConflict(orderID, "order item %s has no active reservation", item.id)
		}
		if reservation.skuID != item.skuID {
			return reservationConflict(orderID, "reservation %s has sku %s but order item %s has sku %s", reservation.id, reservation.skuID, item.id, item.skuID)
		}
		if reservation.quantity != item.quantity {
			return reservationConflict(orderID, "reservation %s reserves %d but order item %s requires %d", reservation.id, reservation.quantity, item.id, item.quantity)
		}
	}
	return nil
}

func reservationConflict(orderID uuid.UUID, format string, args ...any) *apperr.Error {
	return apperr.
		Conflict("reservation_inconsistent", "Order reservations are inconsistent",
			fmt.Sprintf(format, args...)).
		With("order_id", orderID.String())
}

func (s *Service) Events(ctx context.Context, orderID uuid.UUID) ([]Event, error) {
	if _, err := s.Repo.Get(ctx, s.Pool, orderID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.NotFound("order_not_found", "no order with id "+orderID.String())
		}
		return nil, err
	}

	rows, err := s.Pool.Query(ctx, `
		select id, order_id, from_status, to_status, actor, reason, created_at
		from fulfilment_events
		where order_id = $1
		order by id`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]Event, 0)
	for rows.Next() {
		var event Event
		if err := rows.Scan(&event.ID, &event.OrderID, &event.FromStatus, &event.ToStatus, &event.Actor, &event.Reason, &event.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func optionalReason(reason string) *string {
	if reason == "" {
		return nil
	}
	return &reason
}
