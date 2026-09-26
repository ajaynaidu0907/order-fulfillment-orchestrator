package orchestrator

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/domain"
)

// ---- In-memory fakes -----------------------------------------------------

// fakeOrderStore is an in-memory domain.OrderStore that reproduces the real
// store's Transition semantics: idempotent no-op when already at the target
// state, a from-state guard, state-machine validation, and NextJobs capture.
type fakeOrderStore struct {
	mu       sync.Mutex
	orders   map[uuid.UUID]*domain.Order
	events   []domain.OrderEvent
	enqueued []domain.Job
	getErr   error
}

func newFakeOrderStore() *fakeOrderStore {
	return &fakeOrderStore{orders: map[uuid.UUID]*domain.Order{}}
}

func (f *fakeOrderStore) put(o *domain.Order) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.orders[o.ID] = o
}

func (f *fakeOrderStore) status(id uuid.UUID) domain.OrderStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.orders[id].Status
}

func (f *fakeOrderStore) enqueuedTypes() []domain.JobType {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.JobType, 0, len(f.enqueued))
	for _, j := range f.enqueued {
		out = append(out, j.Type)
	}
	return out
}

func (f *fakeOrderStore) Create(context.Context, *domain.Order, *domain.Job) error { return nil }

func (f *fakeOrderStore) Get(_ context.Context, id uuid.UUID) (*domain.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
	}
	o, ok := f.orders[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	cp := *o // return a shallow copy so callers can't mutate stored state
	return &cp, nil
}

func (f *fakeOrderStore) GetByIdempotencyKey(context.Context, string) (*domain.Order, error) {
	return nil, domain.ErrNotFound
}

func (f *fakeOrderStore) List(context.Context, domain.OrderFilter) ([]*domain.Order, error) {
	return nil, nil
}

func (f *fakeOrderStore) Transition(_ context.Context, t domain.StateTransition) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.orders[t.OrderID]
	if !ok {
		return domain.ErrNotFound
	}
	// Idempotency: already applied.
	if o.Status == t.To {
		return nil
	}
	if t.From != "" && o.Status != t.From {
		return domain.ErrConflict
	}
	if err := o.Status.ValidateTransition(t.To); err != nil {
		return err
	}
	f.events = append(f.events, domain.OrderEvent{
		OrderID: t.OrderID, FromStatus: o.Status, ToStatus: t.To, Event: t.Event, Detail: t.Detail,
	})
	o.Status = t.To
	o.Version++
	f.enqueued = append(f.enqueued, t.NextJobs...)
	return nil
}

func (f *fakeOrderStore) Events(context.Context, uuid.UUID) ([]domain.OrderEvent, error) {
	return f.events, nil
}

// fakeJobQueue satisfies domain.JobQueue; the orchestrator does not call it, so
// this is a minimal stand-in to satisfy Deps.
type fakeJobQueue struct{}

var _ domain.JobQueue = fakeJobQueue{}

func (fakeJobQueue) Enqueue(context.Context, *domain.Job) error { return nil }
func (fakeJobQueue) Claim(context.Context, string, int) ([]domain.Job, error) {
	return nil, nil
}
func (fakeJobQueue) Complete(context.Context, uuid.UUID) error                 { return nil }
func (fakeJobQueue) Retry(context.Context, uuid.UUID, string, time.Time) error { return nil }
func (fakeJobQueue) Dead(context.Context, uuid.UUID, string) error             { return nil }
func (fakeJobQueue) ReapStale(context.Context) (int, error)                    { return 0, nil }

type fakePayment struct {
	authorizeErr error
	refundErr    error
	authorized   int
	refunded     int
}

