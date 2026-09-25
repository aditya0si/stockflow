package orders

import (
	"time"

	"github.com/google/uuid"
)

const (
	StatusAccepted  = "accepted"
	StatusPicking   = "picking"
	StatusPacked    = "packed"
	StatusShipped   = "shipped"
	StatusCancelled = "cancelled"

	ReservationActive   = "active"
	ReservationReleased = "released"
)

type LineRequest struct {
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
}

type CreateOrderRequest struct {
	Lines []LineRequest `json:"lines"`
}

type CreateOrderInput struct {
	Scope string
	Key   string
	Lines []LineRequest
}

type OrderItem struct {
	ID                uuid.UUID `json:"id"`
	SKUID             uuid.UUID `json:"sku_id"`
	SKU               string    `json:"sku"`
	Quantity          int       `json:"quantity"`
	ReservationStatus string    `json:"reservation_status"`
}

type Order struct {
	ID          uuid.UUID   `json:"id"`
	Status      string      `json:"status"`
	Items       []OrderItem `json:"items"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
	CancelledAt *time.Time  `json:"cancelled_at,omitempty"`
	ShippedAt   *time.Time  `json:"shipped_at,omitempty"`
}

type CancelInput struct {
	Actor  string
	Reason string
}
