package inventory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
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

// fakeCache is an in-memory domain.Cache used to assert read-through behaviour
// deterministically without needing a Redis container.
type fakeCache struct {
	mu   sync.Mutex
	data map[string][]byte
	gets int
	sets int
}

func newFakeCache() *fakeCache { return &fakeCache{data: map[string][]byte{}} }

func (f *fakeCache) Get(_ context.Context, key string) ([]byte, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	b, ok := f.data[key]
	return b, ok, nil
}

func (f *fakeCache) Set(_ context.Context, key string, val []byte, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets++
	f.data[key] = val
	return nil
}

func (f *fakeCache) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.data, key)
	return nil
}

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

// seedProduct inserts a product and a MAIN-warehouse inventory row with the given
// available quantity.
func seedProduct(t *testing.T, pool *pgxpool.Pool, sku string, available int) {
	t.Helper()
	mustExec(t, pool, `INSERT INTO products (sku, name, category, price_minor, currency) VALUES ($1, $2, 'general', 999, 'USD')`, sku, "Product "+sku)
	mustExec(t, pool, `INSERT INTO inventory (sku, warehouse, available, reserved) VALUES ($1, 'MAIN', $2, 0)`, sku, available)
}

// seedOrder inserts a customer and an order and returns the order ID.
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

func stock(t *testing.T, pool *pgxpool.Pool, sku string) (available, reserved int) {
	t.Helper()
	if err := pool.QueryRow(context.Background(),
		`SELECT available, reserved FROM inventory WHERE sku=$1 AND warehouse='MAIN'`, sku).
		Scan(&available, &reserved); err != nil {
		t.Fatalf("read stock %s: %v", sku, err)
	}
	return available, reserved
}

func TestReserve_Success(t *testing.T) {
	pool := newPool(t)
	svc := New(pool, nil, slog.Default())
	ctx := context.Background()
	seedProduct(t, pool, "SKU-A", 10)
	orderID := seedOrder(t, pool)

	if err := svc.Reserve(ctx, orderID, []domain.OrderItem{{SKU: "SKU-A", Quantity: 3}}); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if avail, reserved := stock(t, pool, "SKU-A"); avail != 7 || reserved != 3 {
		t.Fatalf("expected available=7 reserved=3, got available=%d reserved=%d", avail, reserved)
	}
}

