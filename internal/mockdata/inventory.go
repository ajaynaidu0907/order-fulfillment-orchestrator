package mockdata

import "github.com/reddy-aravind/order-fulfillment-orchestrator/internal/domain"

// InventoryFor returns an initial stock level for every product at the "MAIN"
// warehouse, generated deterministically from seed. Roughly one in seven SKUs is
// deliberately low-stock (0-12 units) so demos exercise the insufficient-inventory
// path; the rest carry a healthy spread. Reserved always starts at zero.
func InventoryFor(products []domain.Product, seed int64) []domain.InventoryItem {
	r := newRand(seed)
	out := make([]domain.InventoryItem, 0, len(products))
	for _, p := range products {
		var available int
		if r.Intn(100) < 15 {
			available = r.Intn(13) // low stock: 0..12
		} else {
			available = 25 + r.Intn(576) // healthy: 25..600
		}
		out = append(out, domain.InventoryItem{
			SKU:       p.SKU,
			Warehouse: "MAIN",
			Available: available,
			Reserved:  0,
		})
	}
	return out
}
