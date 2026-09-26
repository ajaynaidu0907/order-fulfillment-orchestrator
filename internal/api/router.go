// Package api implements the HTTP transport layer for the Order Fulfillment
// Orchestrator: routing, middleware, request/response DTOs, and the synchronous
// cancellation flow. It depends only on the domain ports, so it can be built and
// tested in isolation from the concrete Postgres/Redis adapters.
package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/domain"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/observability"
)

// Rate-limit budget applied to the /v1 API surface, per client IP.
const (
	rateLimitRequests = 120
	rateLimitWindow   = time.Minute
)

// Deps are the dependencies required to build the API router. The first seven
// fields are the contract in docs/ARCHITECTURE.md; DB and Redis are optional and
// consulted only by the readiness probe.
type Deps struct {
	Orders      domain.OrderStore
	Inventory   domain.InventoryService
	Payment     domain.PaymentService
	Fulfillment domain.FulfillmentService
	RateLimiter domain.RateLimiter
	Metrics     *observability.Metrics
	Logger      *slog.Logger

	// DB and Redis are used only by GET /readyz.
	DB    *pgxpool.Pool
	Redis *redis.Client
}

// NewRouter builds the fully wired HTTP handler for the API.
func NewRouter(d Deps) http.Handler {
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}

	h := &handlers{
		orders:      d.Orders,
		inventory:   d.Inventory,
		payment:     d.Payment,
		fulfillment: d.Fulfillment,
		metrics:     d.Metrics,
		logger:      logger,
		db:          d.DB,
		redis:       d.Redis,
	}

	r := chi.NewRouter()

	// Global middleware chain (outermost first). Recoverer is innermost so it
	// turns a handler panic into a normal 500 that the logging and metrics
	// middleware still observe.
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(requestLogger(logger))
	r.Use(otelhttp.NewMiddleware("ofo-api"))
	r.Use(prometheusTiming(d.Metrics))
	r.Use(recoverer(logger))

	// Operational probes (no rate limiting).
	r.Get("/healthz", h.health)
	r.Get("/readyz", h.ready)

	// Versioned API surface, rate limited per client IP.
	r.Route("/v1", func(r chi.Router) {
		r.Use(rateLimit(d.RateLimiter, logger, rateLimitRequests, rateLimitWindow))

		r.Post("/orders", h.createOrder)
		r.Get("/orders", h.listOrders)
		r.Get("/orders/{id}", h.getOrder)
		r.Get("/orders/{id}/events", h.getOrderEvents)
		r.Post("/orders/{id}/cancel", h.cancelOrder)

		r.Get("/products", h.listProducts)
		r.Get("/products/{sku}", h.getProduct)
	})

	return r
}
