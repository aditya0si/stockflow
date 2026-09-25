package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/oliaditya05/stockflow/internal/auth"
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
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "healthcheck":
			runHealthcheck()
			return
		case "hash-password":
			runHashPassword()
			return
		case "new-secret":
			runNewSecret()
			return
		}
	}

	logger := observability.NewLogger(env("LOG_LEVEL", "info"))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	authConfig, err := auth.LoadConfig(os.Getenv)
	if err != nil {
		logger.Error("authentication configuration", slog.Any("error", err))
		os.Exit(1)
	}
	authManager, err := auth.NewManager(authConfig)
	if err != nil {
		logger.Error("authentication manager", slog.Any("error", err))
		os.Exit(1)
	}
	if authConfig.DemoMode {
		logger.Warn("STOCKFLOW_DEMO_MODE is enabled: demo defaults are in use and Secure cookies are disabled")
	}

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
		Auth:           authManager,
		LoginLimiter:   auth.NewLoginLimiter(envInt("STOCKFLOW_LOGIN_MAX_ATTEMPTS", 10), time.Minute),
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

// runHashPassword prints a bcrypt hash for the given password (argument or
// STOCKFLOW_OPERATOR_PASSWORD) so only the hash needs to live in configuration.
func runHashPassword() {
	password := ""
	if len(os.Args) > 2 {
		password = os.Args[2]
	}
	if password == "" {
		password = os.Getenv("STOCKFLOW_OPERATOR_PASSWORD")
	}
	if password == "" {
		fmt.Fprintln(os.Stderr, "usage: stockflow-api hash-password <password>  (or set STOCKFLOW_OPERATOR_PASSWORD)")
		os.Exit(2)
	}
	hash, err := auth.GeneratePasswordHash(password)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hash-password:", err)
		os.Exit(1)
	}
	fmt.Println(hash)
}

// runNewSecret prints a random value for STOCKFLOW_SESSION_SECRET.
func runNewSecret() {
	secret, err := auth.NewRandomSecret()
	if err != nil {
		fmt.Fprintln(os.Stderr, "new-secret:", err)
		os.Exit(1)
	}
	fmt.Println(secret)
}

// runHealthcheck performs an in-process liveness probe so a distroless image
// without a shell or HTTP client can still be health-checked by Compose.
func runHealthcheck() {
	url := env("STOCKFLOW_HEALTHCHECK_URL", "http://127.0.0.1:8080/healthz")
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Get(url)
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		os.Exit(1)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthcheck: status", response.StatusCode)
		os.Exit(1)
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
