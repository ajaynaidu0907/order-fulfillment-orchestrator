package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/domain"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/observability"
)

// handlers holds the dependencies used to service HTTP requests.
type handlers struct {
	orders      domain.OrderStore
	inventory   domain.InventoryService
	payment     domain.PaymentService
	fulfillment domain.FulfillmentService
	metrics     *observability.Metrics
	logger      *slog.Logger

	// db and redis are used only by the readiness probe.
	db    *pgxpool.Pool
	redis *redis.Client
}

// health is the liveness probe.
func (h *handlers) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ready is the readiness probe: it pings PostgreSQL and Redis.
func (h *handlers) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	checks := make(map[string]string, 2)
	ready := true

	if h.db != nil {
		if err := h.db.Ping(ctx); err != nil {
			ready = false
			checks["postgres"] = "error"
		} else {
			checks["postgres"] = "ok"
		}
	}
	if h.redis != nil {
		if err := h.redis.Ping(ctx).Err(); err != nil {
			ready = false
			checks["redis"] = "error"
		} else {
			checks["redis"] = "ok"
		}
	}

	status := http.StatusOK
	result := "ok"
	if !ready {
		status = http.StatusServiceUnavailable
		result = "unavailable"
	}
	writeJSON(w, status, map[string]any{"status": result, "checks": checks})
}

// createOrder creates a new order, honouring the Idempotency-Key header.
func (h *handlers) createOrder(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))

	// Fast path: a repeat request with a known key returns the existing order.
	if key != "" {
		existing, err := h.orders.GetByIdempotencyKey(ctx, key)
		if err == nil {
			writeJSON(w, http.StatusOK, orderToResponse(existing))
			return
		}
		if !errors.Is(err, domain.ErrNotFound) {
			writeError(w, r, h.logger, err)
			return
		}
	}

	var req createOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, h.logger, fmt.Errorf("%w: invalid JSON body", domain.ErrValidation))
		return
	}

	customerID, err := uuid.Parse(strings.TrimSpace(req.CustomerID))
	if err != nil {
		writeError(w, r, h.logger, fmt.Errorf("%w: customer_id must be a valid UUID", domain.ErrValidation))
		return
	}
	if len(req.Items) == 0 {
		writeError(w, r, h.logger, fmt.Errorf("%w: at least one item is required", domain.ErrValidation))
		return
	}
	currency := strings.ToUpper(strings.TrimSpace(req.Currency))
	if currency == "" {
		currency = "USD"
	}

	items := make([]domain.OrderItem, 0, len(req.Items))
	for _, ri := range req.Items {
		sku := strings.TrimSpace(ri.SKU)
		if sku == "" {
			writeError(w, r, h.logger, fmt.Errorf("%w: item sku is required", domain.ErrValidation))
			return
		}
		if ri.Quantity <= 0 {
			writeError(w, r, h.logger, fmt.Errorf("%w: item quantity must be greater than zero", domain.ErrValidation))
			return
		}
		prod, err := h.inventory.GetProduct(ctx, sku)
		if err != nil {
			writeError(w, r, h.logger, err)
			return
		}
		items = append(items, domain.OrderItem{
			SKU:       prod.SKU,
			Name:      prod.Name,
			Quantity:  ri.Quantity,
			UnitPrice: prod.Price,
		})
	}

	order := &domain.Order{
		ID:              uuid.New(),
		CustomerID:      customerID,
		Status:          domain.StatusCreated,
		Currency:        currency,
		Items:           items,
		ShippingAddress: req.ShippingAddress.toDomain(),
		IdempotencyKey:  key,
	}
	order.TotalAmount = order.ComputeTotal()

	firstJob, err := domain.NewOrderJob(domain.JobReserveInventory, order.ID, time.Now())
	if err != nil {
		writeError(w, r, h.logger, err)
		return
	}

	if err := h.orders.Create(ctx, order, &firstJob); err != nil {
		// A concurrent create with the same key raced us; return the winner.
		if key != "" && errors.Is(err, domain.ErrConflict) {
			if existing, gerr := h.orders.GetByIdempotencyKey(ctx, key); gerr == nil {
				writeJSON(w, http.StatusOK, orderToResponse(existing))
				return
			}
		}
		writeError(w, r, h.logger, err)
		return
	}

	if h.metrics != nil {
		h.metrics.OrdersCreated.Inc()
	}
	writeJSON(w, http.StatusCreated, orderToResponse(order))
}

// getOrder returns a single order with its items.
func (h *handlers) getOrder(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, h.logger, fmt.Errorf("%w: order id must be a valid UUID", domain.ErrValidation))
		return
	}
	order, err := h.orders.Get(r.Context(), id)
	if err != nil {
		writeError(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, orderToResponse(order))
}

