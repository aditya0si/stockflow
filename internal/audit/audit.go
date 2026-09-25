// Package audit records actor/action history and append-only fulfilment
// evidence. It depends on nothing but pgx so any module can call it inside its
// own transaction.
package audit

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	ActionOrderCreated      = "order_created"
	ActionOrderCancelled    = "order_cancelled"
	ActionFulfilmentChange  = "fulfilment_transition"
	ActionStockReceipt      = "stock_receipt"
	ActionStockAdjustment   = "stock_adjustment"
	ActionReconciliationRun = "reconciliation_run"
	EntityOrder             = "order"
	EntitySKU               = "sku"
	EntityReconciliationRun = "reconciliation_run"
)

type EntryInput struct {
	Actor      string
	Action     string
	EntityType string
	EntityID   string
	Reason     *string
	Details    map[string]any
}

type EventInput struct {
	OrderID    uuid.UUID
	FromStatus string
	ToStatus   string
	Actor      string
	Reason     *string
}

func Record(ctx context.Context, tx pgx.Tx, in EntryInput) error {
	_, err := tx.Exec(ctx, `
		insert into audit_entries (actor, action, entity_type, entity_id, reason, details)
		values ($1, $2, $3, $4, $5, $6)`,
		in.Actor, in.Action, in.EntityType, in.EntityID, in.Reason, in.Details)
	return err
}

func RecordFulfilmentEvent(ctx context.Context, tx pgx.Tx, in EventInput) error {
	_, err := tx.Exec(ctx, `
		insert into fulfilment_events (order_id, from_status, to_status, actor, reason)
		values ($1, $2, $3, $4, $5)`,
		in.OrderID, in.FromStatus, in.ToStatus, in.Actor, in.Reason)
	return err
}
