package mockdata

import (
	"reflect"
	"strings"
	"testing"
)

func TestProductsCatalog(t *testing.T) {
	products := Products()

	if n := len(products); n < 70 || n > 90 {
		t.Fatalf("catalog size %d outside expected 70-90 range", n)
	}

	skus := make(map[string]struct{}, len(products))
	categories := make(map[string]struct{})
	for _, p := range products {
		if _, dup := skus[p.SKU]; dup {
			t.Errorf("duplicate SKU %q", p.SKU)
		}
		skus[p.SKU] = struct{}{}

		if strings.TrimSpace(p.Name) == "" {
			t.Errorf("product %q has empty name", p.SKU)
		}
		if strings.TrimSpace(p.Description) == "" {
			t.Errorf("product %q has empty description", p.SKU)
		}
		if strings.TrimSpace(p.Brand) == "" {
			t.Errorf("product %q has empty brand", p.SKU)
		}
		if p.Price <= 0 {
			t.Errorf("product %q has non-positive price %d", p.SKU, p.Price)
		}
		if p.Currency != "USD" {
			t.Errorf("product %q currency = %q, want USD", p.SKU, p.Currency)
		}
		if p.Category == "" {
			t.Errorf("product %q has empty category", p.SKU)
		}
		categories[p.Category] = struct{}{}
	}

	if len(categories) < 6 {
		t.Errorf("expected several categories, got %d: %v", len(categories), categories)
	}
}

func TestProductsDeterministic(t *testing.T) {
	if !reflect.DeepEqual(Products(), Products()) {
		t.Fatal("Products() is not deterministic")
	}
}

func TestCustomersUniqueEmailsAndFields(t *testing.T) {
	custs := Customers(500, 7)
	if len(custs) != 500 {
		t.Fatalf("got %d customers, want 500", len(custs))
	}

	emails := make(map[string]struct{}, len(custs))
	ids := make(map[string]struct{}, len(custs))
	for _, c := range custs {
		if _, dup := emails[c.Email]; dup {
			t.Errorf("duplicate email %q", c.Email)
		}
		emails[c.Email] = struct{}{}

		if _, dup := ids[c.ID.String()]; dup {
			t.Errorf("duplicate customer ID %q", c.ID)
		}
		ids[c.ID.String()] = struct{}{}

		if strings.TrimSpace(c.Name) == "" {
			t.Error("customer has empty name")
		}
		if !strings.Contains(c.Email, "@") {
			t.Errorf("customer email %q is malformed", c.Email)
		}
		if strings.TrimSpace(c.Phone) == "" {
			t.Error("customer has empty phone")
		}
		if c.Address.City == "" || c.Address.State == "" || c.Address.PostalCode == "" {
			t.Errorf("customer %q has incomplete address: %+v", c.Email, c.Address)
		}
		if c.Address.Country != "US" {
			t.Errorf("customer %q country = %q, want US", c.Email, c.Address.Country)
		}
	}
}

func TestCustomersDeterministic(t *testing.T) {
	a := Customers(120, 99)
	b := Customers(120, 99)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("Customers is not deterministic for a fixed seed")
	}

	c := Customers(120, 100)
	if reflect.DeepEqual(a, c) {
		t.Fatal("Customers produced identical output for different seeds")
	}
}

func TestCustomersEmpty(t *testing.T) {
	if got := Customers(0, 1); got != nil {
		t.Errorf("Customers(0) = %v, want nil", got)
	}
}

func TestInventoryStockNonNegative(t *testing.T) {
	products := Products()
	inv := InventoryFor(products, 3)
	if len(inv) != len(products) {
		t.Fatalf("got %d inventory rows, want %d", len(inv), len(products))
	}

	skus := make(map[string]struct{}, len(products))
	for _, p := range products {
		skus[p.SKU] = struct{}{}
	}

	sawLowStock := false
	for _, it := range inv {
		if it.Available < 0 {
			t.Errorf("SKU %q has negative stock %d", it.SKU, it.Available)
		}
		if it.Reserved != 0 {
			t.Errorf("SKU %q reserved = %d, want 0", it.SKU, it.Reserved)
		}
		if it.Warehouse != "MAIN" {
			t.Errorf("SKU %q warehouse = %q, want MAIN", it.SKU, it.Warehouse)
		}
		if _, ok := skus[it.SKU]; !ok {
			t.Errorf("inventory references unknown SKU %q", it.SKU)
		}
		if it.Available <= 12 {
			sawLowStock = true
		}
	}
	if !sawLowStock {
		t.Error("expected at least one low-stock SKU in the spread")
	}
}

func TestInventoryDeterministic(t *testing.T) {
	products := Products()
	a := InventoryFor(products, 55)
	b := InventoryFor(products, 55)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("InventoryFor is not deterministic for a fixed seed")
	}
}

func TestSampleOrderRequests(t *testing.T) {
	custs := Customers(50, 1)
	products := Products()

	reqs := SampleOrderRequests(custs, products, 30, 2)
	if len(reqs) != 30 {
		t.Fatalf("got %d requests, want 30", len(reqs))
	}

	for _, r := range reqs {
		if r.Customer.Email == "" {
			t.Error("order request has no customer")
		}
		if l := len(r.Lines); l < 1 || l > 4 {
			t.Errorf("order request has %d lines, want 1-4", l)
		}
		seen := make(map[string]struct{}, len(r.Lines))
		for _, ln := range r.Lines {
			if ln.Quantity < 1 || ln.Quantity > 3 {
				t.Errorf("line quantity %d outside 1-3", ln.Quantity)
			}
			if ln.Product.SKU == "" {
				t.Error("line has no product")
			}
			if _, dup := seen[ln.Product.SKU]; dup {
				t.Errorf("duplicate SKU %q within one order", ln.Product.SKU)
			}
			seen[ln.Product.SKU] = struct{}{}
		}
	}
}

func TestSampleOrderRequestsDeterministic(t *testing.T) {
	custs := Customers(50, 1)
	products := Products()
	a := SampleOrderRequests(custs, products, 25, 8)
	b := SampleOrderRequests(custs, products, 25, 8)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("SampleOrderRequests is not deterministic for a fixed seed")
	}
}

func TestSampleOrderRequestsEmptyInputs(t *testing.T) {
	if got := SampleOrderRequests(nil, Products(), 5, 1); got != nil {
		t.Errorf("expected nil with no customers, got %v", got)
	}
	if got := SampleOrderRequests(Customers(3, 1), nil, 5, 1); got != nil {
		t.Errorf("expected nil with no products, got %v", got)
	}
}
