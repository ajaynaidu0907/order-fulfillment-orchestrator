package api

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/domain"
)

// fakeOrderStore is an in-memory domain.OrderStore for tests (no Postgres).
type fakeOrderStore struct {
	mu       sync.Mutex
	orders   map[uuid.UUID]*domain.Order
	byKey    map[string]uuid.UUID
	events   map[uuid.UUID][]domain.OrderEvent
	nextEvID int64

	createErr error // when set, Create returns it
}

func newFakeOrderStore() *fakeOrderStore {
	return &fakeOrderStore{
		orders: make(map[uuid.UUID]*domain.Order),
		byKey:  make(map[string]uuid.UUID),
		events: make(map[uuid.UUID][]domain.OrderEvent),
	}
}

var _ domain.OrderStore = (*fakeOrderStore)(nil)

func (s *fakeOrderStore) Create(ctx context.Context, o *domain.Order, firstJob *domain.Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return s.createErr
	}
	if o.ID == uuid.Nil {
		o.ID = uuid.New()
	}
	if o.IdempotencyKey != "" {
		if _, ok := s.byKey[o.IdempotencyKey]; ok {
			return fmt.Errorf("%w: duplicate idempotency key", domain.ErrConflict)
		}
	}
	now := time.Now()
	o.CreatedAt = now
	o.UpdatedAt = now
	o.Version = 0
	stored := *o
	s.orders[o.ID] = &stored
	if o.IdempotencyKey != "" {
		s.byKey[o.IdempotencyKey] = o.ID
	}
	s.appendEvent(o.ID, "", o.Status, "order_created")
	return nil
}

func (s *fakeOrderStore) Get(ctx context.Context, id uuid.UUID) (*domain.Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.orders[id]
	if !ok {
		return nil, fmt.Errorf("%w: order %s", domain.ErrNotFound, id)
	}
	cp := *o
	return &cp, nil
}

func (s *fakeOrderStore) GetByIdempotencyKey(ctx context.Context, key string) (*domain.Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byKey[key]
	if !ok {
		return nil, fmt.Errorf("%w: order for key %s", domain.ErrNotFound, key)
	}
	cp := *s.orders[id]
	return &cp, nil
}

func (s *fakeOrderStore) List(ctx context.Context, f domain.OrderFilter) ([]*domain.Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var all []*domain.Order
	for _, o := range s.orders {
		if f.CustomerID != nil && o.CustomerID != *f.CustomerID {
			continue
		}
		if f.Status != nil && o.Status != *f.Status {
			continue
		}
		cp := *o
		all = append(all, &cp)
	}
	// Apply offset/limit deterministically enough for tests.
	if f.Offset > 0 {
		if f.Offset >= len(all) {
			return nil, nil
		}
		all = all[f.Offset:]
	}
	if f.Limit > 0 && len(all) > f.Limit {
		all = all[:f.Limit]
	}
	return all, nil
}

func (s *fakeOrderStore) Transition(ctx context.Context, t domain.StateTransition) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.orders[t.OrderID]
	if !ok {
		return fmt.Errorf("%w: order %s", domain.ErrNotFound, t.OrderID)
	}
	if o.Status == t.To {
		return nil // idempotent no-op
	}
	if t.From != "" && o.Status != t.From {
		return fmt.Errorf("%w: expected %s but was %s", domain.ErrConflict, t.From, o.Status)
	}
	if err := o.Status.ValidateTransition(t.To); err != nil {
		return err
	}
	from := o.Status
	o.Status = t.To
	o.Version++
	o.UpdatedAt = time.Now()
	s.appendEvent(o.ID, from, t.To, t.Event)
	return nil
}

func (s *fakeOrderStore) Events(ctx context.Context, orderID uuid.UUID) ([]domain.OrderEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.OrderEvent(nil), s.events[orderID]...), nil
}

// setStatus forces an order into a status directly (test helper), bypassing the
// state machine so cancellation paths can be exercised.
func (s *fakeOrderStore) setStatus(id uuid.UUID, status domain.OrderStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o, ok := s.orders[id]; ok {
		o.Status = status
	}
}

