package domain

import (
	"time"

	"github.com/google/uuid"
)

// PaymentStatus is the state of a payment against an order.
type PaymentStatus string

const (
	PaymentAuthorized PaymentStatus = "authorized"
	PaymentCaptured   PaymentStatus = "captured"
	PaymentRefunded   PaymentStatus = "refunded"
	PaymentDeclined   PaymentStatus = "declined"
	PaymentFailed     PaymentStatus = "failed"
)

// Payment records the money movement for an order.
type Payment struct {
	ID          uuid.UUID     `json:"id"`
	OrderID     uuid.UUID     `json:"order_id"`
	Amount      int64         `json:"amount_minor"`
	Currency    string        `json:"currency"`
	Status      PaymentStatus `json:"status"`
	ProviderRef string        `json:"provider_ref,omitempty"` // reference from the (mock) payment provider
	// IdempotencyKey ensures a retried authorization does not double-charge.
	IdempotencyKey string    `json:"idempotency_key,omitempty"`
	FailureReason  string    `json:"failure_reason,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// AuthorizeRequest is the input to a payment authorization.
type AuthorizeRequest struct {
	OrderID        uuid.UUID
	Amount         int64
	Currency       string
	IdempotencyKey string
}
