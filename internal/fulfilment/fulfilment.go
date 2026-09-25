// Package fulfilment owns the pick/pack/ship state machine. Every transition is
// a conditional update guarded by the order's current status, so a repeated or
// racing command has exactly one effect.
package fulfilment

import (
	"time"

	"github.com/google/uuid"
)

const (
	StatusPicking = "picking"
	StatusPacked  = "packed"
	StatusShipped = "shipped"

	EventPicking   = "picking"
	EventPacked    = "packed"
	EventShipped   = "shipped"
	EventCancelled = "cancelled"
)

// legalFrom maps a target status to the only status it may be reached from.
var legalFrom = map[string]string{
	StatusPicking: "accepted",
	StatusPacked:  "picking",
	StatusShipped: "packed",
}

// IsLegalTransition reports whether a move from one status to another is part
// of the modelled state machine.
func IsLegalTransition(from, to string) bool {
	required, ok := legalFrom[to]
	return ok && from == required
}

type TransitionInput struct {
	Actor  string
	Reason string
}

type Event struct {
	ID         int64     `json:"id"`
	OrderID    uuid.UUID `json:"order_id"`
	FromStatus string    `json:"from_status"`
	ToStatus   string    `json:"to_status"`
	Actor      string    `json:"actor"`
	Reason     *string   `json:"reason,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}