func (s *fakeOrderStore) appendEvent(orderID uuid.UUID, from, to domain.OrderStatus, event string) {
	s.nextEvID++
	s.events[orderID] = append(s.events[orderID], domain.OrderEvent{
		ID:         s.nextEvID,
		OrderID:    orderID,
		FromStatus: from,
		ToStatus:   to,
		Event:      event,
		CreatedAt:  time.Now(),
	})
}

// fakeInventory is an in-memory domain.InventoryService for tests.
type fakeInventory struct {
	mu       sync.Mutex
	products map[string]domain.Product
	released []uuid.UUID

	releaseErr error
}

func newFakeInventory() *fakeInventory {
	return &fakeInventory{products: make(map[string]domain.Product)}
}

var _ domain.InventoryService = (*fakeInventory)(nil)

func (f *fakeInventory) addProduct(p domain.Product) { f.products[p.SKU] = p }

func (f *fakeInventory) Reserve(ctx context.Context, orderID uuid.UUID, items []domain.OrderItem) error {
	return nil
}

func (f *fakeInventory) Release(ctx context.Context, orderID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.releaseErr != nil {
		return f.releaseErr
	}
	f.released = append(f.released, orderID)
	return nil
}

func (f *fakeInventory) Commit(ctx context.Context, orderID uuid.UUID) error { return nil }

func (f *fakeInventory) GetProduct(ctx context.Context, sku string) (*domain.Product, error) {
	p, ok := f.products[sku]
	if !ok {
		return nil, fmt.Errorf("%w: product %s", domain.ErrNotFound, sku)
	}
	cp := p
	return &cp, nil
}

func (f *fakeInventory) ListProducts(ctx context.Context, filter domain.ProductFilter) ([]domain.Product, error) {
	var out []domain.Product
	for _, p := range f.products {
		if filter.Category != "" && p.Category != filter.Category {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// fakePayment is an in-memory domain.PaymentService for tests.
type fakePayment struct {
	mu        sync.Mutex
	refunded  []uuid.UUID
	refundErr error
}

var _ domain.PaymentService = (*fakePayment)(nil)

func (f *fakePayment) Authorize(ctx context.Context, req domain.AuthorizeRequest) (*domain.Payment, error) {
	return &domain.Payment{OrderID: req.OrderID, Status: domain.PaymentAuthorized}, nil
}

func (f *fakePayment) Capture(ctx context.Context, orderID uuid.UUID) (*domain.Payment, error) {
	return &domain.Payment{OrderID: orderID, Status: domain.PaymentCaptured}, nil
}

func (f *fakePayment) Refund(ctx context.Context, orderID uuid.UUID) (*domain.Payment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refundErr != nil {
		return nil, f.refundErr
	}
	f.refunded = append(f.refunded, orderID)
	return &domain.Payment{OrderID: orderID, Status: domain.PaymentRefunded}, nil
}

func (f *fakePayment) Get(ctx context.Context, orderID uuid.UUID) (*domain.Payment, error) {
	return &domain.Payment{OrderID: orderID}, nil
}

// fakeFulfillment is an in-memory domain.FulfillmentService for tests.
type fakeFulfillment struct{}

var _ domain.FulfillmentService = (*fakeFulfillment)(nil)

func (f *fakeFulfillment) Prepare(ctx context.Context, orderID uuid.UUID) (*domain.Shipment, error) {
	return &domain.Shipment{OrderID: orderID, Status: domain.ShipmentPending}, nil
}

func (f *fakeFulfillment) Ship(ctx context.Context, orderID uuid.UUID) (*domain.Shipment, error) {
	return &domain.Shipment{OrderID: orderID, Status: domain.ShipmentDispatched}, nil
}

func (f *fakeFulfillment) ConfirmDelivery(ctx context.Context, orderID uuid.UUID) (*domain.Shipment, error) {
	return &domain.Shipment{OrderID: orderID, Status: domain.ShipmentDelivered}, nil
}

func (f *fakeFulfillment) Get(ctx context.Context, orderID uuid.UUID) (*domain.Shipment, error) {
	return &domain.Shipment{OrderID: orderID}, nil
}

// fakeRateLimiter is an in-memory domain.RateLimiter for tests.
type fakeRateLimiter struct {
	allow bool
	err   error
}

var _ domain.RateLimiter = (*fakeRateLimiter)(nil)

func (f *fakeRateLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	return f.allow, f.err
}
