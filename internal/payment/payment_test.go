package payment

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/domain"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/store"
)

// startPostgres starts a PostgreSQL container, converting the panic that
// testcontainers raises when no Docker host can be found into an ordinary error,
// so callers can skip cleanly.
func startPostgres(ctx context.Context) (c *postgres.PostgresContainer, err error) {
	defer func() {
		if r := recover(); r != nil {
			c, err = nil, fmt.Errorf("docker unavailable: %v", r)
		}
	}()
	return postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("ofo"),
		postgres.WithUsername("ofo"),
		postgres.WithPassword("ofo"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second)),
	)
}

// newPool starts a throwaway PostgreSQL container, applies the schema, and
// returns a connected pool. It skips the test cleanly when Docker is unavailable.
func newPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	container, err := startPostgres(ctx)
	if err != nil {
		t.Skipf("docker not available: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	if err := store.Migrate(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// seedOrder inserts a customer and an order (payments reference orders) and
// returns the order ID.
func seedOrder(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	ctx := context.Background()

	var customerID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO customers (name, email) VALUES ($1, $2) RETURNING id`,
		"Test Customer", "cust-"+uuid.NewString()+"@example.test").Scan(&customerID); err != nil {
		t.Fatalf("seed customer: %v", err)
	}

	var orderID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO orders (customer_id, status, currency, total_amount_minor)
		VALUES ($1, 'CREATED', 'USD', 1999) RETURNING id`, customerID).Scan(&orderID); err != nil {
		t.Fatalf("seed order: %v", err)
	}
	return orderID
}

func TestAuthorize_SuccessAndIdempotency(t *testing.T) {
	pool := newPool(t)
	svc := New(pool, slog.Default(), 0) // never decline
	ctx := context.Background()
	orderID := seedOrder(t, pool)

	req := domain.AuthorizeRequest{
		OrderID:        orderID,
		Amount:         1999,
		Currency:       "USD",
		IdempotencyKey: "auth:" + orderID.String(),
	}

	p1, err := svc.Authorize(ctx, req)
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if p1.Status != domain.PaymentAuthorized || p1.ProviderRef == "" {
		t.Fatalf("expected authorized payment with provider_ref, got %+v", p1)
	}

	// Idempotent replay: same key returns the same row, no second charge.
	p2, err := svc.Authorize(ctx, req)
	if err != nil {
		t.Fatalf("authorize replay: %v", err)
	}
	if p2.ID != p1.ID {
		t.Fatalf("idempotent replay created a new payment: %s != %s", p2.ID, p1.ID)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM payments WHERE order_id=$1`, orderID).Scan(&count); err != nil {
		t.Fatalf("count payments: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 payment row, got %d", count)
	}
}

func TestAuthorize_Decline(t *testing.T) {
	pool := newPool(t)
	svc := New(pool, slog.Default(), 1.0) // always decline
	ctx := context.Background()
	orderID := seedOrder(t, pool)

	req := domain.AuthorizeRequest{
		OrderID:        orderID,
		Amount:         500,
		Currency:       "USD",
		IdempotencyKey: "auth:" + orderID.String(),
	}

	p, err := svc.Authorize(ctx, req)
	if !errors.Is(err, domain.ErrPaymentDeclined) {
		t.Fatalf("expected ErrPaymentDeclined, got %v", err)
	}
	if p == nil || p.Status != domain.PaymentDeclined || p.FailureReason != "card_declined" {
		t.Fatalf("expected declined payment with reason card_declined, got %+v", p)
	}

	// Replay of a declined authorization reports declined again, same row.
	p2, err := svc.Authorize(ctx, req)
	if !errors.Is(err, domain.ErrPaymentDeclined) {
		t.Fatalf("expected ErrPaymentDeclined on replay, got %v", err)
	}
	if p2.ID != p.ID {
		t.Fatalf("declined replay created a new payment row")
	}
}

func TestCaptureAndRefund_Idempotency(t *testing.T) {
	pool := newPool(t)
	svc := New(pool, slog.Default(), 0)
	ctx := context.Background()
	orderID := seedOrder(t, pool)

	if _, err := svc.Authorize(ctx, domain.AuthorizeRequest{
		OrderID: orderID, Amount: 1000, Currency: "USD", IdempotencyKey: "auth:" + orderID.String(),
	}); err != nil {
		t.Fatalf("authorize: %v", err)
	}

	cap1, err := svc.Capture(ctx, orderID)
	if err != nil || cap1.Status != domain.PaymentCaptured {
		t.Fatalf("capture: status=%v err=%v", cap1.Status, err)
	}
	cap2, err := svc.Capture(ctx, orderID)
	if err != nil || cap2.Status != domain.PaymentCaptured {
		t.Fatalf("capture replay: status=%v err=%v", cap2.Status, err)
	}

	ref1, err := svc.Refund(ctx, orderID)
	if err != nil || ref1.Status != domain.PaymentRefunded {
		t.Fatalf("refund: status=%v err=%v", ref1.Status, err)
	}
	ref2, err := svc.Refund(ctx, orderID)
	if err != nil || ref2.Status != domain.PaymentRefunded {
		t.Fatalf("refund replay: status=%v err=%v", ref2.Status, err)
	}
}

func TestRefund_NoPaymentIsNoOp(t *testing.T) {
	pool := newPool(t)
	svc := New(pool, slog.Default(), 0)
	ctx := context.Background()
	orderID := seedOrder(t, pool)

	p, err := svc.Refund(ctx, orderID)
	if err != nil {
		t.Fatalf("refund with no payment should be a no-op success, got err=%v", err)
	}
	if p != nil {
		t.Fatalf("expected nil payment for no-op refund, got %+v", p)
	}
}
