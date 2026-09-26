package domain

import "errors"

// Sentinel errors for the domain. Callers should wrap these with %w and compare
// with errors.Is so transport layers can map them to the right status codes.
var (
	// ErrNotFound is returned when a requested entity does not exist.
	ErrNotFound = errors.New("not found")

	// ErrConflict signals an optimistic-concurrency or uniqueness conflict.
	ErrConflict = errors.New("conflict")

	// ErrValidation signals invalid input.
	ErrValidation = errors.New("validation error")

	// ErrCurrencyMismatch is returned when combining amounts of different currencies.
	ErrCurrencyMismatch = errors.New("currency mismatch")

	// ErrInvalidTransition is returned for an illegal order state transition.
	ErrInvalidTransition = errors.New("invalid state transition")

	// ErrOrderNotCancellable is returned when an order cannot be cancelled from
	// its current state.
	ErrOrderNotCancellable = errors.New("order not cancellable")

	// ErrInsufficientInventory is returned when stock cannot cover a reservation.
	ErrInsufficientInventory = errors.New("insufficient inventory")

	// ErrPaymentDeclined is returned when a payment authorization is declined.
	ErrPaymentDeclined = errors.New("payment declined")

	// ErrRateLimited is returned when a caller exceeds an allowed request rate.
	ErrRateLimited = errors.New("rate limited")
)
