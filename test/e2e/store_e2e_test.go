package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/domain"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/mockdata"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/store"
)

// insertCustomer inserts one synthetic customer so orders can satisfy their
// foreign key, and returns its ID. A nanosecond seed keeps emails unique.
func insertCustomer(t *testing.T, ctx context.Context, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	c := mockdata.Customers(1, time.Now().UnixNano())[0]
	addr, err := json.Marshal(c.Address)
	if err != nil {
		t.Fatalf("marshal address: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO customers (id, name, email, phone, address)
		VALUES ($1, $2, $3, $4, $5)`,
		c.ID, c.Name, c.Email, c.Phone, addr,
	); err != nil {
		t.Fatalf("insert customer: %v", err)
	}
	return c.ID
}

// newOrder builds an in-memory order (with computed total and a fresh ID) plus its
// initial reserve_inventory job.
func newOrder(t *testing.T, customerID uuid.UUID) (*domain.Order, domain.Job) {
	t.Helper()
	order := &domain.Order{
		ID:         uuid.New(),
		CustomerID: customerID,
		Status:     domain.StatusCreated,
		Currency:   "USD",
		Items: []domain.OrderItem{
			{SKU: "ELEC-0001", Name: "Wireless Noise-Cancelling Headphones", Quantity: 1, UnitPrice: 19999},
			{SKU: "HOME-0001", Name: "Stainless Steel French Press 34oz", Quantity: 2, UnitPrice: 3999},
		},
		ShippingAddress: domain.Address{
			Line1: "742 Maple Ave", City: "Austin", State: "TX", PostalCode: "78701", Country: "US",
		},
	}
	order.TotalAmount = order.ComputeTotal()

	job, err := domain.NewOrderJob(domain.JobReserveInventory, order.ID, time.Now())
	if err != nil {
		t.Fatalf("build first job: %v", err)
	}
	return order, job
}

func jobStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) string {
	t.Helper()
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM jobs WHERE id = $1`, id).Scan(&status); err != nil {
		t.Fatalf("query job status: %v", err)
	}
	return status
}

func TestOrderStoreCreate(t *testing.T) {
	_, pool, cleanup := StartPostgres(t)
	defer cleanup()
	ctx := context.Background()

	customerID := insertCustomer(t, ctx, pool)
	orders := store.NewOrderStore(pool)
	order, firstJob := newOrder(t, customerID)

	if err := orders.Create(ctx, order, &firstJob); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The order and its items are persisted.
	got, err := orders.Get(ctx, order.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != domain.StatusCreated {
		t.Errorf("status = %q, want CREATED", got.Status)
	}
	if got.TotalAmount != 19999+2*3999 {
		t.Errorf("total = %d, want %d", got.TotalAmount, 19999+2*3999)
	}
	if len(got.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(got.Items))
	}

	// The initial audit event is written.
	events, err := orders.Events(ctx, order.ID)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 1 || events[0].Event != "order_created" {
		t.Errorf("events = %+v, want a single order_created event", events)
	}

	// The reserve_inventory job exists in the same commit, with the right key.
	var (
		jobType string
		idemKey string
		status  string
	)
	err = pool.QueryRow(ctx, `
		SELECT type, COALESCE(idempotency_key, ''), status
		FROM jobs WHERE order_id = $1`, order.ID).Scan(&jobType, &idemKey, &status)
	if err != nil {
		t.Fatalf("query job: %v", err)
	}
	if jobType != string(domain.JobReserveInventory) {
		t.Errorf("job type = %q, want reserve_inventory", jobType)
	}
	wantKey := string(domain.JobReserveInventory) + ":" + order.ID.String()
	if idemKey != wantKey {
		t.Errorf("job idempotency_key = %q, want %q", idemKey, wantKey)
	}
	if status != string(domain.JobPending) {
		t.Errorf("job status = %q, want pending", status)
	}
}

func TestOrderStoreTransition(t *testing.T) {
	_, pool, cleanup := StartPostgres(t)
	defer cleanup()
	ctx := context.Background()

	customerID := insertCustomer(t, ctx, pool)
	orders := store.NewOrderStore(pool)
	order, firstJob := newOrder(t, customerID)
	if err := orders.Create(ctx, order, &firstJob); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Advance CREATED -> INVENTORY_RESERVED and enqueue the next job atomically.
	nextJob, err := domain.NewOrderJob(domain.JobAuthorizePayment, order.ID, time.Now())
	if err != nil {
		t.Fatalf("build next job: %v", err)
	}
	err = orders.Transition(ctx, domain.StateTransition{
		OrderID:  order.ID,
		From:     domain.StatusCreated,
		To:       domain.StatusInventoryReserved,
		Event:    "inventory_reserved",
		NextJobs: []domain.Job{nextJob},
	})
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}

	got, err := orders.Get(ctx, order.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != domain.StatusInventoryReserved {
		t.Errorf("status = %q, want INVENTORY_RESERVED", got.Status)
	}
	if got.Version != 1 {
		t.Errorf("version = %d, want 1", got.Version)
	}

	// An order_event was recorded for the transition.
	events, err := orders.Events(ctx, order.ID)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2 (created + transition)", len(events))
	}
	last := events[len(events)-1]
	if last.ToStatus != domain.StatusInventoryReserved || last.Event != "inventory_reserved" {
		t.Errorf("last event = %+v, want inventory_reserved", last)
	}

	// The follow-up job was enqueued in the same transaction.
	var authCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs WHERE order_id = $1 AND type = $2`,
		order.ID, domain.JobAuthorizePayment).Scan(&authCount); err != nil {
		t.Fatalf("count auth jobs: %v", err)
	}
	if authCount != 1 {
		t.Errorf("authorize_payment jobs = %d, want 1", authCount)
	}

	// Re-applying the same transition is an idempotent no-op (version unchanged).
	err = orders.Transition(ctx, domain.StateTransition{
		OrderID: order.ID,
		From:    domain.StatusCreated,
		To:      domain.StatusInventoryReserved,
		Event:   "inventory_reserved",
	})
	if err != nil {
		t.Fatalf("idempotent Transition: %v", err)
	}
	again, err := orders.Get(ctx, order.ID)
	if err != nil {
		t.Fatalf("Get after no-op: %v", err)
	}
	if again.Version != 1 {
		t.Errorf("version after no-op = %d, want 1 (unchanged)", again.Version)
	}

	// A transition from the wrong from-state returns ErrConflict.
	err = orders.Transition(ctx, domain.StateTransition{
		OrderID: order.ID,
		From:    domain.StatusCreated, // actual state is INVENTORY_RESERVED
		To:      domain.StatusPaymentAuthorized,
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Errorf("wrong from-state error = %v, want ErrConflict", err)
	}
}

func TestJobQueueLifecycle(t *testing.T) {
	_, pool, cleanup := StartPostgres(t)
	defer cleanup()
	ctx := context.Background()

	q := store.NewJobQueue(pool, 30*time.Second)

	// Claim marks a job running, leases a lock, and increments attempts.
	claimTarget := &domain.Job{Type: domain.JobReserveInventory}
	if err := q.Enqueue(ctx, claimTarget); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	claimed, err := q.Claim(ctx, "worker-1", 10)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d jobs, want 1", len(claimed))
	}
	c := claimed[0]
	if c.Status != domain.JobRunning {
		t.Errorf("claimed status = %q, want running", c.Status)
	}
	if c.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", c.Attempts)
	}
	if c.LockedUntil == nil {
		t.Error("locked_until is nil, want a lease")
	}

	// Complete.
	if err := q.Complete(ctx, c.ID); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if s := jobStatus(t, ctx, pool, c.ID); s != string(domain.JobCompleted) {
		t.Errorf("status after Complete = %q, want completed", s)
	}

	// Retry reschedules to pending with the recorded cause.
	retryTarget := &domain.Job{Type: domain.JobAuthorizePayment}
	if err := q.Enqueue(ctx, retryTarget); err != nil {
		t.Fatalf("Enqueue retry target: %v", err)
	}
	rc, err := q.Claim(ctx, "worker-1", 1)
	if err != nil || len(rc) != 1 {
		t.Fatalf("Claim retry target: %v (n=%d)", err, len(rc))
	}
	future := time.Now().Add(2 * time.Minute)
	if err := q.Retry(ctx, rc[0].ID, "transient failure", future); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	var (
		rStatus string
		rErr    string
	)
	if err := pool.QueryRow(ctx, `SELECT status, COALESCE(last_error, '') FROM jobs WHERE id = $1`, rc[0].ID).
		Scan(&rStatus, &rErr); err != nil {
		t.Fatalf("query retried job: %v", err)
	}
	if rStatus != string(domain.JobPending) {
		t.Errorf("status after Retry = %q, want pending", rStatus)
	}
	if rErr != "transient failure" {
		t.Errorf("last_error = %q, want %q", rErr, "transient failure")
	}

	// Dead marks a job permanently failed.
	deadTarget := &domain.Job{Type: domain.JobPrepareFulfillment}
	if err := q.Enqueue(ctx, deadTarget); err != nil {
		t.Fatalf("Enqueue dead target: %v", err)
	}
	dc, err := q.Claim(ctx, "worker-1", 1)
	if err != nil || len(dc) != 1 {
		t.Fatalf("Claim dead target: %v (n=%d)", err, len(dc))
	}
	if err := q.Dead(ctx, dc[0].ID, "retries exhausted"); err != nil {
		t.Fatalf("Dead: %v", err)
	}
	if s := jobStatus(t, ctx, pool, dc[0].ID); s != string(domain.JobDead) {
		t.Errorf("status after Dead = %q, want dead", s)
	}

	// ReapStale returns running jobs with an expired lease to pending.
	reapTarget := &domain.Job{Type: domain.JobShipOrder}
	if err := q.Enqueue(ctx, reapTarget); err != nil {
		t.Fatalf("Enqueue reap target: %v", err)
	}
	pc, err := q.Claim(ctx, "worker-1", 1)
	if err != nil || len(pc) != 1 {
		t.Fatalf("Claim reap target: %v (n=%d)", err, len(pc))
	}
	// Force the lease to have expired (simulating a crashed worker).
	if _, err := pool.Exec(ctx, `UPDATE jobs SET locked_until = now() - interval '1 minute' WHERE id = $1`, pc[0].ID); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	n, err := q.ReapStale(ctx)
	if err != nil {
		t.Fatalf("ReapStale: %v", err)
	}
	if n < 1 {
		t.Errorf("ReapStale recovered %d jobs, want >= 1", n)
	}
	if s := jobStatus(t, ctx, pool, pc[0].ID); s != string(domain.JobPending) {
		t.Errorf("status after ReapStale = %q, want pending", s)
	}
}

func TestJobQueueConcurrentClaim(t *testing.T) {
	_, pool, cleanup := StartPostgres(t)
	defer cleanup()
	ctx := context.Background()

	q := store.NewJobQueue(pool, 30*time.Second)

	const total = 200
	for i := 0; i < total; i++ {
		if err := q.Enqueue(ctx, &domain.Job{Type: domain.JobReserveInventory}); err != nil {
			t.Fatalf("Enqueue %d: %v", i, err)
		}
	}

	const workers = 8
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		seen  = make(map[uuid.UUID]int)
		errCh = make(chan error, workers)
	)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			workerID := fmt.Sprintf("worker-%d", id)
			for {
				jobs, err := q.Claim(ctx, workerID, 5)
				if err != nil {
					errCh <- err
					return
				}
				if len(jobs) == 0 {
					return
				}
				mu.Lock()
				for _, j := range jobs {
					seen[j.ID]++
				}
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent Claim: %v", err)
	}

	// FOR UPDATE SKIP LOCKED must hand each job to exactly one worker.
	if len(seen) != total {
		t.Errorf("distinct jobs claimed = %d, want %d", len(seen), total)
	}
	for id, count := range seen {
		if count != 1 {
			t.Errorf("job %s claimed %d times, want exactly 1", id, count)
		}
	}
}
