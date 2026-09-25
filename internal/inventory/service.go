package inventory

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oliaditya05/stockflow/internal/platform/apperr"
)

type Service struct {
	Pool   *pgxpool.Pool
	Ledger *Ledger
}

func (s *Service) Receive(ctx context.Context, in ReceiptInput) (ReceiptResult, error) {
	in.SKUCode = strings.TrimSpace(in.SKUCode)
	in.Actor = strings.TrimSpace(in.Actor)
	in.Reason = strings.TrimSpace(in.Reason)

	if in.SKUCode == "" {
		return ReceiptResult{}, apperr.BadRequest("invalid_sku", "sku is required")
	}
	if in.Quantity <= 0 {
		return ReceiptResult{}, apperr.BadRequest("invalid_quantity", "quantity must be a positive integer")
	}
	if in.Actor == "" {
		return ReceiptResult{}, apperr.BadRequest("invalid_actor", "actor is required")
	}
	if in.Reason == "" {
		return ReceiptResult{}, apperr.BadRequest("invalid_reason", "reason is required for a receipt")
	}

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return ReceiptResult{}, err
	}
	defer tx.Rollback(ctx)

	var skuID uuid.UUID
	err = tx.QueryRow(ctx, `select id from skus where code = $1`, in.SKUCode).Scan(&skuID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReceiptResult{}, apperr.NotFound("sku_not_found", "no sku with code "+in.SKUCode)
	}
	if err != nil {
		return ReceiptResult{}, err
	}

	if _, err := s.Ledger.LockBalances(ctx, tx, []uuid.UUID{skuID}); err != nil {
		return ReceiptResult{}, err
	}

	reason := in.Reason
	movement, balance, err := s.Ledger.Apply(ctx, tx, MovementInput{
		SKUID:       skuID,
		DeltaOnHand: in.Quantity,
		Kind:        KindReceipt,
		Actor:       in.Actor,
		Reason:      &reason,
	})
	if err != nil {
		return ReceiptResult{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return ReceiptResult{}, err
	}
	return ReceiptResult{Movement: movement, Balance: balance}, nil
}

func (s *Service) Balance(ctx context.Context, skuID uuid.UUID) (Balance, error) {
	balance, err := s.Ledger.Balance(ctx, skuID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Balance{}, apperr.NotFound("balance_not_found", "no balance for sku "+skuID.String())
	}
	return balance, err
}

func (s *Service) Movements(ctx context.Context, filter MovementFilter) ([]Movement, error) {
	if filter.Limit <= 0 {
		filter.Limit = 100
	}
	if filter.Limit > 500 {
		filter.Limit = 500
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	return s.Ledger.Movements(ctx, filter)
}
