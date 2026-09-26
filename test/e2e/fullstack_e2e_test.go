package e2e

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/cache"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/config"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/domain"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/fulfillment"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/inventory"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/observability"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/orchestrator"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/payment"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/store"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/worker"
)

// TestFullStackSaga drives real orders through the whole system — the PostgreSQL
// store, the job queue, the domain services, the saga orchestrator, and the
// background worker — against throwaway Postgres and Redis containers. It proves
// the happy path reaches DELIVERED and that both failure modes compensate
// correctly (release inventory; refund payment + release inventory).
func TestFullStackSaga(t *testing.T) {
	dsn, pool, cleanupPG := StartPostgres(t)
	defer cleanupPG()
	_, rdb, cleanupR := StartRedis(t)
	defer cleanupR()
	_ = dsn

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	const sku = "TEST-FS-001"
	const unitPrice int64 = 4999
	const stock = 100
	customerID := seedFullStack(t, pool, sku, unitPrice, stock)

	t.Run("happy_path_reaches_DELIVERED", func(t *testing.T) {
		resetInventory(t, pool, sku, stock)
		st := buildStack(t, pool, rdb, 0.0, 0.0, logger)
		stop := startWorker(st.w)
		defer stop()

		const qty = 3
		o := createOrder(t, st.orders, customerID, sku, qty, unitPrice)

		final := waitForStatus(t, st.orders, o.ID, domain.StatusDelivered, 30*time.Second)
		require.Equal(t, domain.StatusDelivered, final.Status)

		// Inventory committed: available permanently reduced, nothing left reserved.
		avail, reserved := waitForInventory(t, pool, sku, stock-qty, 0, 10*time.Second)
		require.Equal(t, stock-qty, avail)
		require.Equal(t, 0, reserved)

		// Payment authorized; a shipment exists and is delivered.
		require.Equal(t, "authorized", latestPaymentStatus(t, pool, o.ID))
		require.Equal(t, "delivered", shipmentStatus(t, pool, o.ID))

		// Full audit trail of the forward flow.
		requireEventPath(t, st.orders, o.ID,
			domain.StatusInventoryReserved, domain.StatusPaymentAuthorized,
			domain.StatusReadyForFulfillment, domain.StatusShipped, domain.StatusDelivered)
	})

	t.Run("payment_decline_compensates_release_inventory", func(t *testing.T) {
		resetInventory(t, pool, sku, stock)
		st := buildStack(t, pool, rdb, 1.0 /* always decline */, 0.0, logger)
		stop := startWorker(st.w)
		defer stop()

		const qty = 5
		o := createOrder(t, st.orders, customerID, sku, qty, unitPrice)

		final := waitForStatus(t, st.orders, o.ID, domain.StatusFailed, 30*time.Second)
		require.Equal(t, domain.StatusFailed, final.Status)

		// Inventory that was reserved must be released back to available.
		avail, reserved := waitForInventory(t, pool, sku, stock, 0, 15*time.Second)
		require.Equal(t, stock, avail, "reserved stock should be fully released")
		require.Equal(t, 0, reserved)

		require.Equal(t, "declined", latestPaymentStatus(t, pool, o.ID))
	})

	t.Run("fulfillment_failure_compensates_refund_and_release", func(t *testing.T) {
		resetInventory(t, pool, sku, stock)
		// Payment succeeds, but shipping always fails -> retries exhaust -> compensate.
		st := buildStack(t, pool, rdb, 0.0, 1.0 /* always fail shipping */, logger)
		stop := startWorker(st.w)
		defer stop()

		const qty = 4
		o := createOrder(t, st.orders, customerID, sku, qty, unitPrice)

		// Shipping retries with exponential backoff (2+4+8+16s) before dead-lettering.
		final := waitForStatus(t, st.orders, o.ID, domain.StatusFailed, 75*time.Second)
		require.Equal(t, domain.StatusFailed, final.Status)

		// Both compensations must run: payment refunded AND inventory released.
		require.Equal(t, "refunded", waitForPaymentStatus(t, pool, o.ID, "refunded", 20*time.Second))
		avail, reserved := waitForInventory(t, pool, sku, stock, 0, 20*time.Second)
		require.Equal(t, stock, avail, "reserved stock should be released after refund")
		require.Equal(t, 0, reserved)
	})
}

