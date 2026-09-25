package inventory

import (
	"time"

	"github.com/google/uuid"
)

const (
	KindReceipt     = "receipt"
	KindAdjustment  = "adjustment"
	KindShipment    = "shipment"
	KindReservation = "reservation"
	KindRelease     = "release"
)

type Balance struct {
	SKUID     uuid.UUID `json:"sku_id"`
	OnHand    int       `json:"on_hand"`
	Reserved  int       `json:"reserved"`
	Available int       `json:"available"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Movement struct {
	ID            int64      `json:"id"`
	SKUID         uuid.UUID  `json:"sku_id"`
	DeltaOnHand   int        `json:"delta_on_hand"`
	DeltaReserved int        `json:"delta_reserved"`
	Kind          string     `json:"kind"`
	Actor         string     `json:"actor"`
	Reason        *string    `json:"reason,omitempty"`
	OrderID       *uuid.UUID `json:"order_id,omitempty"`
	OrderItemID   *uuid.UUID `json:"order_item_id,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

type MovementInput struct {
	SKUID         uuid.UUID
	DeltaOnHand   int
	DeltaReserved int
	Kind          string
	Actor         string
	Reason        *string
	OrderID       *uuid.UUID
	OrderItemID   *uuid.UUID
}

type MovementFilter struct {
	SKUID   *uuid.UUID
	OrderID *uuid.UUID
	Limit   int
	Offset  int
}

type ReceiptInput struct {
	SKUCode  string
	Quantity int
	Actor    string
	Reason   string
}

type ReceiptResult struct {
	Movement Movement `json:"movement"`
	Balance  Balance  `json:"balance"`
}
