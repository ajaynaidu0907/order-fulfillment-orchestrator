// Package orchestrator implements the saga engine that drives an order through
// its fulfillment workflow. It is pure logic with no polling: the worker claims
// a job and hands it to Handle (a forward step) or Compensate (a rollback).
//
// Every forward step performs its side effect via a domain service and then, in
// a single OrderStore.Transition call (the transactional outbox), advances the
// order's state and enqueues the next job. Because Transition treats an
// already-applied transition as a no-op and every service is idempotent per
// order, re-running a claimed-but-not-completed job (recovered by ReapStale) is
// safe.
package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/domain"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/observability"
)

// confirmDeliveryDelay is how long after shipping the confirm_delivery job is
// scheduled to run, simulating carrier transit time.
const confirmDeliveryDelay = 5 * time.Second

// Deps are the dependencies of the orchestrator, matching the constructor
// contract in docs/ARCHITECTURE.md.
type Deps struct {
	Orders      domain.OrderStore
	Jobs        domain.JobQueue
	Payment     domain.PaymentService
	Inventory   domain.InventoryService
	Fulfillment domain.FulfillmentService
	Metrics     *observability.Metrics
	Logger      *slog.Logger
}

// Orchestrator executes a single workflow job (forward step or compensation).
type Orchestrator struct {
	orders      domain.OrderStore
	jobs        domain.JobQueue
	payment     domain.PaymentService
	inventory   domain.InventoryService
	fulfillment domain.FulfillmentService
	metrics     *observability.Metrics
	logger      *slog.Logger
}

