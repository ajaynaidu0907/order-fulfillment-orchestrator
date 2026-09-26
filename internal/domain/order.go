package domain

import (
	"time"

	"github.com/google/uuid"
)

// OrderItem is a single line on an order.
type OrderItem struct {
	ID        uuid.UUID `json:"id"`
	SKU       string    `json:"sku"`
	Name      string    `json:"name"`
	Quantity  int       `json:"quantity"`
	UnitPrice int64     `json:"unit_price_minor"` // minor units (cents)
}

// LineTotal is quantity * unit price, in minor units.
func (i OrderItem) LineTotal() int64 {
	return int64(i.Quantity) * i.UnitPrice
}

// Order is the aggregate root and the system of record for a purchase.
type Order struct {
	ID              uuid.UUID   `json:"id"`
	CustomerID      uuid.UUID   `json:"customer_id"`
	Status          OrderStatus `json:"status"`
	Currency        string      `json:"currency"`
	TotalAmount     int64       `json:"total_amount_minor"` // minor units (cents)
	Items           []OrderItem `json:"items"`
	ShippingAddress Address     `json:"shipping_address"`
	// IdempotencyKey de-duplicates create requests; unique per order when set.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	// Version supports optimistic concurrency control on state transitions.
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ComputeTotal sums all line totals. It does not mutate the order.
func (o *Order) ComputeTotal() int64 {
	var total int64
	for _, it := range o.Items {
		total += it.LineTotal()
	}
	return total
}

// Total returns the order total as a Money value.
func (o *Order) Total() Money {
	return NewMoney(o.TotalAmount, o.Currency)
}

// OrderFilter narrows an order listing query.
type OrderFilter struct {
	CustomerID *uuid.UUID
	Status     *OrderStatus
	Limit      int
	Offset     int
}

// OrderEvent is an immutable audit record of a state transition or a
// significant lifecycle event on an order.
type OrderEvent struct {
	ID         int64          `json:"id"`
	OrderID    uuid.UUID      `json:"order_id"`
	FromStatus OrderStatus    `json:"from_status"`
	ToStatus   OrderStatus    `json:"to_status"`
	Event      string         `json:"event"`
	Detail     map[string]any `json:"detail,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
}
