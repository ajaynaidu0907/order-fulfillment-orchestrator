package mockdata

import "github.com/reddy-aravind/order-fulfillment-orchestrator/internal/domain"

// OrderLine pairs a chosen product with the quantity ordered.
type OrderLine struct {
	Product  domain.Product
	Quantity int
}

// OrderRequest is a plausible order composition: a customer plus one to four
// distinct line items. Seeders and demos turn these into real domain.Order rows.
type OrderRequest struct {
	Customer domain.Customer
	Lines    []OrderLine
}

// SampleOrderRequests returns n plausible order compositions generated
// deterministically from seed. Each request picks a random customer and 1-4
// distinct products with quantities of 1-3. It returns nil if there is nothing to
// draw from.
func SampleOrderRequests(customers []domain.Customer, products []domain.Product, n int, seed int64) []OrderRequest {
	if n <= 0 || len(customers) == 0 || len(products) == 0 {
		return nil
	}
	r := newRand(seed)
	out := make([]OrderRequest, 0, n)
	for i := 0; i < n; i++ {
		cust := customers[r.Intn(len(customers))]

		numLines := 1 + r.Intn(4) // 1..4
		if numLines > len(products) {
			numLines = len(products)
		}

		chosen := make(map[int]struct{}, numLines)
		lines := make([]OrderLine, 0, numLines)
		for len(lines) < numLines {
			idx := r.Intn(len(products))
			if _, dup := chosen[idx]; dup {
				continue
			}
			chosen[idx] = struct{}{}
			lines = append(lines, OrderLine{
				Product:  products[idx],
				Quantity: 1 + r.Intn(3), // 1..3
			})
		}
		out = append(out, OrderRequest{Customer: cust, Lines: lines})
	}
	return out
}
