// Package reconciliation reports modelled inventory inconsistencies. It is
// strictly report-only: checks run in a read-only transaction and findings are
// persisted, never repaired. Repair would be a compensating movement, which is a
// deliberate operator action, not something reconciliation may do silently.
package reconciliation

import (
	"time"

	"github.com/google/uuid"
)

const (
	StatusClean    = "clean"
	StatusFindings = "findings"

	CheckReservedMatchesActive = "reserved_matches_active_reservations"
	CheckTerminalHasNoActive   = "terminal_orders_have_no_active_reservation"
	CheckShippedHasDecrement   = "shipped_line_has_one_decrement"
	CheckManualHasActorReason  = "manual_movement_has_actor_and_reason"
	CheckBalanceInvariants     = "balance_values_respect_invariants"
)

type Finding struct {
	ID        uuid.UUID      `json:"id"`
	RunID     uuid.UUID      `json:"run_id"`
	CheckName string         `json:"check_name"`
	SKUID     *uuid.UUID     `json:"sku_id,omitempty"`
	OrderID   *uuid.UUID     `json:"order_id,omitempty"`
	EntityRef *string        `json:"entity_ref,omitempty"`
	Expected  string         `json:"expected"`
	Observed  string         `json:"observed"`
	Details   map[string]any `json:"details,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

type Run struct {
	ID            uuid.UUID `json:"id"`
	Status        string    `json:"status"`
	ChecksRun     int       `json:"checks_run"`
	FindingsCount int       `json:"findings_count"`
	StartedAt     time.Time `json:"started_at"`
	FinishedAt    time.Time `json:"finished_at"`
	Findings      []Finding `json:"findings,omitempty"`
}

func timeNow() time.Time { return time.Now().UTC() }
