package catalog

import (
	"time"

	"github.com/google/uuid"
)

type SKU struct {
	ID        uuid.UUID `json:"id"`
	Code      string    `json:"code"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type CreateSKUInput struct {
	Code            string
	Name            string
	Actor           string
	OpeningQuantity int
	OpeningReason   string
}
