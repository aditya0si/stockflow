package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/oliaditya05/stockflow/internal/catalog"
	"github.com/oliaditya05/stockflow/internal/fulfilment"
	"github.com/oliaditya05/stockflow/internal/httpapi"
	"github.com/oliaditya05/stockflow/internal/inventory"
	"github.com/oliaditya05/stockflow/internal/orders"
	"github.com/oliaditya05/stockflow/internal/platform/observability"
	"github.com/oliaditya05/stockflow/internal/platform/postgres"
	"github.com/oliaditya05/stockflow/internal/reconciliation"
	"github.com/oliaditya05/stockflow/internal/webui"
	"github.com/oliaditya05/stockflow/migrations"
)

func main() {
	logger := observability.NewLogger(env("LOG_LEVEL", "info"))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		logger.Error("DATABASE_URL is required")
		os.Exit(1)
	}

	pool, err := postgres.Open(ctx, postgres.Config{
		URL:             databaseURL,
		MaxConns:        int32(envInt("DB_MAX_CONNS", 10)),
		MinConns:        int32(envInt("DB_MIN_CONNS", 1)),
		MaxConnLifetime: time.Hour,
		MaxConnIdleTime: 5 * time.Minute,
		ConnectTimeout:  5 * time.Second,
	})
	if err != nil {
		logger.Error("connect to postgres", slog.Any("error", err))
		os.Exit(1)
	}
	defer pool.Close()

	if envBool("MIGRATE_ON_START", true) {
		if err := postgres.Migrate(ctx, pool, migrations.FS); err != nil {
			logger.Error("apply migrations", slog.Any("error", err))
			os.Exit(1)
		}
		logger.Info("migrations applied")
	}

	ledger := &inventory.Ledger{Pool: pool}
	catalogService := &catalog.Service{Pool: pool, Repo: &catalog.Repository{Pool: pool}, Ledger: ledger}
	inventoryService := &inventory.Service{Pool: pool, Ledger: ledger}
	ordersRepo := &orders.Repository{Pool: pool}
	ordersService := &orders.Service{Pool: pool, Ledger: ledger, Repo: ordersRepo}
	fulfilmentService := &fulfilment.Service{Pool: pool, Ledger: ledger, Repo: ordersRepo}
	reconciliationService := &reconciliation.Service{Pool: pool}

	server := &httpapi.Server{
		Catalog:        catalogService,
		Inventory:      inventoryService,
		Orders:         ordersService,
		Fulfilment:     fulfilmentService,
		Reconciliation: reconciliationService,
		Web:            webui.Handler(),
		Health:         pool.Ping,
		Logger:         logger,
	}

	httpServer := &http.Server{
		Addr:              env("HTTP_ADDR", ":8080"),
		Handler:           server.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		logger.Info("http server listening", slog.String("addr", httpServer.Addr))
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server stopped", slog.Any("error", err))
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", slog.Any("error", err))
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return value
}

func envBool(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}