// ---- helpers ---------------------------------------------------------------

type fullStack struct {
	orders *store.OrderStore
	jobs   *store.JobQueue
	w      *worker.Worker
}

func buildStack(t *testing.T, pool *pgxpool.Pool, rdb *redis.Client, declineRate, failureRate float64, logger *slog.Logger) *fullStack {
	t.Helper()
	ch := cache.NewCache(rdb)
	orders := store.NewOrderStore(pool)
	jobs := store.NewJobQueue(pool, 30*time.Second)
	pay := payment.New(pool, logger, declineRate)
	inv := inventory.New(pool, ch, logger)
	ful := fulfillment.New(pool, logger, failureRate)

	orch := orchestrator.New(orchestrator.Deps{
		Orders: orders, Jobs: jobs, Payment: pay, Inventory: inv, Fulfillment: ful,
		Metrics: observability.NewMetrics(), Logger: logger,
	})
	w := worker.New(worker.Deps{
		Jobs:      jobs,
		Processor: orch,
		Cfg: config.WorkerConfig{
			Concurrency:  4,
			PollInterval: 50 * time.Millisecond,
			ClaimBatch:   10,
			LockTTL:      30 * time.Second,
			ReapInterval: 2 * time.Second,
		},
		Metrics: observability.NewMetrics(),
		Logger:  logger,
	})
	return &fullStack{orders: orders, jobs: jobs, w: w}
}

// startWorker runs the worker in the background and returns a stop function that
// cancels it and waits for a clean drain.
func startWorker(w *worker.Worker) func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = w.Run(ctx)
		close(done)
	}()
	return func() {
		cancel()
		<-done
	}
}

func seedFullStack(t *testing.T, pool *pgxpool.Pool, sku string, unitPrice int64, stock int) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	customerID := uuid.New()
	_, err := pool.Exec(ctx,
		`INSERT INTO customers (id, name, email, phone, address) VALUES ($1, $2, $3, $4, '{}'::jsonb)`,
		customerID, "E2E Test Buyer", fmt.Sprintf("buyer+%s@example.test", customerID.String()[:8]), "+1 (555) 555-0100")
	require.NoError(t, err)

	_, err = pool.Exec(ctx,
		`INSERT INTO products (sku, name, description, category, brand, price_minor, currency)
		 VALUES ($1, $2, $3, $4, $5, $6, 'USD') ON CONFLICT (sku) DO NOTHING`,
		sku, "End-to-End Test Widget", "A synthetic product used only by e2e tests.", "Testing", "Proto", unitPrice)
	require.NoError(t, err)

	_, err = pool.Exec(ctx,
		`INSERT INTO inventory (sku, warehouse, available, reserved) VALUES ($1, 'MAIN', $2, 0)
		 ON CONFLICT (sku, warehouse) DO UPDATE SET available = EXCLUDED.available, reserved = 0`,
		sku, stock)
	require.NoError(t, err)
	return customerID
}

func resetInventory(t *testing.T, pool *pgxpool.Pool, sku string, stock int) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`UPDATE inventory SET available = $2, reserved = 0 WHERE sku = $1 AND warehouse = 'MAIN'`, sku, stock)
	require.NoError(t, err)
}

