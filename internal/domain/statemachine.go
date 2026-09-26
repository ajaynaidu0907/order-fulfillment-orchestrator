package domain

import "fmt"

// OrderStatus is a state in the order fulfillment workflow.
type OrderStatus string

// The forward-flow states plus the two terminal failure states.
const (
	StatusCreated             OrderStatus = "CREATED"
	StatusInventoryReserved   OrderStatus = "INVENTORY_RESERVED"
	StatusPaymentAuthorized   OrderStatus = "PAYMENT_AUTHORIZED"
	StatusReadyForFulfillment OrderStatus = "READY_FOR_FULFILLMENT"
	StatusShipped             OrderStatus = "SHIPPED"
	StatusDelivered           OrderStatus = "DELIVERED"

	// StatusCancelled is reached when an order is cancelled and any required
	// compensations (inventory release, payment refund) have completed.
	StatusCancelled OrderStatus = "CANCELLED"
	// StatusFailed is reached when the workflow exhausts retries and cannot
	// proceed, after compensations have run.
	StatusFailed OrderStatus = "FAILED"
)

// ForwardFlow is the canonical happy-path sequence of states.
var ForwardFlow = []OrderStatus{
	StatusCreated,
	StatusInventoryReserved,
	StatusPaymentAuthorized,
	StatusReadyForFulfillment,
	StatusShipped,
	StatusDelivered,
}

// validTransitions maps each state to the states it may move to.
var validTransitions = map[OrderStatus]map[OrderStatus]bool{
	StatusCreated: {
		StatusInventoryReserved: true,
		StatusCancelled:         true,
		StatusFailed:            true,
	},
	StatusInventoryReserved: {
		StatusPaymentAuthorized: true,
		StatusCancelled:         true,
		StatusFailed:            true,
	},
	StatusPaymentAuthorized: {
		StatusReadyForFulfillment: true,
		StatusCancelled:           true,
		StatusFailed:              true,
	},
	StatusReadyForFulfillment: {
		StatusShipped:   true,
		StatusCancelled: true,
		StatusFailed:    true,
	},
	StatusShipped: {
		StatusDelivered: true,
		StatusFailed:    true,
	},
	StatusDelivered: {},
	StatusCancelled: {},
	StatusFailed:    {},
}

// IsValid reports whether s is a recognised status.
func (s OrderStatus) IsValid() bool {
	_, ok := validTransitions[s]
	return ok
}

// IsTerminal reports whether no further transitions are possible from s.
func (s OrderStatus) IsTerminal() bool {
	return len(validTransitions[s]) == 0
}

// CanTransitionTo reports whether moving from s to next is a legal transition.
func (s OrderStatus) CanTransitionTo(next OrderStatus) bool {
	return validTransitions[s][next]
}

// ValidateTransition returns ErrInvalidTransition if s -> next is not allowed.
func (s OrderStatus) ValidateTransition(next OrderStatus) error {
	if !s.IsValid() {
		return fmt.Errorf("%w: unknown source status %q", ErrInvalidTransition, s)
	}
	if !s.CanTransitionTo(next) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, s, next)
	}
	return nil
}

// CompensationAction is a corrective step that must run to unwind partial work
// when an order is cancelled or fails.
type CompensationAction string

const (
	// CompReleaseInventory returns previously reserved stock to available.
	CompReleaseInventory CompensationAction = "release_inventory"
	// CompRefundPayment reverses a previously authorized/captured payment.
	CompRefundPayment CompensationAction = "refund_payment"
)

// CompensationsForCancel returns the ordered compensations required to safely
// cancel an order that is currently in status s. The order matters: money is
// returned before stock is released so a caller can reason about partial
// failures. Returns ErrOrderNotCancellable for states that cannot be cancelled
// (already shipped, delivered, or terminal).
func CompensationsForCancel(s OrderStatus) ([]CompensationAction, error) {
	switch s {
	case StatusCreated:
		// Nothing reserved or charged yet.
		return nil, nil
	case StatusInventoryReserved:
		return []CompensationAction{CompReleaseInventory}, nil
	case StatusPaymentAuthorized, StatusReadyForFulfillment:
		return []CompensationAction{CompRefundPayment, CompReleaseInventory}, nil
	case StatusShipped, StatusDelivered:
		return nil, fmt.Errorf("%w: order already %s (use a returns flow)", ErrOrderNotCancellable, s)
	case StatusCancelled, StatusFailed:
		return nil, fmt.Errorf("%w: order already %s", ErrOrderNotCancellable, s)
	default:
		return nil, fmt.Errorf("%w: unknown status %q", ErrOrderNotCancellable, s)
	}
}