func TestReserve_Insufficient(t *testing.T) {
	pool := newPool(t)
	svc := New(pool, nil, slog.Default())
	ctx := context.Background()
	seedProduct(t, pool, "SKU-A", 10)
	seedProduct(t, pool, "SKU-B", 1)
	orderID := seedOrder(t, pool)

	err := svc.Reserve(ctx, orderID, []domain.OrderItem{
		{SKU: "SKU-A", Quantity: 2},
		{SKU: "SKU-B", Quantity: 5}, // not enough stock
	})
	if !errors.Is(err, domain.ErrInsufficientInventory) {
		t.Fatalf("expected ErrInsufficientInventory, got %v", err)
	}

	// All-or-nothing: the whole transaction rolled back, SKU-A untouched.
	if avail, reserved := stock(t, pool, "SKU-A"); avail != 10 || reserved != 0 {
		t.Fatalf("expected rollback (available=10 reserved=0), got available=%d reserved=%d", avail, reserved)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM inventory_reservations WHERE order_id=$1`, orderID).Scan(&count); err != nil {
		t.Fatalf("count reservations: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 reservations after rollback, got %d", count)
	}
}

func TestReserve_IdempotentReplay(t *testing.T) {
	pool := newPool(t)
	svc := New(pool, nil, slog.Default())
	ctx := context.Background()
	seedProduct(t, pool, "SKU-A", 10)
	orderID := seedOrder(t, pool)
	items := []domain.OrderItem{{SKU: "SKU-A", Quantity: 4}}

	if err := svc.Reserve(ctx, orderID, items); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if err := svc.Reserve(ctx, orderID, items); err != nil {
		t.Fatalf("reserve replay: %v", err)
	}
	if avail, reserved := stock(t, pool, "SKU-A"); avail != 6 || reserved != 4 {
		t.Fatalf("replay must not double-decrement: available=%d reserved=%d", avail, reserved)
	}
}

func TestRelease_RestoresAvailability(t *testing.T) {
	pool := newPool(t)
	svc := New(pool, nil, slog.Default())
	ctx := context.Background()
	seedProduct(t, pool, "SKU-A", 10)
	orderID := seedOrder(t, pool)
	if err := svc.Reserve(ctx, orderID, []domain.OrderItem{{SKU: "SKU-A", Quantity: 4}}); err != nil {
		t.Fatalf("reserve: %v", err)
	}

	if err := svc.Release(ctx, orderID); err != nil {
		t.Fatalf("release: %v", err)
	}
	if avail, reserved := stock(t, pool, "SKU-A"); avail != 10 || reserved != 0 {
		t.Fatalf("release must restore stock: available=%d reserved=%d", avail, reserved)
	}
	// Idempotent: a second release is a no-op.
	if err := svc.Release(ctx, orderID); err != nil {
		t.Fatalf("release replay: %v", err)
	}
	if avail, reserved := stock(t, pool, "SKU-A"); avail != 10 || reserved != 0 {
		t.Fatalf("release replay changed stock: available=%d reserved=%d", avail, reserved)
	}
}

func TestCommit_ReducesReserved(t *testing.T) {
	pool := newPool(t)
	svc := New(pool, nil, slog.Default())
	ctx := context.Background()
	seedProduct(t, pool, "SKU-A", 10)
	orderID := seedOrder(t, pool)
	if err := svc.Reserve(ctx, orderID, []domain.OrderItem{{SKU: "SKU-A", Quantity: 4}}); err != nil {
		t.Fatalf("reserve: %v", err)
	}

	if err := svc.Commit(ctx, orderID); err != nil {
		t.Fatalf("commit: %v", err)
	}
	// availability was already reduced at reserve time; commit only clears reserved.
	if avail, reserved := stock(t, pool, "SKU-A"); avail != 6 || reserved != 0 {
		t.Fatalf("commit must reduce reserved only: available=%d reserved=%d", avail, reserved)
	}
	// Idempotent.
	if err := svc.Commit(ctx, orderID); err != nil {
		t.Fatalf("commit replay: %v", err)
	}
	if avail, reserved := stock(t, pool, "SKU-A"); avail != 6 || reserved != 0 {
		t.Fatalf("commit replay changed stock: available=%d reserved=%d", avail, reserved)
	}
}

func TestGetProduct_CacheReadThrough(t *testing.T) {
	pool := newPool(t)
	fc := newFakeCache()
	svc := New(pool, fc, slog.Default())
	ctx := context.Background()
	seedProduct(t, pool, "SKU-A", 10)

	// First read: cache miss -> DB hit -> cache populated.
	p1, err := svc.GetProduct(ctx, "SKU-A")
	if err != nil || p1.SKU != "SKU-A" {
		t.Fatalf("first get: p=%+v err=%v", p1, err)
	}
	if fc.sets != 1 {
		t.Fatalf("expected cache populated once, sets=%d", fc.sets)
	}

	// Delete the DB rows so the second read can only succeed from cache.
	mustExec(t, pool, `DELETE FROM inventory WHERE sku='SKU-A'`)
	mustExec(t, pool, `DELETE FROM products WHERE sku='SKU-A'`)

	p2, err := svc.GetProduct(ctx, "SKU-A")
	if err != nil || p2.SKU != "SKU-A" {
		t.Fatalf("second get should be served from cache: p=%+v err=%v", p2, err)
	}
	if fc.sets != 1 {
		t.Fatalf("cache hit should not repopulate, sets=%d", fc.sets)
	}
}

func TestGetProduct_NotFound(t *testing.T) {
	pool := newPool(t)
	svc := New(pool, newFakeCache(), slog.Default())
	if _, err := svc.GetProduct(context.Background(), "does-not-exist"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestListProducts_Filters(t *testing.T) {
	pool := newPool(t)
	svc := New(pool, nil, slog.Default())
	ctx := context.Background()
	mustExec(t, pool, `INSERT INTO products (sku,name,category,price_minor,currency) VALUES ('W1','Widget Alpha','tools',100,'USD')`)
	mustExec(t, pool, `INSERT INTO products (sku,name,category,price_minor,currency) VALUES ('W2','Widget Beta','tools',200,'USD')`)
	mustExec(t, pool, `INSERT INTO products (sku,name,category,price_minor,currency) VALUES ('G1','Gadget Gamma','gadgets',300,'USD')`)

	if all, err := svc.ListProducts(ctx, domain.ProductFilter{}); err != nil || len(all) != 3 {
		t.Fatalf("list all: n=%d err=%v", len(all), err)
	}
	if tools, err := svc.ListProducts(ctx, domain.ProductFilter{Category: "tools"}); err != nil || len(tools) != 2 {
		t.Fatalf("list tools: n=%d err=%v", len(tools), err)
	}
	search, err := svc.ListProducts(ctx, domain.ProductFilter{Search: "gamma"})
	if err != nil || len(search) != 1 || search[0].SKU != "G1" {
		t.Fatalf("search gamma: %+v err=%v", search, err)
	}
}
