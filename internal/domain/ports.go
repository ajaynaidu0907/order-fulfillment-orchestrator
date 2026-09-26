package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// This file defines the ports (interfaces) that decouple the layers of the
// system. Concrete adapters live in their own packages:
//
//   OrderStore, JobQueue       -> internal/store   (PostgreSQL, foundation)
//   PaymentService             -> internal/payment
//   InventoryService           -> internal/inventory
//   FulfillmentService         -> internal/fulfillment
//   Cache, Locker, RateLimiter -> internal/cache    (Redis)
//
// The orchestrator and the HTTP API depend only on these interfaces, so the
// pieces can be built and tested independently.

// StateTransition captures an atomic order state change: it guards the
// current status/version, records an audit event, and enqueues any follow-up
// jobs — all within a single database transaction (transactional outbox).
type StateTransition struct {
	OrderID  uuid.UUID
	From     OrderStatus
	To       OrderStatus
	Event    string
	Detail   map[string]any
	NextJobs []Job
	// ExpectVersion, when non-nil, requires the stored order.version to match
	// before the transition is applied (optimistic concurrency).
	ExpectVersion *int
}

// OrderStore persists orders and applies state transitions atomically with the
// jobs they schedule.
type OrderStore interface {
	// Create inserts the order, its items, and an initial job in one transaction.
	Create(ctx context.Context, o *Order, firstJob *Job) error
	// Get returns an order with its items, or ErrNotFound.
	Get(ctx context.Context, id uuid.UUID) (*Order, error)
	// GetByIdempotencyKey returns an existing order for a create idempotency key,
	// or ErrNotFound.
	GetByIdempotencyKey(ctx context.Context, key string) (*Order, error)
	// List returns orders matching the filter, newest first.
	List(ctx context.Context, f OrderFilter) ([]*Order, error)
	// Transition atomically applies a StateTransition. Returns
	// ErrInvalidTransition or ErrConflict when guards fail.
	Transition(ctx context.Context, t StateTransition) error
	// Events returns the audit trail for an order, oldest first.
	Events(ctx context.Context, orderID uuid.UUID) ([]OrderEvent, error)
}

// JobQueue is the PostgreSQL-backed asynchronous work queue.
type JobQueue interface {
	// Enqueue inserts a standalone job (outside an order-creation transaction).
	Enqueue(ctx context.Context, j *Job) error
	// Claim atomically claims up to batch pending jobs whose run_at has passed,
	// marking them running with a lock lease. Uses FOR UPDATE SKIP LOCKED.
	Claim(ctx context.Context, workerID string, batch int) ([]Job, error)
	// Complete marks a claimed job completed.
	Complete(ctx context.Context, id uuid.UUID) error
	// Retry reschedules a failed job to run again at runAt, recording the cause.
	Retry(ctx context.Context, id uuid.UUID, cause string, runAt time.Time) error
	// Dead marks a job permanently failed (retries exhausted).
	Dead(ctx context.Context, id uuid.UUID, cause string) error
	// ReapStale returns running jobs whose lock lease has expired back to pending
	// and reports how many were recovered.
	ReapStale(ctx context.Context) (int, error)
}

// PaymentService authorizes, captures, and refunds payments. All methods must
// be idempotent with respect to the order/idempotency key so retried jobs are safe.
type PaymentService interface {
	Authorize(ctx context.Context, req AuthorizeRequest) (*Payment, error)
	Capture(ctx context.Context, orderID uuid.UUID) (*Payment, error)
	Refund(ctx context.Context, orderID uuid.UUID) (*Payment, error)
	Get(ctx context.Context, orderID uuid.UUID) (*Payment, error)
}

// InventoryService reserves, releases, and commits stock, and serves catalog reads.
// Reserve/Release/Commit must be idempotent per order.
type InventoryService interface {
	Reserve(ctx context.Context, orderID uuid.UUID, items []OrderItem) error
	Release(ctx context.Context, orderID uuid.UUID) error
	Commit(ctx context.Context, orderID uuid.UUID) error
	GetProduct(ctx context.Context, sku string) (*Product, error)
	ListProducts(ctx context.Context, f ProductFilter) ([]Product, error)
}

// FulfillmentService creates shipments and advances them to shipped/delivered.
// All methods must be idempotent per order.
type FulfillmentService interface {
	Prepare(ctx context.Context, orderID uuid.UUID) (*Shipment, error)
	Ship(ctx context.Context, orderID uuid.UUID) (*Shipment, error)
	ConfirmDelivery(ctx context.Context, orderID uuid.UUID) (*Shipment, error)
	Get(ctx context.Context, orderID uuid.UUID) (*Shipment, error)
}

// Cache is a simple byte-oriented cache (Redis) for read-through caching.
type Cache interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Set(ctx context.Context, key string, val []byte, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
}

// Locker provides short-lived distributed mutual exclusion (Redis).
type Locker interface {
	// Acquire tries to take the lock for key with the given TTL. When ok is true
	// the caller must invoke release to free the lock early.
	Acquire(ctx context.Context, key string, ttl time.Duration) (release func(context.Context) error, ok bool, err error)
}

// RateLimiter enforces a request budget per key over a sliding window (Redis).
type RateLimiter interface {
	// Allow reports whether a request for key is within limit over window.
	Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error)
}
