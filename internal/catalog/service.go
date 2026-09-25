package catalog

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oliaditya05/stockflow/internal/inventory"
	"github.com/oliaditya05/stockflow/internal/platform/apperr"
)

type Service struct {
	Pool   *pgxpool.Pool
	Repo   *Repository
	Ledger *inventory.Ledger
}

func (s *Service) Create(ctx context.Context, in CreateSKUInput) (SKU, inventory.Balance, error) {
	in.Code = strings.TrimSpace(in.Code)
	in.Name = strings.TrimSpace(in.Name)
	in.Actor = strings.TrimSpace(in.Actor)
	in.OpeningReason = strings.TrimSpace(in.OpeningReason)

	if in.Code == "" {
		return SKU{}, inventory.Balance{}, apperr.BadRequest("invalid_code", "code is required")
	}
	if in.Name == "" {
		return SKU{}, inventory.Balance{}, apperr.BadRequest("invalid_name", "name is required")
	}
	if in.OpeningQuantity < 0 {
		return SKU{}, inventory.Balance{}, apperr.BadRequest("invalid_opening_quantity", "opening_quantity must be zero or positive")
	}
	if in.OpeningQuantity > 0 && in.Actor == "" {
		return SKU{}, inventory.Balance{}, apperr.BadRequest("invalid_actor", "actor is required when opening stock is provided")
	}

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return SKU{}, inventory.Balance{}, err
	}
	defer tx.Rollback(ctx)

	sku := SKU{ID: uuid.New(), Code: in.Code, Name: in.Name}
	if err := s.Repo.Insert(ctx, tx, sku); err != nil {
		if isUniqueViolation(err) {
			return SKU{}, inventory.Balance{}, apperr.Conflict("sku_code_exists", "SKU code already exists", "a sku with code "+in.Code+" already exists")
		}
		return SKU{}, inventory.Balance{}, err
	}
	if err := s.Ledger.InitializeBalance(ctx, tx, sku.ID); err != nil {
		return SKU{}, inventory.Balance{}, err
	}

	var balance inventory.Balance
	if in.OpeningQuantity > 0 {
		reason := in.OpeningReason
		if reason == "" {
			reason = "opening balance"
		}
		_, balance, err = s.Ledger.Apply(ctx, tx, inventory.MovementInput{
			SKUID:       sku.ID,
			DeltaOnHand: in.OpeningQuantity,
			Kind:        inventory.KindReceipt,
			Actor:       in.Actor,
			Reason:      &reason,
		})
		if err != nil {
			return SKU{}, inventory.Balance{}, err
		}
	} else {
		balances, lockErr := s.Ledger.LockBalances(ctx, tx, []uuid.UUID{sku.ID})
		if lockErr != nil {
			return SKU{}, inventory.Balance{}, lockErr
		}
		if len(balances) == 0 {
			return SKU{}, inventory.Balance{}, apperr.Internal("balance was not initialized")
		}
		balance = balances[0]
	}

	if err := tx.Commit(ctx); err != nil {
		return SKU{}, inventory.Balance{}, err
	}

	if readErr := s.Repo.Pool.QueryRow(ctx, `select created_at from skus where id = $1`, sku.ID).Scan(&sku.CreatedAt); readErr != nil {
		return SKU{}, inventory.Balance{}, readErr
	}
	return sku, balance, nil
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (SKU, error) {
	sku, err := s.Repo.GetByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return SKU{}, apperr.NotFound("sku_not_found", "no sku with id "+id.String())
	}
	return sku, err
}

func (s *Service) GetByCode(ctx context.Context, code string) (SKU, error) {
	sku, err := s.Repo.GetByCode(ctx, code)
	if errors.Is(err, pgx.ErrNoRows) {
		return SKU{}, apperr.NotFound("sku_not_found", "no sku with code "+code)
	}
	return sku, err
}

func (s *Service) List(ctx context.Context, limit, offset int) ([]SKU, error) {
	return s.Repo.List(ctx, limit, offset)
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