func (p *fakePayment) Authorize(_ context.Context, req domain.AuthorizeRequest) (*domain.Payment, error) {
	p.authorized++
	if p.authorizeErr != nil {
		return nil, p.authorizeErr
	}
	return &domain.Payment{OrderID: req.OrderID, Amount: req.Amount, Currency: req.Currency, Status: domain.PaymentAuthorized}, nil
}
func (p *fakePayment) Capture(_ context.Context, orderID uuid.UUID) (*domain.Payment, error) {
	return &domain.Payment{OrderID: orderID, Status: domain.PaymentCaptured}, nil
}
func (p *fakePayment) Refund(_ context.Context, orderID uuid.UUID) (*domain.Payment, error) {
	p.refunded++
	if p.refundErr != nil {
		return nil, p.refundErr
	}
	return &domain.Payment{OrderID: orderID, Status: domain.PaymentRefunded}, nil
}
func (p *fakePayment) Get(_ context.Context, orderID uuid.UUID) (*domain.Payment, error) {
	return &domain.Payment{OrderID: orderID}, nil
}

type fakeInventory struct {
	reserveErr error
	releaseErr error
	commitErr  error
	reserved   int
	released   int
	committed  int
}

func (i *fakeInventory) Reserve(_ context.Context, _ uuid.UUID, _ []domain.OrderItem) error {
	i.reserved++
	return i.reserveErr
}
func (i *fakeInventory) Release(_ context.Context, _ uuid.UUID) error {
	i.released++
	return i.releaseErr
}
func (i *fakeInventory) Commit(_ context.Context, _ uuid.UUID) error {
	i.committed++
	return i.commitErr
}
func (i *fakeInventory) GetProduct(context.Context, string) (*domain.Product, error) {
	return nil, domain.ErrNotFound
}
func (i *fakeInventory) ListProducts(context.Context, domain.ProductFilter) ([]domain.Product, error) {
	return nil, nil
}

type fakeFulfillment struct {
	prepareErr error
	shipErr    error
	confirmErr error
	prepared   int
	shipped    int
	confirmed  int
}

func (f *fakeFulfillment) Prepare(_ context.Context, orderID uuid.UUID) (*domain.Shipment, error) {
	f.prepared++
	if f.prepareErr != nil {
		return nil, f.prepareErr
	}
	return &domain.Shipment{OrderID: orderID, Status: domain.ShipmentPending}, nil
}
func (f *fakeFulfillment) Ship(_ context.Context, orderID uuid.UUID) (*domain.Shipment, error) {
	f.shipped++
	if f.shipErr != nil {
		return nil, f.shipErr
	}
	return &domain.Shipment{OrderID: orderID, Carrier: "ACME", TrackingNumber: "TRK123", Status: domain.ShipmentDispatched}, nil
}
func (f *fakeFulfillment) ConfirmDelivery(_ context.Context, orderID uuid.UUID) (*domain.Shipment, error) {
	f.confirmed++
	if f.confirmErr != nil {
		return nil, f.confirmErr
	}
	return &domain.Shipment{OrderID: orderID, Status: domain.ShipmentDelivered}, nil
}
func (f *fakeFulfillment) Get(_ context.Context, orderID uuid.UUID) (*domain.Shipment, error) {
	return &domain.Shipment{OrderID: orderID}, nil
}

// ---- Test helpers --------------------------------------------------------

func newOrder(status domain.OrderStatus) *domain.Order {
	return &domain.Order{
		ID:          uuid.New(),
		CustomerID:  uuid.New(),
		Status:      status,
		Currency:    "USD",
		TotalAmount: 4200,
		Items: []domain.OrderItem{
			{ID: uuid.New(), SKU: "SKU-1", Name: "Widget", Quantity: 2, UnitPrice: 2100},
		},
	}
}

func mustJob(t *testing.T, jt domain.JobType, orderID uuid.UUID) domain.Job {
	t.Helper()
	j, err := domain.NewOrderJob(jt, orderID, time.Now())
	if err != nil {
		t.Fatalf("NewOrderJob(%s): %v", jt, err)
	}
	return j
}

// ---- Tests ---------------------------------------------------------------

