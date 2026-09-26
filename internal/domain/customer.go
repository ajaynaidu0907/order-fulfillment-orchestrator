package domain

import (
	"time"

	"github.com/google/uuid"
)

// Address is a physical shipping or billing address.
type Address struct {
	Line1      string `json:"line1"`
	Line2      string `json:"line2,omitempty"`
	City       string `json:"city"`
	State      string `json:"state"`
	PostalCode string `json:"postal_code"`
	Country    string `json:"country"` // ISO-3166 alpha-2, e.g. "US"
}

// Customer is a person or organisation that places orders.
type Customer struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Phone     string    `json:"phone,omitempty"`
	Address   Address   `json:"address"`
	CreatedAt time.Time `json:"created_at"`
}
