// Package fulfillment implements domain.FulfillmentService against PostgreSQL. It
// is a mock carrier integration: Ship fails transiently at a configured rate to
// exercise the retry-then-compensation path. All methods are idempotent per order.
package fulfillment

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/domain"
)

var carriers = []string{"UPS", "FedEx", "DHL", "USPS"}

// Service is the PostgreSQL-backed fulfillment adapter.
type Service struct {
	pool        *pgxpool.Pool
	logger      *slog.Logger
	failureRate float64
}

// New builds a fulfillment Service. failureRate is the probability (0..1) that a
// Ship call fails transiently without mutating state.
func New(pool *pgxpool.Pool, logger *slog.Logger, failureRate float64) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{pool: pool, logger: logger, failureRate: failureRate}
}

var _ domain.FulfillmentService = (*Service)(nil)

// Prepare creates a pending shipment for the order if none exists (ON CONFLICT DO
// NOTHING), assigning a random carrier and tracking number. It is idempotent and
// returns the current shipment.
func (s *Service) Prepare(ctx context.Context, orderID uuid.UUID) (*domain.Shipment, error) {
	carrier := carriers[rand.Intn(len(carriers))]
	tracking := carrier + "-" + randToken(12)
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO shipments (id, order_id, carrier, tracking_number, status)
		VALUES ($1, $2, $3, $4, 'pending')
		ON CONFLICT (order_id) DO NOTHING`,
		uuid.New(), orderID, carrier, tracking); err != nil {
		return nil, fmt.Errorf("prepare shipment: %w", err)
	}
	return s.Get(ctx, orderID)
}

// Ship dispatches a pending shipment. With probability failureRate it returns a
// transient error WITHOUT mutating state (driving retries and, once exhausted,
// compensation). It is idempotent: an already-dispatched/delivered shipment is a
// no-op success.
func (s *Service) Ship(ctx context.Context, orderID uuid.UUID) (*domain.Shipment, error) {
	if rand.Float64() < s.failureRate {
		return nil, fmt.Errorf("carrier rejected shipment for %s", orderID)
	}
	if _, err := s.pool.Exec(ctx, `
		UPDATE shipments SET status = 'dispatched', shipped_at = now(), updated_at = now()
		WHERE order_id = $1 AND status = 'pending'`, orderID); err != nil {
		return nil, fmt.Errorf("ship: %w", err)
	}
	return s.Get(ctx, orderID)
}

// ConfirmDelivery marks the shipment delivered. It is idempotent.
func (s *Service) ConfirmDelivery(ctx context.Context, orderID uuid.UUID) (*domain.Shipment, error) {
	if _, err := s.pool.Exec(ctx, `
		UPDATE shipments SET status = 'delivered', delivered_at = now(), updated_at = now()
		WHERE order_id = $1 AND status <> 'delivered'`, orderID); err != nil {
		return nil, fmt.Errorf("confirm delivery: %w", err)
	}
	return s.Get(ctx, orderID)
}

// Get returns the shipment for an order, or domain.ErrNotFound.
func (s *Service) Get(ctx context.Context, orderID uuid.UUID) (*domain.Shipment, error) {
	sh := &domain.Shipment{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, order_id, carrier, tracking_number, status, shipped_at, delivered_at, created_at, updated_at
		FROM shipments WHERE order_id = $1`, orderID).Scan(
		&sh.ID, &sh.OrderID, &sh.Carrier, &sh.TrackingNumber, &sh.Status,
		&sh.ShippedAt, &sh.DeliveredAt, &sh.CreatedAt, &sh.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: shipment for order %s", domain.ErrNotFound, orderID)
	}
	if err != nil {
		return nil, fmt.Errorf("get shipment: %w", err)
	}
	return sh, nil
}

const alphanum = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// randToken returns a random uppercase alphanumeric string of length n.
func randToken(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = alphanum[rand.Intn(len(alphanum))]
	}
	return string(b)
}