// New builds an Orchestrator from its dependencies.
func New(d Deps) *Orchestrator {
	if d.Metrics == nil {
		d.Metrics = observability.NewMetrics()
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Orchestrator{
		orders:      d.Orders,
		jobs:        d.Jobs,
		payment:     d.Payment,
		inventory:   d.Inventory,
		fulfillment: d.Fulfillment,
		metrics:     d.Metrics,
		logger:      d.Logger,
	}
}

// Handle executes exactly one job and is idempotent. Forward steps perform their
// operation and then advance the order state while enqueueing the next job in a
// single transaction; compensation steps run their corrective action without a
// state transition (the order is already FAILED/CANCELLED).
func (o *Orchestrator) Handle(ctx context.Context, j domain.Job) error {
	orderID, err := jobOrderID(j)
	if err != nil {
		return err
	}

	switch j.Type {
	case domain.JobReserveInventory:
		return o.reserveInventory(ctx, orderID)
	case domain.JobAuthorizePayment:
		return o.authorizePayment(ctx, orderID)
	case domain.JobPrepareFulfillment:
		return o.prepareFulfillment(ctx, orderID)
	case domain.JobShipOrder:
		return o.shipOrder(ctx, orderID)
	case domain.JobConfirmDelivery:
		return o.confirmDelivery(ctx, orderID)
	case domain.JobReleaseInventory:
		return o.releaseInventory(ctx, orderID)
	case domain.JobRefundPayment:
		return o.refundPayment(ctx, orderID)
	default:
		return fmt.Errorf("orchestrator: unknown job type %q", j.Type)
	}
}

// Compensate is invoked by the worker when a job has exhausted its retries. It
// looks up the order's current status, derives the required compensations via
// domain.CompensationsForCancel, and transitions the order to FAILED while
// enqueueing the compensation jobs in the same transaction. If the order is no
// longer in a cancellable state (already shipped, delivered, or terminal) there
// is nothing to unwind and it returns nil.
func (o *Orchestrator) Compensate(ctx context.Context, j domain.Job, cause string) error {
	orderID, err := jobOrderID(j)
	if err != nil {
		return err
	}
	order, err := o.orders.Get(ctx, orderID)
	if err != nil {
		return fmt.Errorf("compensate: load order: %w", err)
	}

	comps, err := domain.CompensationsForCancel(order.Status)
	if err != nil {
		// Order already terminal or past the point of no return: nothing to do.
		o.logger.Info("compensation skipped: order not cancellable",
			"order_id", orderID, "status", order.Status, "cause", cause)
		return nil
	}

	nextJobs := make([]domain.Job, 0, len(comps))
	for _, c := range comps {
		jt, ok := compJobType(c)
		if !ok {
			continue
		}
		cj, err := domain.NewOrderJob(jt, orderID, time.Now())
		if err != nil {
			return fmt.Errorf("compensate: build %s job: %w", jt, err)
		}
		nextJobs = append(nextJobs, cj)
	}

	t := domain.StateTransition{
		OrderID: orderID,
		From:    order.Status,
		To:      domain.StatusFailed,
		Event:   "workflow_failed",
		Detail: map[string]any{
			"cause":      cause,
			"failed_job": string(j.Type),
		},
		NextJobs: nextJobs,
	}
	if err := o.orders.Transition(ctx, t); err != nil {
		return fmt.Errorf("compensate: transition %s->%s: %w", order.Status, domain.StatusFailed, err)
	}
	o.metrics.OrderTransitions.WithLabelValues(string(order.Status), string(domain.StatusFailed)).Inc()
	o.logger.Warn("workflow failed: compensations enqueued",
		"order_id", orderID, "from", order.Status, "cause", cause, "compensations", len(nextJobs))
	return nil
}

// ---- Forward steps -------------------------------------------------------

func (o *Orchestrator) reserveInventory(ctx context.Context, orderID uuid.UUID) error {
	order, err := o.orders.Get(ctx, orderID)
	if err != nil {
		return fmt.Errorf("reserve_inventory: load order: %w", err)
	}
	if err := o.inventory.Reserve(ctx, order.ID, order.Items); err != nil {
		return fmt.Errorf("reserve_inventory: %w", err)
	}
	next, err := domain.NewOrderJob(domain.JobAuthorizePayment, order.ID, time.Now())
	if err != nil {
		return err
	}
	return o.forward(ctx, order.ID, domain.StatusCreated, domain.StatusInventoryReserved,
		"inventory_reserved", map[string]any{"items": len(order.Items)}, next)
}

func (o *Orchestrator) authorizePayment(ctx context.Context, orderID uuid.UUID) error {
	order, err := o.orders.Get(ctx, orderID)
	if err != nil {
		return fmt.Errorf("authorize_payment: load order: %w", err)
	}
	_, err = o.payment.Authorize(ctx, domain.AuthorizeRequest{
		OrderID:        order.ID,
		Amount:         order.TotalAmount,
		Currency:       order.Currency,
		IdempotencyKey: "pay:" + order.ID.String(),
	})
	if err != nil {
		return fmt.Errorf("authorize_payment: %w", err)
	}
	next, err := domain.NewOrderJob(domain.JobPrepareFulfillment, order.ID, time.Now())
	if err != nil {
		return err
	}
	return o.forward(ctx, order.ID, domain.StatusInventoryReserved, domain.StatusPaymentAuthorized,
		"payment_authorized", map[string]any{"amount_minor": order.TotalAmount, "currency": order.Currency}, next)
}

func (o *Orchestrator) prepareFulfillment(ctx context.Context, orderID uuid.UUID) error {
	if _, err := o.fulfillment.Prepare(ctx, orderID); err != nil {
		return fmt.Errorf("prepare_fulfillment: %w", err)
	}
	next, err := domain.NewOrderJob(domain.JobShipOrder, orderID, time.Now())
	if err != nil {
		return err
	}
	return o.forward(ctx, orderID, domain.StatusPaymentAuthorized, domain.StatusReadyForFulfillment,
		"fulfillment_prepared", map[string]any{}, next)
}

func (o *Orchestrator) shipOrder(ctx context.Context, orderID uuid.UUID) error {
	shipment, err := o.fulfillment.Ship(ctx, orderID)
	if err != nil {
		return fmt.Errorf("ship_order: %w", err)
	}
	// Consume the reserved stock now that the goods have left the warehouse.
	if err := o.inventory.Commit(ctx, orderID); err != nil {
		return fmt.Errorf("ship_order: commit inventory: %w", err)
	}
	next, err := domain.NewOrderJob(domain.JobConfirmDelivery, orderID, time.Now().Add(confirmDeliveryDelay))
	if err != nil {
		return err
	}
	detail := map[string]any{}
	if shipment != nil {
		detail["carrier"] = shipment.Carrier
		detail["tracking_number"] = shipment.TrackingNumber
	}
	return o.forward(ctx, orderID, domain.StatusReadyForFulfillment, domain.StatusShipped,
		"order_shipped", detail, next)
}

func (o *Orchestrator) confirmDelivery(ctx context.Context, orderID uuid.UUID) error {
	if _, err := o.fulfillment.ConfirmDelivery(ctx, orderID); err != nil {
		return fmt.Errorf("confirm_delivery: %w", err)
	}
	// Terminal state: no follow-up job.
	return o.forward(ctx, orderID, domain.StatusShipped, domain.StatusDelivered,
		"order_delivered", map[string]any{})
}

// ---- Compensation steps --------------------------------------------------

func (o *Orchestrator) releaseInventory(ctx context.Context, orderID uuid.UUID) error {
	err := o.inventory.Release(ctx, orderID)
	o.metrics.Compensations.WithLabelValues(string(domain.CompReleaseInventory), result(err)).Inc()
	if err != nil {
		return fmt.Errorf("release_inventory: %w", err)
	}
	o.logger.Info("compensation applied: inventory released", "order_id", orderID)
	return nil
}

func (o *Orchestrator) refundPayment(ctx context.Context, orderID uuid.UUID) error {
	_, err := o.payment.Refund(ctx, orderID)
	o.metrics.Compensations.WithLabelValues(string(domain.CompRefundPayment), result(err)).Inc()
	if err != nil {
		return fmt.Errorf("refund_payment: %w", err)
	}
	o.logger.Info("compensation applied: payment refunded", "order_id", orderID)
	return nil
}

// ---- Helpers -------------------------------------------------------------

// forward applies a successful forward transition and records the metric. It
// tolerates Transition's idempotent no-op (returned as a nil error) so a
// re-executed job does not fail.
func (o *Orchestrator) forward(ctx context.Context, orderID uuid.UUID, from, to domain.OrderStatus, event string, detail map[string]any, nextJobs ...domain.Job) error {
	t := domain.StateTransition{
		OrderID:  orderID,
		From:     from,
		To:       to,
		Event:    event,
		Detail:   detail,
		NextJobs: nextJobs,
	}
	if err := o.orders.Transition(ctx, t); err != nil {
		return fmt.Errorf("transition %s->%s: %w", from, to, err)
	}
	o.metrics.OrderTransitions.WithLabelValues(string(from), string(to)).Inc()
	return nil
}

// compJobType maps a compensation action to its job type.
func compJobType(c domain.CompensationAction) (domain.JobType, bool) {
	switch c {
	case domain.CompReleaseInventory:
		return domain.JobReleaseInventory, true
	case domain.CompRefundPayment:
		return domain.JobRefundPayment, true
	default:
		return "", false
	}
}

// jobOrderID resolves the order id a job operates on, preferring the job's
// OrderID column and falling back to the JSON payload.
func jobOrderID(j domain.Job) (uuid.UUID, error) {
	if j.OrderID != nil && *j.OrderID != uuid.Nil {
		return *j.OrderID, nil
	}
	if len(j.Payload) > 0 {
		var p domain.JobPayload
		if err := json.Unmarshal(j.Payload, &p); err == nil && p.OrderID != uuid.Nil {
			return p.OrderID, nil
		}
	}
	return uuid.Nil, fmt.Errorf("job %s (%s) has no order id", j.ID, j.Type)
}

// result renders a metric result label from an error.
func result(err error) string {
	if err != nil {
		return "error"
	}
	return "success"
}