// getOrderEvents returns the audit trail for an order.
func (h *handlers) getOrderEvents(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, h.logger, fmt.Errorf("%w: order id must be a valid UUID", domain.ErrValidation))
		return
	}
	events, err := h.orders.Events(r.Context(), id)
	if err != nil {
		writeError(w, r, h.logger, err)
		return
	}
	out := make([]eventResponse, 0, len(events))
	for _, e := range events {
		out = append(out, eventToResponse(e))
	}
	writeJSON(w, http.StatusOK, eventsListResponse{Events: out, Count: len(out)})
}

// cancelOrder synchronously cancels an order, running any required
// compensations (refund, release) before transitioning to CANCELLED.
func (h *handlers) cancelOrder(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, h.logger, fmt.Errorf("%w: order id must be a valid UUID", domain.ErrValidation))
		return
	}

	order, err := h.orders.Get(ctx, id)
	if err != nil {
		writeError(w, r, h.logger, err)
		return
	}

	comps, err := domain.CompensationsForCancel(order.Status)
	if err != nil {
		// Already shipped/delivered/terminal -> 409.
		writeError(w, r, h.logger, err)
		return
	}

	for _, comp := range comps {
		var cerr error
		switch comp {
		case domain.CompRefundPayment:
			_, cerr = h.payment.Refund(ctx, id)
		case domain.CompReleaseInventory:
			cerr = h.inventory.Release(ctx, id)
		}
		h.recordCompensation(comp, cerr)
		if cerr != nil {
			writeError(w, r, h.logger, cerr)
			return
		}
	}

	if err := h.orders.Transition(ctx, domain.StateTransition{
		OrderID: id,
		From:    order.Status,
		To:      domain.StatusCancelled,
		Event:   "cancelled",
	}); err != nil {
		// A worker advanced the order concurrently -> 409; the client may retry.
		writeError(w, r, h.logger, err)
		return
	}

	updated, err := h.orders.Get(ctx, id)
	if err != nil {
		writeError(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, orderToResponse(updated))
}

// listOrders returns a filtered, paginated list of orders.
func (h *handlers) listOrders(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := domain.OrderFilter{
		Limit:  parseIntDefault(q.Get("limit"), 50),
		Offset: parseIntDefault(q.Get("offset"), 0),
	}
	if raw := strings.TrimSpace(q.Get("customer_id")); raw != "" {
		cid, err := uuid.Parse(raw)
		if err != nil {
			writeError(w, r, h.logger, fmt.Errorf("%w: customer_id must be a valid UUID", domain.ErrValidation))
			return
		}
		f.CustomerID = &cid
	}
	if raw := strings.TrimSpace(q.Get("status")); raw != "" {
		st := domain.OrderStatus(strings.ToUpper(raw))
		if !st.IsValid() {
			writeError(w, r, h.logger, fmt.Errorf("%w: unknown status %q", domain.ErrValidation, raw))
			return
		}
		f.Status = &st
	}

	orders, err := h.orders.List(r.Context(), f)
	if err != nil {
		writeError(w, r, h.logger, err)
		return
	}
	out := make([]orderResponse, 0, len(orders))
	for _, o := range orders {
		out = append(out, orderToResponse(o))
	}
	writeJSON(w, http.StatusOK, ordersListResponse{
		Orders: out,
		Count:  len(out),
		Limit:  f.Limit,
		Offset: f.Offset,
	})
}

// listProducts returns a filtered, paginated catalog listing.
func (h *handlers) listProducts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := domain.ProductFilter{
		Category: strings.TrimSpace(q.Get("category")),
		Search:   strings.TrimSpace(q.Get("search")),
		Limit:    parseIntDefault(q.Get("limit"), 50),
		Offset:   parseIntDefault(q.Get("offset"), 0),
	}
	products, err := h.inventory.ListProducts(r.Context(), f)
	if err != nil {
		writeError(w, r, h.logger, err)
		return
	}
	out := make([]productResponse, 0, len(products))
	for _, p := range products {
		out = append(out, productToResponse(p))
	}
	writeJSON(w, http.StatusOK, productsListResponse{
		Products: out,
		Count:    len(out),
		Limit:    f.Limit,
		Offset:   f.Offset,
	})
}

// getProduct returns a single catalog product by SKU.
func (h *handlers) getProduct(w http.ResponseWriter, r *http.Request) {
	sku := strings.TrimSpace(chi.URLParam(r, "sku"))
	if sku == "" {
		writeError(w, r, h.logger, fmt.Errorf("%w: sku is required", domain.ErrValidation))
		return
	}
	product, err := h.inventory.GetProduct(r.Context(), sku)
	if err != nil {
		writeError(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, productToResponse(*product))
}

// recordCompensation increments the compensations metric if it is configured.
func (h *handlers) recordCompensation(action domain.CompensationAction, err error) {
	if h.metrics == nil {
		return
	}
	result := "success"
	if err != nil {
		result = "error"
	}
	h.metrics.Compensations.WithLabelValues(string(action), result).Inc()
}

// parseIntDefault parses s as an int, returning def on empty or invalid input.
func parseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
