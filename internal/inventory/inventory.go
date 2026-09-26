// Package inventory implements domain.InventoryService against PostgreSQL, with
// an injected domain.Cache used for read-through caching of catalog products.
// Reserve/Release/Commit are all-or-nothing and idempotent per order.
package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/domain"
)

const (
	warehouse       = "MAIN"
	productCacheTTL = 60 * time.Second
)

// Service is the PostgreSQL-backed inventory adapter.
type Service struct {
	pool   *pgxpool.Pool
	cache  domain.Cache
	logger *slog.Logger
}

// New builds an inventory Service. cache is used as a read-through cache for
// GetProduct; it may be nil to disable caching.
func New(pool *pgxpool.Pool, cache domain.Cache, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{pool: pool, cache: cache, logger: logger}
}

var _ domain.InventoryService = (*Service)(nil)

// Reserve holds stock for every item on the order, all-or-nothing, inside one
// transaction. Each line inserts a reservation (ON CONFLICT DO NOTHING); a newly
// inserted line then decrements available stock guarded by availability. If any
// line lacks stock the whole transaction rolls back with
// domain.ErrInsufficientInventory. Lines whose reservation already existed are
// skipped, making the whole operation an idempotent replay.
func (s *Service) Reserve(ctx context.Context, orderID uuid.UUID, items []domain.OrderItem) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	for _, it := range items {
		tag, err := tx.Exec(ctx, `
			INSERT INTO inventory_reservations (order_id, sku, quantity, status)
			VALUES ($1, $2, $3, 'reserved')
			ON CONFLICT (order_id, sku) DO NOTHING`,
			orderID, it.SKU, it.Quantity)
		if err != nil {
			return fmt.Errorf("insert reservation %s: %w", it.SKU, err)
		}
		if tag.RowsAffected() == 0 {
			// Reservation already existed: idempotent replay, stock already held.
			continue
		}

		ct, err := tx.Exec(ctx, `
			UPDATE inventory
			SET available = available - $2, reserved = reserved + $2, updated_at = now()
			WHERE sku = $1 AND warehouse = $3 AND available >= $2`,
			it.SKU, it.Quantity, warehouse)
		if err != nil {
			return fmt.Errorf("reserve stock %s: %w", it.SKU, err)
		}
		if ct.RowsAffected() == 0 {
			// Insufficient stock (or unknown SKU): abandon the whole reservation.
			return fmt.Errorf("%w: sku %s (qty %d)", domain.ErrInsufficientInventory, it.SKU, it.Quantity)
		}
	}

	return tx.Commit(ctx)
}

// Release returns reserved stock to availability and marks the reservations
// released. It only touches reservations still in the 'reserved' state, so it is
// idempotent and safe after a partial run.
func (s *Service) Release(ctx context.Context, orderID uuid.UUID) error {
	return s.settle(ctx, orderID, func(tx pgx.Tx, sku string, qty int) error {
		_, err := tx.Exec(ctx, `
			UPDATE inventory
			SET available = available + $2, reserved = reserved - $2, updated_at = now()
			WHERE sku = $1 AND warehouse = $3`, sku, qty, warehouse)
		return err
	}, domain.ReservationReleased)
}

// Commit consumes reserved stock on shipment: availability was already reduced at
// reserve time, so only the held (reserved) count is decremented. It only touches
// reservations still in the 'reserved' state, so it is idempotent.
func (s *Service) Commit(ctx context.Context, orderID uuid.UUID) error {
	return s.settle(ctx, orderID, func(tx pgx.Tx, sku string, qty int) error {
		_, err := tx.Exec(ctx, `
			UPDATE inventory
			SET reserved = reserved - $2, updated_at = now()
			WHERE sku = $1 AND warehouse = $3`, sku, qty, warehouse)
		return err
	}, domain.ReservationCommitted)
}

