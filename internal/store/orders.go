package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/domain"
)

// OrderStore is the PostgreSQL implementation of domain.OrderStore.
type OrderStore struct {
	pool *pgxpool.Pool
}

// NewOrderStore builds an OrderStore over the given pool.
func NewOrderStore(pool *pgxpool.Pool) *OrderStore {
	return &OrderStore{pool: pool}
}

var _ domain.OrderStore = (*OrderStore)(nil)

// Create inserts the order, its items, an initial audit event, and an optional
// first job in a single transaction (the transactional outbox pattern).
func (s *OrderStore) Create(ctx context.Context, o *domain.Order, firstJob *domain.Job) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	if o.ID == uuid.Nil {
		o.ID = uuid.New()
	}
	if o.Status == "" {
		o.Status = domain.StatusCreated
	}
	addr, err := json.Marshal(o.ShippingAddress)
	if err != nil {
		return fmt.Errorf("marshal address: %w", err)
	}

	err = tx.QueryRow(ctx, `
		INSERT INTO orders (id, customer_id, status, currency, total_amount_minor, shipping_address, idempotency_key, version)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), 0)
		RETURNING created_at, updated_at, version`,
		o.ID, o.CustomerID, o.Status, o.Currency, o.TotalAmount, addr, o.IdempotencyKey,
	).Scan(&o.CreatedAt, &o.UpdatedAt, &o.Version)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: duplicate order idempotency key", domain.ErrConflict)
		}
		return fmt.Errorf("insert order: %w", err)
	}

	for i := range o.Items {
		it := &o.Items[i]
		if it.ID == uuid.Nil {
			it.ID = uuid.New()
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO order_items (id, order_id, sku, name, quantity, unit_price_minor)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			it.ID, o.ID, it.SKU, it.Name, it.Quantity, it.UnitPrice,
		); err != nil {
			return fmt.Errorf("insert order item %s: %w", it.SKU, err)
		}
	}

	detail, _ := json.Marshal(map[string]any{"items": len(o.Items), "total_minor": o.TotalAmount})
	if _, err := tx.Exec(ctx, `
		INSERT INTO order_events (order_id, from_status, to_status, event, detail)
		VALUES ($1, NULL, $2, 'order_created', $3)`,
		o.ID, o.Status, detail,
	); err != nil {
		return fmt.Errorf("insert create event: %w", err)
	}

	if firstJob != nil {
		if firstJob.OrderID == nil {
			id := o.ID
			firstJob.OrderID = &id
		}
		if err := insertJob(ctx, tx, firstJob); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// Get returns an order with its items, or domain.ErrNotFound.
func (s *OrderStore) Get(ctx context.Context, id uuid.UUID) (*domain.Order, error) {
	o, err := scanOrder(ctx, s.pool, `WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	if err := s.loadItems(ctx, o); err != nil {
		return nil, err
	}
	return o, nil
}

// GetByIdempotencyKey returns an existing order for a create idempotency key.
func (s *OrderStore) GetByIdempotencyKey(ctx context.Context, key string) (*domain.Order, error) {
	o, err := scanOrder(ctx, s.pool, `WHERE idempotency_key = $1`, key)
	if err != nil {
		return nil, err
	}
	if err := s.loadItems(ctx, o); err != nil {
		return nil, err
	}
	return o, nil
}

// List returns orders matching the filter, newest first.
func (s *OrderStore) List(ctx context.Context, f domain.OrderFilter) ([]*domain.Order, error) {
	var (
		conds []string
		args  []any
	)
	if f.CustomerID != nil {
		args = append(args, *f.CustomerID)
		conds = append(conds, fmt.Sprintf("customer_id = $%d", len(args)))
	}
	if f.Status != nil {
		args = append(args, *f.Status)
		conds = append(conds, fmt.Sprintf("status = $%d", len(args)))
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
		SELECT id, customer_id, status, currency, total_amount_minor, shipping_address,
		       COALESCE(idempotency_key, ''), version, created_at, updated_at
		FROM orders %s
		ORDER BY created_at DESC
		LIMIT %s OFFSET %s`, where, limClause, offClause)

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list orders: %w", err)
	}
	defer rows.Close()

	var out []*domain.Order
	for rows.Next() {
		o, err := scanOrderRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Load items for each order.
	for _, o := range out {
		if err := s.loadItems(ctx, o); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Transition atomically applies a state change, records an audit event, and
// enqueues any follow-up jobs. It locks the order row, enforces the from-state
// (treating an already-applied transition as an idempotent no-op), validates
// the transition against the state machine, and bumps the version.
func (s *OrderStore) Transition(ctx context.Context, t domain.StateTransition) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	var (
		curStatus  domain.OrderStatus
		curVersion int
	)
	err = tx.QueryRow(ctx, `SELECT status, version FROM orders WHERE id = $1 FOR UPDATE`, t.OrderID).
		Scan(&curStatus, &curVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: order %s", domain.ErrNotFound, t.OrderID)
	}
	if err != nil {
		return fmt.Errorf("lock order: %w", err)
	}

	// Idempotency: the transition has already been applied.
	if curStatus == t.To {
		return tx.Commit(ctx)
	}
	if t.From != "" && curStatus != t.From {
		return fmt.Errorf("%w: expected order in %s but was %s", domain.ErrConflict, t.From, curStatus)
	}
	if t.ExpectVersion != nil && *t.ExpectVersion != curVersion {
		return fmt.Errorf("%w: order version %d != expected %d", domain.ErrConflict, curVersion, *t.ExpectVersion)
	}
	if err := curStatus.ValidateTransition(t.To); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `
		UPDATE orders SET status = $2, version = version + 1, updated_at = now() WHERE id = $1`,
		t.OrderID, t.To,
	); err != nil {
		return fmt.Errorf("update order status: %w", err)
	}

	detail, _ := json.Marshal(nz(t.Detail))
	event := t.Event
	if event == "" {
		event = "transition"
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO order_events (order_id, from_status, to_status, event, detail)
		VALUES ($1, $2, $3, $4, $5)`,
		t.OrderID, curStatus, t.To, event, detail,
	); err != nil {
		return fmt.Errorf("insert transition event: %w", err)
	}

	for i := range t.NextJobs {
		j := &t.NextJobs[i]
		if err := insertJob(ctx, tx, j); err != nil {
			// A duplicate active job is safe to ignore (the work is already queued).
			if errors.Is(err, domain.ErrConflict) {
				continue
			}
			return err
		}
	}

	return tx.Commit(ctx)
}

// Events returns the audit trail for an order, oldest first.
func (s *OrderStore) Events(ctx context.Context, orderID uuid.UUID) ([]domain.OrderEvent, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, order_id, COALESCE(from_status, ''), COALESCE(to_status, ''), event, detail, created_at
		FROM order_events WHERE order_id = $1 ORDER BY id ASC`, orderID)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()

	var out []domain.OrderEvent
	for rows.Next() {
		var (
			ev         domain.OrderEvent
			from, to   string
			detailJSON []byte
		)
		if err := rows.Scan(&ev.ID, &ev.OrderID, &from, &to, &ev.Event, &detailJSON, &ev.CreatedAt); err != nil {
			return nil, err
		}
		ev.FromStatus = domain.OrderStatus(from)
		ev.ToStatus = domain.OrderStatus(to)
		if len(detailJSON) > 0 {
			_ = json.Unmarshal(detailJSON, &ev.Detail)
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (s *OrderStore) loadItems(ctx context.Context, o *domain.Order) error {
	rows, err := s.pool.Query(ctx, `
		SELECT id, sku, name, quantity, unit_price_minor
		FROM order_items WHERE order_id = $1 ORDER BY id`, o.ID)
	if err != nil {
		return fmt.Errorf("load items: %w", err)
	}
	defer rows.Close()
	o.Items = nil
	for rows.Next() {
		var it domain.OrderItem
		if err := rows.Scan(&it.ID, &it.SKU, &it.Name, &it.Quantity, &it.UnitPrice); err != nil {
			return err
		}
		o.Items = append(o.Items, it)
	}
	return rows.Err()
}

// scanOrder runs a single-row order query with the given WHERE clause.
func scanOrder(ctx context.Context, q execer, where string, args ...any) (*domain.Order, error) {
	row := q.QueryRow(ctx, `
		SELECT id, customer_id, status, currency, total_amount_minor, shipping_address,
		       COALESCE(idempotency_key, ''), version, created_at, updated_at
		FROM orders `+where, args...)
	o, err := scanOrderRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: order", domain.ErrNotFound)
	}
	return o, err
}

// rowScanner is implemented by both pgx.Row and pgx.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanOrderRow(row rowScanner) (*domain.Order, error) {
	o := &domain.Order{}
	var addr []byte
	if err := row.Scan(
		&o.ID, &o.CustomerID, &o.Status, &o.Currency, &o.TotalAmount,
		&addr, &o.IdempotencyKey, &o.Version, &o.CreatedAt, &o.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if len(addr) > 0 {
		_ = json.Unmarshal(addr, &o.ShippingAddress)
	}
	return o, nil
}
