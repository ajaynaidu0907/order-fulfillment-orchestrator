package api

import (
	"time"

	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/domain"
)

// addressDTO is the wire representation of a shipping address.
type addressDTO struct {
	Line1      string `json:"line1"`
	Line2      string `json:"line2,omitempty"`
	City       string `json:"city"`
	State      string `json:"state"`
	PostalCode string `json:"postal_code"`
	Country    string `json:"country"`
}

func (a addressDTO) toDomain() domain.Address {
	return domain.Address{
		Line1:      a.Line1,
		Line2:      a.Line2,
		City:       a.City,
		State:      a.State,
		PostalCode: a.PostalCode,
		Country:    a.Country,
	}
}

func addressFromDomain(a domain.Address) addressDTO {
	return addressDTO{
		Line1:      a.Line1,
		Line2:      a.Line2,
		City:       a.City,
		State:      a.State,
		PostalCode: a.PostalCode,
		Country:    a.Country,
	}
}

// createOrderItemRequest is a single requested line: only the SKU and quantity
// are trusted from the client; name and price come from the catalog.
type createOrderItemRequest struct {
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
}

// createOrderRequest is the body of POST /v1/orders.
type createOrderRequest struct {
	CustomerID      string                   `json:"customer_id"`
	Currency        string                   `json:"currency"`
	Items           []createOrderItemRequest `json:"items"`
	ShippingAddress addressDTO               `json:"shipping_address"`
}

// orderItemResponse is the wire representation of an order line.
type orderItemResponse struct {
	ID             string `json:"id"`
	SKU            string `json:"sku"`
	Name           string `json:"name"`
	Quantity       int    `json:"quantity"`
	UnitPriceMinor int64  `json:"unit_price_minor"`
	LineTotalMinor int64  `json:"line_total_minor"`
}

// orderResponse is the wire representation of an order.
type orderResponse struct {
	ID               string              `json:"id"`
	CustomerID       string              `json:"customer_id"`
	Status           string              `json:"status"`
	Currency         string              `json:"currency"`
	TotalAmountMinor int64               `json:"total_amount_minor"`
	Items            []orderItemResponse `json:"items"`
	ShippingAddress  addressDTO          `json:"shipping_address"`
	IdempotencyKey   string              `json:"idempotency_key,omitempty"`
	Version          int                 `json:"version"`
	CreatedAt        time.Time           `json:"created_at"`
	UpdatedAt        time.Time           `json:"updated_at"`
}

func orderToResponse(o *domain.Order) orderResponse {
	items := make([]orderItemResponse, 0, len(o.Items))
	for _, it := range o.Items {
		items = append(items, orderItemResponse{
			ID:             it.ID.String(),
			SKU:            it.SKU,
			Name:           it.Name,
			Quantity:       it.Quantity,
			UnitPriceMinor: it.UnitPrice,
			LineTotalMinor: it.LineTotal(),
		})
	}
	return orderResponse{
		ID:               o.ID.String(),
		CustomerID:       o.CustomerID.String(),
		Status:           string(o.Status),
		Currency:         o.Currency,
		TotalAmountMinor: o.TotalAmount,
		Items:            items,
		ShippingAddress:  addressFromDomain(o.ShippingAddress),
		IdempotencyKey:   o.IdempotencyKey,
		Version:          o.Version,
		CreatedAt:        o.CreatedAt,
		UpdatedAt:        o.UpdatedAt,
	}
}

// eventResponse is the wire representation of an order audit event.
type eventResponse struct {
	ID         int64          `json:"id"`
	OrderID    string         `json:"order_id"`
	FromStatus string         `json:"from_status,omitempty"`
	ToStatus   string         `json:"to_status"`
	Event      string         `json:"event"`
	Detail     map[string]any `json:"detail,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
}

func eventToResponse(e domain.OrderEvent) eventResponse {
	return eventResponse{
		ID:         e.ID,
		OrderID:    e.OrderID.String(),
		FromStatus: string(e.FromStatus),
		ToStatus:   string(e.ToStatus),
		Event:      e.Event,
		Detail:     e.Detail,
		CreatedAt:  e.CreatedAt,
	}
}

// productResponse is the wire representation of a catalog product.
type productResponse struct {
	SKU         string    `json:"sku"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Category    string    `json:"category"`
	Brand       string    `json:"brand,omitempty"`
	PriceMinor  int64     `json:"price_minor"`
	Currency    string    `json:"currency"`
	CreatedAt   time.Time `json:"created_at"`
}

func productToResponse(p domain.Product) productResponse {
	return productResponse{
		SKU:         p.SKU,
		Name:        p.Name,
		Description: p.Description,
		Category:    p.Category,
		Brand:       p.Brand,
		PriceMinor:  p.Price,
		Currency:    p.Currency,
		CreatedAt:   p.CreatedAt,
	}
}

// ordersListResponse wraps a page of orders.
type ordersListResponse struct {
	Orders []orderResponse `json:"orders"`
	Count  int             `json:"count"`
	Limit  int             `json:"limit"`
	Offset int             `json:"offset"`
}

// productsListResponse wraps a page of products.
type productsListResponse struct {
	Products []productResponse `json:"products"`
	Count    int               `json:"count"`
	Limit    int               `json:"limit"`
	Offset   int               `json:"offset"`
}

// eventsListResponse wraps an order's audit trail.
type eventsListResponse struct {
	Events []eventResponse `json:"events"`
	Count  int             `json:"count"`
}
