package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/oliaditya05/stockflow/internal/catalog"
	"github.com/oliaditya05/stockflow/internal/fulfilment"
	"github.com/oliaditya05/stockflow/internal/inventory"
	"github.com/oliaditya05/stockflow/internal/orders"
	"github.com/oliaditya05/stockflow/internal/platform/apperr"
	"github.com/oliaditya05/stockflow/internal/platform/httpx"
	"github.com/oliaditya05/stockflow/internal/reconciliation"
)

type Server struct {
	Catalog        *catalog.Service
	Inventory      *inventory.Service
	Orders         *orders.Service
	Fulfilment     *fulfilment.Service
	Reconciliation *reconciliation.Service
	Web            http.Handler
	Health         func(ctx context.Context) error
	Logger         *slog.Logger
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /readyz", s.handleReady)

	mux.HandleFunc("POST /skus", s.handleCreateSKU)
	mux.HandleFunc("GET /skus", s.handleListSKUs)
	mux.HandleFunc("GET /skus/{id}", s.handleGetSKU)
	mux.HandleFunc("GET /skus/{id}/balance", s.handleGetBalance)

	mux.HandleFunc("POST /inventory/receipts", s.handleReceive)
	mux.HandleFunc("GET /inventory/movements", s.handleMovements)
	mux.HandleFunc("GET /inventory/balances", s.handleListBalances)

	mux.HandleFunc("POST /orders", s.handleCreateOrder)
	mux.HandleFunc("GET /orders", s.handleListOrders)
	mux.HandleFunc("GET /orders/{id}", s.handleGetOrder)
	mux.HandleFunc("GET /orders/{id}/events", s.handleOrderEvents)
	mux.HandleFunc("POST /orders/{id}/cancel", s.handleCancelOrder)
	mux.HandleFunc("POST /orders/{id}/pick", s.handleTransition("pick"))
	mux.HandleFunc("POST /orders/{id}/pack", s.handleTransition("pack"))
	mux.HandleFunc("POST /orders/{id}/ship", s.handleTransition("ship"))

	mux.HandleFunc("POST /reconciliation/runs", s.handleRunReconciliation)
	mux.HandleFunc("GET /reconciliation/runs", s.handleListReconciliationRuns)
	mux.HandleFunc("GET /reconciliation/runs/{id}", s.handleGetReconciliationRun)

	mux.HandleFunc("/", s.handleFallback)

	return httpx.RequestID(httpx.AccessLog(s.Logger)(httpx.Recoverer(s.Logger)(mux)))
}

var apiPrefixes = []string{
	"/healthz", "/readyz", "/skus", "/inventory", "/orders", "/reconciliation",
}

