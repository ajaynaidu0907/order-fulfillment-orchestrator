// Command seed populates the database with realistic synthetic data: customers,
// the product catalog, and per-SKU inventory, plus a handful of live orders so a
// running system immediately shows pipeline activity. It is idempotent — every
// write is an upsert and seed orders are keyed so repeated runs do not pile up.
//
// It reuses app.NewBase to load config, run migrations, and connect, so it honours
// the same OFO_* environment variables as the other binaries. Extra knobs:
//
//	OFO_SEED_CUSTOMERS  number of customers to generate (default 50)
//	OFO_SEED_ORDERS     number of live orders to create  (default 5)
//	OFO_SEED            PRNG seed for reproducible data   (default 42)
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/app"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/domain"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/mockdata"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/store"
)

func main() {
	ctx := context.Background()

	base, err := app.NewBase(ctx, true)
	if err != nil {
		// The logger lives on Base, so it is not available before this succeeds.
		fmt.Fprintf(os.Stderr, "seed: failed to initialise: %v\n", err)
		os.Exit(1)
	}
	defer base.Close(ctx)

	if err := run(ctx, base); err != nil {
		base.Logger.Error("seed failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, base *app.Base) error {
	log := base.Logger

	customerCount := envInt("OFO_SEED_CUSTOMERS", 50)
	orderCount := envInt("OFO_SEED_ORDERS", 5)
	seed := envInt64("OFO_SEED", 42)

	customers := mockdata.Customers(customerCount, seed)
	products := mockdata.Products()
	inventory := mockdata.InventoryFor(products, seed)

	log.Info("seeding started",
		"customers", len(customers),
		"products", len(products),
		"inventory_rows", len(inventory),
		"orders_requested", orderCount,
		"seed", seed,
	)

	custInserted, err := upsertCustomers(ctx, base.Pool, customers)
	if err != nil {
		return err
	}
	if err := upsertProducts(ctx, base.Pool, products); err != nil {
		return err
	}
	if err := upsertInventory(ctx, base.Pool, inventory); err != nil {
		return err
	}

	ordersCreated, err := seedOrders(ctx, base.Pool, customers, products, orderCount, seed)
	if err != nil {
		return err
	}

	log.Info("seeding complete",
		"customers_inserted", custInserted,
		"customers_total", len(customers),
		"products_upserted", len(products),
		"inventory_upserted", len(inventory),
		"orders_created", ordersCreated,
		"orders_requested", orderCount,
	)
	return nil
}

// upsertCustomers inserts customers, skipping any whose email already exists.
// It returns the number of newly inserted rows.
func upsertCustomers(ctx context.Context, pool *pgxpool.Pool, customers []domain.Customer) (int, error) {
	inserted := 0
	for _, c := range customers {
		addr, err := json.Marshal(c.Address)
		if err != nil {
			return inserted, fmt.Errorf("marshal address for %s: %w", c.Email, err)
		}
		tag, err := pool.Exec(ctx, `
			INSERT INTO customers (id, name, email, phone, address)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (email) DO NOTHING`,
			c.ID, c.Name, c.Email, c.Phone, addr,
		)
		if err != nil {
			return inserted, fmt.Errorf("upsert customer %s: %w", c.Email, err)
		}
		inserted += int(tag.RowsAffected())
	}
	return inserted, nil
}

// upsertProducts inserts or refreshes the catalog, updating mutable fields on
// conflict so re-running the seeder keeps prices and descriptions current.
func upsertProducts(ctx context.Context, pool *pgxpool.Pool, products []domain.Product) error {
	for _, p := range products {
		_, err := pool.Exec(ctx, `
			INSERT INTO products (sku, name, description, category, brand, price_minor, currency)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (sku) DO UPDATE SET
				name        = EXCLUDED.name,
				description = EXCLUDED.description,
				category    = EXCLUDED.category,
				brand       = EXCLUDED.brand,
				price_minor = EXCLUDED.price_minor,
				currency    = EXCLUDED.currency`,
			p.SKU, p.Name, p.Description, p.Category, p.Brand, p.Price, p.Currency,
		)
		if err != nil {
			return fmt.Errorf("upsert product %s: %w", p.SKU, err)
		}
	}
	return nil
}

// upsertInventory sets the available stock per SKU/warehouse, refreshing the
// available count on conflict while leaving any existing reservations untouched.
func upsertInventory(ctx context.Context, pool *pgxpool.Pool, items []domain.InventoryItem) error {
	for _, it := range items {
		_, err := pool.Exec(ctx, `
			INSERT INTO inventory (sku, warehouse, available, reserved)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (sku, warehouse) DO UPDATE SET
				available  = EXCLUDED.available,
				updated_at = now()`,
			it.SKU, it.Warehouse, it.Available, it.Reserved,
		)
		if err != nil {
			return fmt.Errorf("upsert inventory %s@%s: %w", it.SKU, it.Warehouse, err)
		}
	}
	return nil
}

// seedOrders creates up to n live orders through the OrderStore so the worker
// picks them up. Customer IDs are resolved from the database (not the generated
// structs) so the orders reference whatever rows actually exist, which keeps the
// step correct even when customers were inserted on a prior run. Each order uses a
// stable idempotency key, so re-running the seeder skips already-created orders.
func seedOrders(ctx context.Context, pool *pgxpool.Pool, customers []domain.Customer, products []domain.Product, n int, seed int64) (int, error) {
	if n <= 0 {
		return 0, nil
	}

	emailToID, err := resolveCustomerIDs(ctx, pool, customers)
	if err != nil {
		return 0, err
	}

	reqs := mockdata.SampleOrderRequests(customers, products, n, seed+1)
	orders := store.NewOrderStore(pool)
	now := time.Now()
	created := 0

	for i, req := range reqs {
		custID, ok := emailToID[req.Customer.Email]
		if !ok {
			// The customer was not persisted (unexpected); skip rather than fail.
			continue
		}

		items := make([]domain.OrderItem, 0, len(req.Lines))
		for _, ln := range req.Lines {
			items = append(items, domain.OrderItem{
				SKU:       ln.Product.SKU,
				Name:      ln.Product.Name,
				Quantity:  ln.Quantity,
				UnitPrice: ln.Product.Price,
			})
		}

		order := &domain.Order{
			ID:              uuid.New(),
			CustomerID:      custID,
			Status:          domain.StatusCreated,
			Currency:        "USD",
			Items:           items,
			ShippingAddress: req.Customer.Address,
			IdempotencyKey:  fmt.Sprintf("seed-order-%d", i),
		}
		order.TotalAmount = order.ComputeTotal()

		firstJob, err := domain.NewOrderJob(domain.JobReserveInventory, order.ID, now)
		if err != nil {
			return created, fmt.Errorf("build first job for seed order %d: %w", i, err)
		}

		if err := orders.Create(ctx, order, &firstJob); err != nil {
			if errors.Is(err, domain.ErrConflict) {
				// Already seeded on an earlier run — idempotent skip.
				continue
			}
			return created, fmt.Errorf("create seed order %d: %w", i, err)
		}
		created++
	}
	return created, nil
}

// resolveCustomerIDs maps generated customer emails to their authoritative IDs in
// the database.
func resolveCustomerIDs(ctx context.Context, pool *pgxpool.Pool, customers []domain.Customer) (map[string]uuid.UUID, error) {
	emails := make([]string, 0, len(customers))
	for _, c := range customers {
		emails = append(emails, c.Email)
	}

	rows, err := pool.Query(ctx, `SELECT id, email FROM customers WHERE email = ANY($1)`, emails)
	if err != nil {
		return nil, fmt.Errorf("resolve customer ids: %w", err)
	}
	defer rows.Close()

	out := make(map[string]uuid.UUID, len(customers))
	for rows.Next() {
		var (
			id    uuid.UUID
			email string
		)
		if err := rows.Scan(&id, &email); err != nil {
			return nil, err
		}
		out[email] = id
	}
	return out, rows.Err()
}

func envInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envInt64(key string, def int64) int64 {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}