// settle applies adjust to every still-reserved line of an order and moves those
// reservations to the given terminal status, all in one transaction.
func (s *Service) settle(ctx context.Context, orderID uuid.UUID, adjust func(pgx.Tx, string, int) error, to domain.ReservationStatus) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `
		SELECT sku, quantity FROM inventory_reservations
		WHERE order_id = $1 AND status = 'reserved'`, orderID)
	if err != nil {
		return fmt.Errorf("load reservations: %w", err)
	}
	type line struct {
		sku string
		qty int
	}
	var lines []line
	for rows.Next() {
		var l line
		if err := rows.Scan(&l.sku, &l.qty); err != nil {
			rows.Close()
			return fmt.Errorf("scan reservation: %w", err)
		}
		lines = append(lines, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read reservations: %w", err)
	}

	if len(lines) == 0 {
		return tx.Commit(ctx) // nothing reserved: idempotent no-op
	}

	for _, l := range lines {
		if err := adjust(tx, l.sku, l.qty); err != nil {
			return fmt.Errorf("adjust stock %s: %w", l.sku, err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inventory_reservations SET status = $2, updated_at = now()
		WHERE order_id = $1 AND status = 'reserved'`, orderID, to); err != nil {
		return fmt.Errorf("update reservation status: %w", err)
	}

	return tx.Commit(ctx)
}

// GetProduct returns a catalog product by SKU, reading through the injected cache
// (key "product:"+sku, TTL 60s, JSON-encoded). Returns domain.ErrNotFound when
// the SKU is unknown.
func (s *Service) GetProduct(ctx context.Context, sku string) (*domain.Product, error) {
	key := "product:" + sku
	if s.cache != nil {
		if b, found, err := s.cache.Get(ctx, key); err != nil {
			s.logger.WarnContext(ctx, "product cache get failed", "sku", sku, "err", err)
		} else if found {
			var p domain.Product
			if err := json.Unmarshal(b, &p); err == nil {
				return &p, nil
			}
		}
	}

	p := &domain.Product{}
	err := s.pool.QueryRow(ctx, `
		SELECT sku, name, COALESCE(description, ''), category, COALESCE(brand, ''), price_minor, currency, created_at
		FROM products WHERE sku = $1`, sku).Scan(
		&p.SKU, &p.Name, &p.Description, &p.Category, &p.Brand, &p.Price, &p.Currency, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: product %s", domain.ErrNotFound, sku)
	}
	if err != nil {
		return nil, fmt.Errorf("get product: %w", err)
	}

	if s.cache != nil {
		if b, err := json.Marshal(p); err == nil {
			if err := s.cache.Set(ctx, key, b, productCacheTTL); err != nil {
				s.logger.WarnContext(ctx, "product cache set failed", "sku", sku, "err", err)
			}
		}
	}
	return p, nil
}

// ListProducts returns catalog products matching the filter, ordered by SKU.
func (s *Service) ListProducts(ctx context.Context, f domain.ProductFilter) ([]domain.Product, error) {
	var (
		conds []string
		args  []any
	)
	if f.Category != "" {
		args = append(args, f.Category)
		conds = append(conds, fmt.Sprintf("category = $%d", len(args)))
	}
	if f.Search != "" {
		args = append(args, "%"+f.Search+"%")
		conds = append(conds, fmt.Sprintf("name ILIKE $%d", len(args)))
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	args = append(args, limit)
	limClause := fmt.Sprintf("$%d", len(args))
	args = append(args, f.Offset)
	offClause := fmt.Sprintf("$%d", len(args))

	q := fmt.Sprintf(`
		SELECT sku, name, COALESCE(description, ''), category, COALESCE(brand, ''), price_minor, currency, created_at
		FROM products %s
		ORDER BY sku
		LIMIT %s OFFSET %s`, where, limClause, offClause)

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list products: %w", err)
	}
	defer rows.Close()

	var out []domain.Product
	for rows.Next() {
		var p domain.Product
		if err := rows.Scan(
			&p.SKU, &p.Name, &p.Description, &p.Category, &p.Brand, &p.Price, &p.Currency, &p.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan product: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