func (s *Server) handleFallback(w http.ResponseWriter, r *http.Request) {
	for _, prefix := range apiPrefixes {
		if r.URL.Path == prefix || strings.HasPrefix(r.URL.Path, prefix+"/") {
			httpx.WriteProblem(w, r, apperr.NotFound("not_found", "no route for "+r.Method+" "+r.URL.Path))
			return
		}
	}
	if s.Web == nil {
		httpx.WriteProblem(w, r, apperr.NotFound("not_found", "no route for "+r.Method+" "+r.URL.Path))
		return
	}
	s.Web.ServeHTTP(w, r)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if s.Health == nil {
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ready"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.Health(ctx); err != nil {
		httpx.WriteProblem(w, r, apperr.New(http.StatusServiceUnavailable, "not_ready", "Service unavailable", "database is not reachable"))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

type createSKURequest struct {
	Code            string `json:"code"`
	Name            string `json:"name"`
	Actor           string `json:"actor"`
	OpeningQuantity int    `json:"opening_quantity"`
	OpeningReason   string `json:"opening_reason"`
}

func (s *Server) handleCreateSKU(w http.ResponseWriter, r *http.Request) {
	var request createSKURequest
	if !httpx.DecodeJSON(w, r, &request) {
		return
	}
	sku, balance, err := s.Catalog.Create(r.Context(), catalog.CreateSKUInput{
		Code:            request.Code,
		Name:            request.Name,
		Actor:           request.Actor,
		OpeningQuantity: request.OpeningQuantity,
		OpeningReason:   request.OpeningReason,
	})
	if err != nil {
		httpx.WriteError(w, r, s.Logger, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"sku": sku, "balance": balance})
}

func (s *Server) handleListSKUs(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	skus, err := s.Catalog.List(r.Context(), limit, offset)
	if err != nil {
		httpx.WriteError(w, r, s.Logger, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"skus": skus})
}

func (s *Server) handleGetSKU(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	sku, err := s.Catalog.Get(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, s.Logger, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, sku)
}

func (s *Server) handleGetBalance(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	balance, err := s.Inventory.Balance(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, s.Logger, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, balance)
}

type receiveRequest struct {
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
	Actor    string `json:"actor"`
	Reason   string `json:"reason"`
}

func (s *Server) handleReceive(w http.ResponseWriter, r *http.Request) {
	var request receiveRequest
	if !httpx.DecodeJSON(w, r, &request) {
		return
	}
	result, err := s.Inventory.Receive(r.Context(), inventory.ReceiptInput{
		SKUCode:  request.SKU,
		Quantity: request.Quantity,
		Actor:    request.Actor,
		Reason:   request.Reason,
	})
	if err != nil {
		httpx.WriteError(w, r, s.Logger, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, result)
}

func (s *Server) handleMovements(w http.ResponseWriter, r *http.Request) {
	filter := inventory.MovementFilter{Limit: 100}
	query := r.URL.Query()

	if raw := strings.TrimSpace(query.Get("sku_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			httpx.WriteProblem(w, r, apperr.BadRequest("invalid_sku_id", "sku_id must be a UUID"))
			return
		}
		filter.SKUID = &id
	}
	if raw := strings.TrimSpace(query.Get("order_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			httpx.WriteProblem(w, r, apperr.BadRequest("invalid_order_id", "order_id must be a UUID"))
			return
		}
		filter.OrderID = &id
	}
	limit, offset := pagination(r)
	filter.Limit = limit
	filter.Offset = offset

	movements, err := s.Inventory.Movements(r.Context(), filter)
	if err != nil {
		httpx.WriteError(w, r, s.Logger, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"movements": movements})
}

func (s *Server) handleListBalances(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	balances, err := s.Inventory.Balances(r.Context(), limit, offset)
	if err != nil {
		httpx.WriteError(w, r, s.Logger, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"balances": balances})
}

func (s *Server) handleCreateOrder(w http.ResponseWriter, r *http.Request) {
	var request orders.CreateOrderRequest
	if !httpx.DecodeJSON(w, r, &request) {
		return
	}
	order, err := s.Orders.CreateOrder(r.Context(), orders.CreateOrderInput{
		Scope: strings.TrimSpace(r.Header.Get("X-Caller-Scope")),
		Key:   strings.TrimSpace(r.Header.Get("Idempotency-Key")),
		Lines: request.Lines,
	})
	if err != nil {
		httpx.WriteError(w, r, s.Logger, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, order)
}

func (s *Server) handleListOrders(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	result, err := s.Orders.ListOrders(r.Context(), limit, offset)
	if err != nil {
		httpx.WriteError(w, r, s.Logger, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"orders": result})
}

func (s *Server) handleGetOrder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	order, err := s.Orders.GetOrder(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, s.Logger, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, order)
}

type cancelRequest struct {
	Actor  string `json:"actor"`
	Reason string `json:"reason"`
}

func (s *Server) handleCancelOrder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	var request cancelRequest
	if r.Body != nil && r.ContentLength != 0 {
		if !httpx.DecodeJSON(w, r, &request) {
			return
		}
	}
	order, err := s.Orders.CancelOrder(r.Context(), id, orders.CancelInput{
		Actor:  request.Actor,
		Reason: request.Reason,
	})
	if err != nil {
		httpx.WriteError(w, r, s.Logger, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, order)
}

func (s *Server) handleOrderEvents(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	events, err := s.Fulfilment.Events(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, s.Logger, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"events": events})
}

type transitionRequest struct {
	Actor  string `json:"actor"`
	Reason string `json:"reason"`
}

func (s *Server) handleTransition(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathUUID(w, r, "id")
		if !ok {
			return
		}
		var request transitionRequest
		if r.Body != nil && r.ContentLength != 0 {
			if !httpx.DecodeJSON(w, r, &request) {
				return
			}
		}
		input := fulfilment.TransitionInput{Actor: request.Actor, Reason: request.Reason}
		var (
			order orders.Order
			err   error
		)
		switch action {
		case "pick":
			order, err = s.Fulfilment.Pick(r.Context(), id, input)
		case "pack":
			order, err = s.Fulfilment.Pack(r.Context(), id, input)
		case "ship":
			order, err = s.Fulfilment.Ship(r.Context(), id, input)
		default:
			err = apperr.Internal("unknown transition action " + action)
		}
		if err != nil {
			httpx.WriteError(w, r, s.Logger, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, order)
	}
}

func (s *Server) handleRunReconciliation(w http.ResponseWriter, r *http.Request) {
	run, err := s.Reconciliation.Run(r.Context())
	if err != nil {
		httpx.WriteError(w, r, s.Logger, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, run)
}

func (s *Server) handleListReconciliationRuns(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	runs, err := s.Reconciliation.ListRuns(r.Context(), limit, offset)
	if err != nil {
		httpx.WriteError(w, r, s.Logger, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (s *Server) handleGetReconciliationRun(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	run, err := s.Reconciliation.GetRun(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, s.Logger, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, run)
}

func pathUUID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	raw := r.PathValue(name)
	id, err := uuid.Parse(raw)
	if err != nil {
		httpx.WriteProblem(w, r, apperr.BadRequest("invalid_"+name, name+" must be a UUID"))
		return uuid.Nil, false
	}
	return id, true
}

func pagination(r *http.Request) (int, int) {
	limit := 50
	offset := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 0 {
			offset = parsed
		}
	}
	if limit > 200 {
		limit = 200
	}
	return limit, offset
}
