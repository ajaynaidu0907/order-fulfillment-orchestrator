# Architecture & Integration Contract

This document is the contract the independently-built packages must honour so they wire together
cleanly. The **foundation** (`domain`, `config`, `platform`, `store`, `observability`, `app`) is
already implemented and stable. Everything below builds on it.

## Ports (already defined in `internal/domain/ports.go`)

- `OrderStore` — implemented by `store.OrderStore` (foundation).
- `JobQueue` — implemented by `store.JobQueue` (foundation).
- `PaymentService`, `InventoryService`, `FulfillmentService` — to be implemented.
- `Cache`, `Locker`, `RateLimiter` — to be implemented (Redis).

## Constructor contract (what each package must expose)

Concrete adapters accept infrastructure handles and return a type that satisfies the matching
domain interface, so `cmd/*` can wire them without extra glue.

```go
// internal/cache  (Redis) — one struct may satisfy all three, or split them.
func NewCache(rdb *redis.Client) *Cache                 // domain.Cache
func NewLocker(rdb *redis.Client) *Locker               // domain.Locker
func NewRateLimiter(rdb *redis.Client) *RateLimiter     // domain.RateLimiter

// internal/payment
func New(pool *pgxpool.Pool, logger *slog.Logger, declineRate float64) *Service // domain.PaymentService

// internal/inventory
func New(pool *pgxpool.Pool, cache domain.Cache, logger *slog.Logger) *Service   // domain.InventoryService

// internal/fulfillment
func New(pool *pgxpool.Pool, logger *slog.Logger, failureRate float64) *Service  // domain.FulfillmentService

// internal/orchestrator — the saga engine (pure logic, no polling)
type Deps struct {
    Orders      domain.OrderStore
    Jobs        domain.JobQueue
    Payment     domain.PaymentService
    Inventory   domain.InventoryService
    Fulfillment domain.FulfillmentService
    Metrics     *observability.Metrics
    Logger      *slog.Logger
}
func New(d Deps) *Orchestrator
// Handle executes exactly one job (a forward step or a compensation action) and
// is idempotent. For a forward step it performs the operation then calls
// OrderStore.Transition to advance state AND enqueue the next job in one tx.
func (o *Orchestrator) Handle(ctx context.Context, j domain.Job) error
// Compensate is called by the worker when a job has exhausted its retries. It
// looks up the order's current status, derives the needed compensations via
// domain.CompensationsForCancel(status), and calls OrderStore.Transition to
// move the order to FAILED while enqueueing release_inventory / refund_payment
// jobs in the same tx.
func (o *Orchestrator) Compensate(ctx context.Context, j domain.Job, cause string) error

// internal/worker — the polling loop that claims jobs and drives the processor.
// Processor is satisfied by *orchestrator.Orchestrator.
type Processor interface {
    Handle(ctx context.Context, j domain.Job) error
    Compensate(ctx context.Context, j domain.Job, cause string) error
}
type Deps struct {
    Jobs      domain.JobQueue
    Processor Processor
    Cfg       config.WorkerConfig
    Metrics   *observability.Metrics
    Logger    *slog.Logger
}
func New(d Deps) *Worker
func (w *Worker) Run(ctx context.Context) error   // blocks until ctx cancelled

// internal/api — HTTP layer
type Deps struct {
    Orders      domain.OrderStore
    Inventory   domain.InventoryService
    Payment     domain.PaymentService
    Fulfillment domain.FulfillmentService
    RateLimiter domain.RateLimiter
    Metrics     *observability.Metrics
    Logger      *slog.Logger
}
func NewRouter(d Deps) http.Handler
```

## Job → operation mapping (orchestrator dispatch)

| Job type                | Action                                   | On success: next job          |
| ----------------------- | ---------------------------------------- | ----------------------------- |
| `reserve_inventory`     | `Inventory.Reserve`                      | transition→INVENTORY_RESERVED, enqueue `authorize_payment` |
| `authorize_payment`     | `Payment.Authorize`                      | transition→PAYMENT_AUTHORIZED, enqueue `prepare_fulfillment` |
| `prepare_fulfillment`   | `Fulfillment.Prepare`                    | transition→READY_FOR_FULFILLMENT, enqueue `ship_order` |
| `ship_order`            | `Fulfillment.Ship` + `Inventory.Commit`  | transition→SHIPPED, enqueue `confirm_delivery` (delayed) |
| `confirm_delivery`      | `Fulfillment.ConfirmDelivery`            | transition→DELIVERED (terminal) |
| `release_inventory`     | `Inventory.Release` (compensation)       | — |
| `refund_payment`        | `Payment.Refund` (compensation)          | — |

The state change **and** the follow-up job enqueue happen inside one `OrderStore.Transition`
call (transactional outbox), so they commit together.

## Failure & compensation rules

- On a job error: if `attempts < max_attempts`, `JobQueue.Retry` with exponential backoff
  (`base=2s`, capped at 5m): `run_at = now + min(base * 2^(attempts-1), 5m)`.
- When retries are exhausted: `JobQueue.Dead`, then enqueue compensations per
  `domain.CompensationsForCancel` semantics for the order's current state, and finally
  transition the order to `FAILED`.
- Compensation jobs (`release_inventory`, `refund_payment`) are themselves retried and must be
  idempotent.

## Idempotency

- Every operation is keyed by `order_id` (+ operation), so a re-run of a claimed-but-not-completed
  job (recovered by `ReapStale`) is safe.
- `OrderStore.Transition` treats an already-applied transition as a no-op success.
- The `jobs` table has a partial unique index on `idempotency_key` for `pending|running` rows, so
  duplicate active jobs cannot be enqueued.

## HTTP API (to implement in `internal/api`)

| Method | Path                     | Purpose                         |
| ------ | ------------------------ | ------------------------------- |
| GET    | `/healthz`               | liveness                        |
| GET    | `/readyz`                | readiness (db + redis ping)     |
| POST   | `/v1/orders`             | create order (Idempotency-Key)  |
| GET    | `/v1/orders/{id}`        | get order + items               |
| GET    | `/v1/orders/{id}/events` | order audit trail               |
| POST   | `/v1/orders/{id}/cancel` | request cancellation            |
| GET    | `/v1/orders`             | list orders (filters)           |
| GET    | `/v1/products`           | catalog listing                 |

Middleware: request ID, structured request logging, panic recovery, OTel (`otelhttp`),
Prometheus timing, and Redis rate limiting per client.

### Cancellation (synchronous, in the API)

`POST /v1/orders/{id}/cancel` is handled synchronously by the API using the injected services:

1. Load the order; compute `comps, err := domain.CompensationsForCancel(order.Status)`.
2. If `err != nil` (already shipped/delivered/terminal) → `409 Conflict`.
3. Run each compensation directly and idempotently: `CompRefundPayment` → `Payment.Refund`,
   `CompReleaseInventory` → `Inventory.Release`.
4. `Orders.Transition(from=order.Status, to=CANCELLED, event="cancelled")`. The from-state guard
   returns `409` if a worker advanced the order concurrently (the client may retry).

This keeps cancellation simple and needs no new job type. Known simplification: it does not set a
"cancel requested" flag to pre-empt an in-flight forward step; the from-state guard is the safety net.