func createOrder(t *testing.T, orders *store.OrderStore, customerID uuid.UUID, sku string, qty int, unitPrice int64) *domain.Order {
	t.Helper()
	o := &domain.Order{
		ID:          uuid.New(),
		CustomerID:  customerID,
		Status:      domain.StatusCreated,
		Currency:    "USD",
		TotalAmount: int64(qty) * unitPrice,
		Items: []domain.OrderItem{{
			SKU: sku, Name: "End-to-End Test Widget", Quantity: qty, UnitPrice: unitPrice,
		}},
		ShippingAddress: domain.Address{
			Line1: "100 Integration Way", City: "Testburg", State: "CA", PostalCode: "90210", Country: "US",
		},
	}
	firstJob, err := domain.NewOrderJob(domain.JobReserveInventory, o.ID, time.Now())
	require.NoError(t, err)
	require.NoError(t, orders.Create(context.Background(), o, &firstJob))
	return o
}

func waitForStatus(t *testing.T, orders *store.OrderStore, id uuid.UUID, target domain.OrderStatus, timeout time.Duration) *domain.Order {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last domain.OrderStatus
	for time.Now().Before(deadline) {
		o, err := orders.Get(context.Background(), id)
		require.NoError(t, err)
		last = o.Status
		if o.Status == target {
			return o
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("order %s did not reach %s within %s (last status %s)", id, target, timeout, last)
	return nil
}

func waitForInventory(t *testing.T, pool *pgxpool.Pool, sku string, wantAvail, wantReserved int, timeout time.Duration) (int, int) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var avail, reserved int
	for time.Now().Before(deadline) {
		avail, reserved = readInventory(t, pool, sku)
		if avail == wantAvail && reserved == wantReserved {
			return avail, reserved
		}
		time.Sleep(100 * time.Millisecond)
	}
	return avail, reserved
}

func readInventory(t *testing.T, pool *pgxpool.Pool, sku string) (avail, reserved int) {
	t.Helper()
	err := pool.QueryRow(context.Background(),
		`SELECT available, reserved FROM inventory WHERE sku = $1 AND warehouse = 'MAIN'`, sku).
		Scan(&avail, &reserved)
	require.NoError(t, err)
	return avail, reserved
}

func latestPaymentStatus(t *testing.T, pool *pgxpool.Pool, orderID uuid.UUID) string {
	t.Helper()
	var status string
	err := pool.QueryRow(context.Background(),
		`SELECT status FROM payments WHERE order_id = $1 ORDER BY created_at DESC LIMIT 1`, orderID).Scan(&status)
	require.NoError(t, err)
	return status
}

func waitForPaymentStatus(t *testing.T, pool *pgxpool.Pool, orderID uuid.UUID, want string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var status string
	for time.Now().Before(deadline) {
		_ = pool.QueryRow(context.Background(),
			`SELECT status FROM payments WHERE order_id = $1 ORDER BY created_at DESC LIMIT 1`, orderID).Scan(&status)
		if status == want {
			return status
		}
		time.Sleep(100 * time.Millisecond)
	}
	return status
}

func shipmentStatus(t *testing.T, pool *pgxpool.Pool, orderID uuid.UUID) string {
	t.Helper()
	var status string
	err := pool.QueryRow(context.Background(),
		`SELECT status FROM shipments WHERE order_id = $1`, orderID).Scan(&status)
	require.NoError(t, err)
	return status
}

// requireEventPath asserts that the order_events audit trail contains the given
// target statuses in order.
func requireEventPath(t *testing.T, orders *store.OrderStore, id uuid.UUID, wantSequence ...domain.OrderStatus) {
	t.Helper()
	events, err := orders.Events(context.Background(), id)
	require.NoError(t, err)
	var got []domain.OrderStatus
	for _, e := range events {
		got = append(got, e.ToStatus)
	}
	// Every wanted status should appear, in order, within the recorded path.
	idx := 0
	for _, s := range got {
		if idx < len(wantSequence) && s == wantSequence[idx] {
			idx++
		}
	}
	require.Equalf(t, len(wantSequence), idx, "event path %v did not contain expected sequence %v", got, wantSequence)
}
