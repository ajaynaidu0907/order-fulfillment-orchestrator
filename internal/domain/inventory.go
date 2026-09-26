package domain

import (
	"time"

	"github.com/google/uuid"
)

// Product is a sellable item in the catalog.
type Product struct {
	SKU         string    `json:"sku"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Category    string    `json:"category"`
	Brand       string    `json:"brand,omitempty"`
	Price       int64     `json:"price_minor"` // minor units (cents)
	Currency    string    `json:"currency"`
	CreatedAt   time.Time `json:"created_at"`
}

// InventoryItem tracks stock for a SKU at a warehouse.
type InventoryItem struct {
	SKU       string    `json:"sku"`
	Warehouse string    `json:"warehouse"`
	Available int       `json:"available"` // on-hand and unreserved
	Reserved  int       `json:"reserved"`  // held for orders not yet fulfilled
	UpdatedAt time.Time `json:"updated_at"`
}

// ReservationStatus is the state of an inventory reservation.
type ReservationStatus string

const (
	ReservationReserved  ReservationStatus = "reserved"  // stock held for the order
	ReservationCommitted ReservationStatus = "committed" // consumed on shipment
	ReservationReleased  ReservationStatus = "released"  // returned to available (compensation)
)

// Reservation is a hold placed on stock for a specific order line.
type Reservation struct {
	ID        uuid.UUID         `json:"id"`
	OrderID   uuid.UUID         `json:"order_id"`
	SKU       string            `json:"sku"`
	Quantity  int               `json:"quantity"`
	Status    ReservationStatus `json:"status"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// ProductFilter narrows a catalog listing query.
type ProductFilter struct {
	Category string
	Search   string
	Limit    int
	Offset   int
}
