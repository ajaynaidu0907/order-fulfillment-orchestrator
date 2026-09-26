package fulfillment

import (
	"context"
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

// seedOrder inserts a customer and an order (shipments reference orders) and
// returns the order ID.
func seedOrder(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	ctx := context.Background()

	var customerID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO customers (name, email) VALUES ($1, $2) RETURNING id`,
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

func TestPrepare_Idempotent(t *testing.T) {
	pool := newPool(t)
	svc := New(pool, slog.Default(), 0)
	ctx := context.Background()
	orderID := seedOrder(t, pool)

	sh1, err := svc.Prepare(ctx, orderID)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if sh1.Status != domain.ShipmentPending || sh1.Carrier == "" || sh1.TrackingNumber == "" {
		t.Fatalf("unexpected shipment: %+v", sh1)
	}

	sh2, err := svc.Prepare(ctx, orderID)
	if err != nil {
		t.Fatalf("prepare replay: %v", err)
	}
	if sh2.ID != sh1.ID || sh2.TrackingNumber != sh1.TrackingNumber {
		t.Fatalf("prepare not idempotent: %s/%s vs %s/%s", sh1.ID, sh1.TrackingNumber, sh2.ID, sh2.TrackingNumber)
	}
}

func TestShip_Success(t *testing.T) {
	pool := newPool(t)
	svc := New(pool, slog.Default(), 0) // never fail
	ctx := context.Background()
	orderID := seedOrder(t, pool)
	if _, err := svc.Prepare(ctx, orderID); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	sh, err := svc.Ship(ctx, orderID)
	if err != nil {
		t.Fatalf("ship: %v", err)
	}
	if sh.Status != domain.ShipmentDispatched || sh.ShippedAt == nil {
		t.Fatalf("expected dispatched with shipped_at, got %+v", sh)
	}

	// Idempotent: shipping an already-dispatched shipment is a no-op success.
	sh2, err := svc.Ship(ctx, orderID)
	if err != nil || sh2.Status != domain.ShipmentDispatched {
		t.Fatalf("ship replay: status=%v err=%v", sh2.Status, err)
	}
}

func TestShip_TransientFailureLeavesStateUnchanged(t *testing.T) {
	pool := newPool(t)
	svc := New(pool, slog.Default(), 1.0) // always fail
	ctx := context.Background()
	orderID := seedOrder(t, pool)
	if _, err := svc.Prepare(ctx, orderID); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	if _, err := svc.Ship(ctx, orderID); err == nil {
		t.Fatalf("expected a transient ship failure")
	}

	// The failure must not have mutated state.
	sh, err := svc.Get(ctx, orderID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if sh.Status != domain.ShipmentPending || sh.ShippedAt != nil {
		t.Fatalf("failed ship must not mutate state, got %+v", sh)
	}
}

func TestConfirmDelivery_Idempotent(t *testing.T) {
	pool := newPool(t)
	svc := New(pool, slog.Default(), 0)
	ctx := context.Background()
	orderID := seedOrder(t, pool)
	if _, err := svc.Prepare(ctx, orderID); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := svc.Ship(ctx, orderID); err != nil {
		t.Fatalf("ship: %v", err)
	}

	sh, err := svc.ConfirmDelivery(ctx, orderID)
	if err != nil || sh.Status != domain.ShipmentDelivered || sh.DeliveredAt == nil {
		t.Fatalf("confirm: shipment=%+v err=%v", sh, err)
	}
	sh2, err := svc.ConfirmDelivery(ctx, orderID)
	if err != nil || sh2.Status != domain.ShipmentDelivered {
		t.Fatalf("confirm replay: status=%v err=%v", sh2.Status, err)
	}
}