func TestOrchestrator_HappyPath(t *testing.T) {
	store := newFakeOrderStore()
	pay := &fakePayment{}
	inv := &fakeInventory{}
	ful := &fakeFulfillment{}
	o := New(Deps{Orders: store, Jobs: fakeJobQueue{}, Payment: pay, Inventory: inv, Fulfillment: ful})

	order := newOrder(domain.StatusCreated)
	store.put(order)

	ctx := context.Background()

	steps := []struct {
		job      domain.JobType
		wantTo   domain.OrderStatus
		wantNext domain.JobType // "" means no next job expected
	}{
		{domain.JobReserveInventory, domain.StatusInventoryReserved, domain.JobAuthorizePayment},
		{domain.JobAuthorizePayment, domain.StatusPaymentAuthorized, domain.JobPrepareFulfillment},
		{domain.JobPrepareFulfillment, domain.StatusReadyForFulfillment, domain.JobShipOrder},
		{domain.JobShipOrder, domain.StatusShipped, domain.JobConfirmDelivery},
		{domain.JobConfirmDelivery, domain.StatusDelivered, ""},
	}

	for _, s := range steps {
		before := len(store.enqueuedTypes())
		if err := o.Handle(ctx, mustJob(t, s.job, order.ID)); err != nil {
			t.Fatalf("Handle(%s): unexpected error: %v", s.job, err)
		}
		if got := store.status(order.ID); got != s.wantTo {
			t.Fatalf("after %s: status = %s, want %s", s.job, got, s.wantTo)
		}
		enq := store.enqueuedTypes()
		if s.wantNext == "" {
			if len(enq) != before {
				t.Fatalf("after %s: expected no new job, got %v", s.job, enq[before:])
			}
			continue
		}
		if len(enq) != before+1 {
			t.Fatalf("after %s: expected 1 new job, got %d new (%v)", s.job, len(enq)-before, enq)
		}
		if enq[before] != s.wantNext {
			t.Fatalf("after %s: next job = %s, want %s", s.job, enq[before], s.wantNext)
		}
	}

	if store.status(order.ID) != domain.StatusDelivered {
		t.Fatalf("final status = %s, want DELIVERED", store.status(order.ID))
	}
	// Each forward service was called exactly once.
	if inv.reserved != 1 || pay.authorized != 1 || ful.prepared != 1 || ful.shipped != 1 || inv.committed != 1 || ful.confirmed != 1 {
		t.Fatalf("service call counts unexpected: reserve=%d auth=%d prepare=%d ship=%d commit=%d confirm=%d",
			inv.reserved, pay.authorized, ful.prepared, ful.shipped, inv.committed, ful.confirmed)
	}
}

func TestOrchestrator_HandleIsIdempotent(t *testing.T) {
	store := newFakeOrderStore()
	o := New(Deps{Orders: store, Jobs: fakeJobQueue{}, Payment: &fakePayment{}, Inventory: &fakeInventory{}, Fulfillment: &fakeFulfillment{}})

	order := newOrder(domain.StatusCreated)
	store.put(order)
	ctx := context.Background()

	// Run reserve twice; the second run is a Transition no-op and must not error.
	if err := o.Handle(ctx, mustJob(t, domain.JobReserveInventory, order.ID)); err != nil {
		t.Fatalf("first reserve: %v", err)
	}
	if err := o.Handle(ctx, mustJob(t, domain.JobReserveInventory, order.ID)); err != nil {
		t.Fatalf("second reserve (idempotent) should not error: %v", err)
	}
	if store.status(order.ID) != domain.StatusInventoryReserved {
		t.Fatalf("status = %s, want INVENTORY_RESERVED", store.status(order.ID))
	}
}

func TestOrchestrator_PaymentDeclined_ReturnsError(t *testing.T) {
	store := newFakeOrderStore()
	pay := &fakePayment{authorizeErr: domain.ErrPaymentDeclined}
	o := New(Deps{Orders: store, Jobs: fakeJobQueue{}, Payment: pay, Inventory: &fakeInventory{}, Fulfillment: &fakeFulfillment{}})

	order := newOrder(domain.StatusInventoryReserved)
	store.put(order)

	err := o.Handle(context.Background(), mustJob(t, domain.JobAuthorizePayment, order.ID))
	if !errors.Is(err, domain.ErrPaymentDeclined) {
		t.Fatalf("Handle error = %v, want wrap of ErrPaymentDeclined", err)
	}
	// State must not have advanced.
	if store.status(order.ID) != domain.StatusInventoryReserved {
		t.Fatalf("status = %s, want unchanged INVENTORY_RESERVED", store.status(order.ID))
	}
}

