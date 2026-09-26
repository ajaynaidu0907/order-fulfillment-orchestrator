package domain

import (
	"time"

	"github.com/google/uuid"
)

// ShipmentStatus is the state of a shipment for an order.
type ShipmentStatus string

const (
	ShipmentPending    ShipmentStatus = "pending"    // created, awaiting carrier pickup
	ShipmentDispatched ShipmentStatus = "dispatched" // handed to carrier / in transit
	ShipmentDelivered  ShipmentStatus = "delivered"
	ShipmentFailed     ShipmentStatus = "failed"
)

// Shipment records the physical fulfillment of an order.
type Shipment struct {
	ID             uuid.UUID      `json:"id"`
	OrderID        uuid.UUID      `json:"order_id"`
	Carrier        string         `json:"carrier"`
	TrackingNumber string         `json:"tracking_number"`
	Status         ShipmentStatus `json:"status"`
	ShippedAt      *time.Time     `json:"shipped_at,omitempty"`
	DeliveredAt    *time.Time     `json:"delivered_at,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}
