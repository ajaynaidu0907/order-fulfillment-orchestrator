// Package payment implements domain.PaymentService against PostgreSQL. It is a
// mock payment provider: authorizations succeed or are declined at a configured
// rate, and every operation is idempotent per order / idempotency key so retried
// jobs are safe.
package payment

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

// Service is the PostgreSQL-backed payment adapter.
type Service struct {
	pool        *pgxpool.Pool
	logger      *slog.Logger
	declineRate float64
}

// New builds a payment Service. declineRate is the probability (0..1) that an
// authorization of a not-yet-seen order is declined, used to exercise the
// compensation path in demos and tests.
func New(pool *pgxpool.Pool, logger *slog.Logger, declineRate float64) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{pool: pool, logger: logger, declineRate: declineRate}
}

var _ domain.PaymentService = (*Service)(nil)

// Authorize authorizes a payment idempotently. If a payment already exists for
// the request's idempotency key (or order) it is returned as-is. Otherwise a new
// payment is created: declined with probability declineRate, else authorized. The
// insert uses ON CONFLICT (idempotency_key) DO NOTHING so concurrent retries
// converge on a single row. A declined payment is returned together with
// domain.ErrPaymentDeclined.
func (s *Service) Authorize(ctx context.Context, req domain.AuthorizeRequest) (*domain.Payment, error) {
	// 1. Return an existing payment if this request has already been processed.
	if existing, err := s.lookup(ctx, req.IdempotencyKey, req.OrderID); err != nil {
		return nil, err
	} else if existing != nil {
		return result(existing)
	}

	// 2. Decide the outcome for a fresh authorization.
	status := domain.PaymentAuthorized
	var providerRef, failureReason string
	if rand.Float64() < s.declineRate {
		status = domain.PaymentDeclined
		failureReason = "card_declined"
	} else {
		providerRef = "auth_" + randToken(16)
	}

	// 3. Insert, converging concurrent retries with the same key onto one row.
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO payments (id, order_id, amount_minor, currency, status, provider_ref, idempotency_key, failure_reason)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7, ''), NULLIF($8, ''))
		ON CONFLICT (idempotency_key) DO NOTHING`,
		uuid.New(), req.OrderID, req.Amount, req.Currency, status, providerRef, req.IdempotencyKey, failureReason,
	); err != nil {
		return nil, fmt.Errorf("insert payment: %w", err)
	}

	// 4. Read back the winning row (ours, or a concurrent insert's).
	p, err := s.lookup(ctx, req.IdempotencyKey, req.OrderID)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, fmt.Errorf("insert payment: row vanished after insert")
	}
	return result(p)
}

// Capture advances an authorized payment to captured. It is idempotent: an
// already-captured (or non-authorized) payment is left unchanged.
func (s *Service) Capture(ctx context.Context, orderID uuid.UUID) (*domain.Payment, error) {
	if _, err := s.pool.Exec(ctx, `
		UPDATE payments SET status = 'captured', updated_at = now()
		WHERE order_id = $1 AND status = 'authorized'`, orderID); err != nil {
		return nil, fmt.Errorf("capture payment: %w", err)
	}
	return s.Get(ctx, orderID)
}

// Refund sets the order's payment to refunded. It is idempotent, and a no-op
// success when the order has no payment.
func (s *Service) Refund(ctx context.Context, orderID uuid.UUID) (*domain.Payment, error) {
	if _, err := s.pool.Exec(ctx, `
		UPDATE payments SET status = 'refunded', updated_at = now()
		WHERE order_id = $1 AND status <> 'refunded'`, orderID); err != nil {
		return nil, fmt.Errorf("refund payment: %w", err)
	}
	p, err := s.Get(ctx, orderID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil // no payment to refund: idempotent no-op success
	}
	return p, err
}

// Get returns the payment for an order, or domain.ErrNotFound.
func (s *Service) Get(ctx context.Context, orderID uuid.UUID) (*domain.Payment, error) {
	return s.scan(ctx, `WHERE order_id = $1 ORDER BY created_at DESC, id LIMIT 1`, orderID)
}

// lookup returns the existing payment for the idempotency key (preferred) or the
// order, or nil when none exists.
func (s *Service) lookup(ctx context.Context, key string, orderID uuid.UUID) (*domain.Payment, error) {
	if key != "" {
		p, err := s.scan(ctx, `WHERE idempotency_key = $1`, key)
		if err == nil {
			return p, nil
		}
		if !errors.Is(err, domain.ErrNotFound) {
			return nil, err
		}
	}
	p, err := s.Get(ctx, orderID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	return p, err
}

func (s *Service) scan(ctx context.Context, where string, args ...any) (*domain.Payment, error) {
	p := &domain.Payment{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, order_id, amount_minor, currency, status,
		       COALESCE(provider_ref, ''), COALESCE(idempotency_key, ''), COALESCE(failure_reason, ''),
		       created_at, updated_at
		FROM payments `+where, args...).Scan(
		&p.ID, &p.OrderID, &p.Amount, &p.Currency, &p.Status,
		&p.ProviderRef, &p.IdempotencyKey, &p.FailureReason, &p.CreatedAt, &p.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: payment", domain.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("scan payment: %w", err)
	}
	return p, nil
}

// result pairs a payment with domain.ErrPaymentDeclined when it was declined, so
// callers see the decline both on first authorization and on idempotent replay.
func result(p *domain.Payment) (*domain.Payment, error) {
	if p.Status == domain.PaymentDeclined {
		return p, domain.ErrPaymentDeclined
	}
	return p, nil
}

const alphanum = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// randToken returns a random alphanumeric string of length n (synthetic data).
func randToken(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = alphanum[rand.Intn(len(alphanum))]
	}
	return string(b)
}