func TestOrchestrator_Compensate_EnqueuesReleaseInventory(t *testing.T) {
	store := newFakeOrderStore()
	o := New(Deps{Orders: store, Jobs: fakeJobQueue{}, Payment: &fakePayment{}, Inventory: &fakeInventory{}, Fulfillment: &fakeFulfillment{}})

	order := newOrder(domain.StatusInventoryReserved)
	store.put(order)

	failed := mustJob(t, domain.JobAuthorizePayment, order.ID)
	if err := o.Compensate(context.Background(), failed, "payment declined"); err != nil {
		t.Fatalf("Compensate: %v", err)
	}
	if store.status(order.ID) != domain.StatusFailed {
		t.Fatalf("status = %s, want FAILED", store.status(order.ID))
	}
	types := store.enqueuedTypes()
	if len(types) != 1 || types[0] != domain.JobReleaseInventory {
		t.Fatalf("enqueued comps = %v, want [release_inventory]", types)
	}
}

func TestOrchestrator_Compensate_RefundThenRelease(t *testing.T) {
	store := newFakeOrderStore()
	o := New(Deps{Orders: store, Jobs: fakeJobQueue{}, Payment: &fakePayment{}, Inventory: &fakeInventory{}, Fulfillment: &fakeFulfillment{}})

	order := newOrder(domain.StatusPaymentAuthorized)
	store.put(order)

	failed := mustJob(t, domain.JobPrepareFulfillment, order.ID)
	if err := o.Compensate(context.Background(), failed, "prepare failed"); err != nil {
		t.Fatalf("Compensate: %v", err)
	}
	if store.status(order.ID) != domain.StatusFailed {
		t.Fatalf("status = %s, want FAILED", store.status(order.ID))
	}
	types := store.enqueuedTypes()
	want := []domain.JobType{domain.JobRefundPayment, domain.JobReleaseInventory}
	if len(types) != len(want) {
		t.Fatalf("enqueued comps = %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("enqueued comps = %v, want %v", types, want)
		}
	}
}

func TestOrchestrator_Compensate_TerminalOrderIsNoop(t *testing.T) {
	store := newFakeOrderStore()
	o := New(Deps{Orders: store, Jobs: fakeJobQueue{}, Payment: &fakePayment{}, Inventory: &fakeInventory{}, Fulfillment: &fakeFulfillment{}})

	// SHIPPED is past the point of cancellation.
	order := newOrder(domain.StatusShipped)
	store.put(order)

	failed := mustJob(t, domain.JobConfirmDelivery, order.ID)
	if err := o.Compensate(context.Background(), failed, "boom"); err != nil {
		t.Fatalf("Compensate on non-cancellable order should be nil, got %v", err)
	}
	if store.status(order.ID) != domain.StatusShipped {
		t.Fatalf("status = %s, want unchanged SHIPPED", store.status(order.ID))
	}
	if len(store.enqueuedTypes()) != 0 {
		t.Fatalf("expected no comps enqueued, got %v", store.enqueuedTypes())
	}
}

func TestOrchestrator_CompensationHandlers(t *testing.T) {
	store := newFakeOrderStore()
	pay := &fakePayment{}
	inv := &fakeInventory{}
	o := New(Deps{Orders: store, Jobs: fakeJobQueue{}, Payment: pay, Inventory: inv, Fulfillment: &fakeFulfillment{}})
	ctx := context.Background()
	id := uuid.New()

	if err := o.Handle(ctx, mustJob(t, domain.JobReleaseInventory, id)); err != nil {
		t.Fatalf("release_inventory: %v", err)
	}
	if inv.released != 1 {
		t.Fatalf("Release called %d times, want 1", inv.released)
	}
	if err := o.Handle(ctx, mustJob(t, domain.JobRefundPayment, id)); err != nil {
		t.Fatalf("refund_payment: %v", err)
	}
	if pay.refunded != 1 {
		t.Fatalf("Refund called %d times, want 1", pay.refunded)
	}
}
