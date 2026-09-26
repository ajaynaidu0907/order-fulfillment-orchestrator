package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/domain"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/observability"
)

// testEnv bundles a router with the fakes backing it so tests can inspect state.
type testEnv struct {
	router      http.Handler
	orders      *fakeOrderStore
	inventory   *fakeInventory
	payment     *fakePayment
	fulfillment *fakeFulfillment
	limiter     *fakeRateLimiter
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	orders := newFakeOrderStore()
	inv := newFakeInventory()
	inv.addProduct(domain.Product{SKU: "SKU-1", Name: "Widget", Category: "tools", Price: 1500, Currency: "USD"})
	inv.addProduct(domain.Product{SKU: "SKU-2", Name: "Gadget", Category: "tools", Price: 2500, Currency: "USD"})
	pay := &fakePayment{}
	ful := &fakeFulfillment{}
	lim := &fakeRateLimiter{allow: true}

	router := NewRouter(Deps{
		Orders:      orders,
		Inventory:   inv,
		Payment:     pay,
		Fulfillment: ful,
		RateLimiter: lim,
		Metrics:     observability.NewMetrics(),
	})
	return &testEnv{router: router, orders: orders, inventory: inv, payment: pay, fulfillment: ful, limiter: lim}
}

func (e *testEnv) do(t *testing.T, method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, reader)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

func decodeOrder(t *testing.T, r io.Reader) orderResponse {
	t.Helper()
	var o orderResponse
	if err := json.NewDecoder(r).Decode(&o); err != nil {
		t.Fatalf("decode order: %v", err)
	}
	return o
}

func validCreateBody() map[string]any {
	return map[string]any{
		"customer_id": uuid.New().String(),
		"currency":    "USD",
		"items": []map[string]any{
			{"sku": "SKU-1", "quantity": 2},
			{"sku": "SKU-2", "quantity": 1},
		},
		"shipping_address": map[string]any{
			"line1":       "1 Test St",
			"city":        "Testville",
			"state":       "CA",
			"postal_code": "90000",
			"country":     "US",
		},
	}
}

func TestHealthz(t *testing.T) {
	env := newTestEnv(t)
	rec := env.do(t, http.MethodGet, "/healthz", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("status field = %q, want ok", body["status"])
	}
}

func TestCreateOrder_Success(t *testing.T) {
	env := newTestEnv(t)
	rec := env.do(t, http.MethodPost, "/v1/orders", validCreateBody(), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	o := decodeOrder(t, rec.Body)
	if o.Status != string(domain.StatusCreated) {
		t.Fatalf("status = %q, want CREATED", o.Status)
	}
	// 2 * 1500 + 1 * 2500 = 5500.
	if o.TotalAmountMinor != 5500 {
		t.Fatalf("total = %d, want 5500", o.TotalAmountMinor)
	}
	if len(o.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(o.Items))
	}
	// Name comes authoritatively from the catalog, not the request.
	if o.Items[0].Name != "Widget" {
		t.Fatalf("item name = %q, want Widget", o.Items[0].Name)
	}
	if _, err := uuid.Parse(o.ID); err != nil {
		t.Fatalf("order id not a uuid: %v", err)
	}
}

func TestCreateOrder_Idempotent(t *testing.T) {
	env := newTestEnv(t)
	headers := map[string]string{"Idempotency-Key": "key-abc"}
	body := validCreateBody()

	rec1 := env.do(t, http.MethodPost, "/v1/orders", body, headers)
	if rec1.Code != http.StatusCreated {
		t.Fatalf("first create status = %d, want 201; body=%s", rec1.Code, rec1.Body.String())
	}
	first := decodeOrder(t, rec1.Body)

	rec2 := env.do(t, http.MethodPost, "/v1/orders", body, headers)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second create status = %d, want 200; body=%s", rec2.Code, rec2.Body.String())
	}
	second := decodeOrder(t, rec2.Body)

	if first.ID != second.ID {
		t.Fatalf("idempotent create returned different ids: %s vs %s", first.ID, second.ID)
	}
	if len(env.orders.orders) != 1 {
		t.Fatalf("stored orders = %d, want 1", len(env.orders.orders))
	}
}

func TestCreateOrder_ProductNotFound(t *testing.T) {
	env := newTestEnv(t)
	body := validCreateBody()
	body["items"] = []map[string]any{{"sku": "NOPE", "quantity": 1}}
	rec := env.do(t, http.MethodPost, "/v1/orders", body, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreateOrder_Validation(t *testing.T) {
	env := newTestEnv(t)

	// No items.
	empty := validCreateBody()
	empty["items"] = []map[string]any{}
	if rec := env.do(t, http.MethodPost, "/v1/orders", empty, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty items status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}

	// Non-positive quantity.
	badQty := validCreateBody()
	badQty["items"] = []map[string]any{{"sku": "SKU-1", "quantity": 0}}
	if rec := env.do(t, http.MethodPost, "/v1/orders", badQty, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("zero quantity status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}

	// Invalid customer id.
	badCust := validCreateBody()
	badCust["customer_id"] = "not-a-uuid"
	if rec := env.do(t, http.MethodPost, "/v1/orders", badCust, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad customer_id status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestGetOrder_NotFound(t *testing.T) {
	env := newTestEnv(t)
	rec := env.do(t, http.MethodGet, "/v1/orders/"+uuid.New().String(), nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestGetOrder_Found(t *testing.T) {
	env := newTestEnv(t)
	created := decodeOrder(t, env.do(t, http.MethodPost, "/v1/orders", validCreateBody(), nil).Body)
	rec := env.do(t, http.MethodGet, "/v1/orders/"+created.ID, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	got := decodeOrder(t, rec.Body)
	if got.ID != created.ID {
		t.Fatalf("got id %s, want %s", got.ID, created.ID)
	}
}

func TestCancelOrder_HappyPath(t *testing.T) {
	env := newTestEnv(t)
	created := decodeOrder(t, env.do(t, http.MethodPost, "/v1/orders", validCreateBody(), nil).Body)
	id := uuid.MustParse(created.ID)

	// Advance the order so cancellation must refund and release.
	env.orders.setStatus(id, domain.StatusPaymentAuthorized)

	rec := env.do(t, http.MethodPost, "/v1/orders/"+created.ID+"/cancel", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	got := decodeOrder(t, rec.Body)
	if got.Status != string(domain.StatusCancelled) {
		t.Fatalf("status = %q, want CANCELLED", got.Status)
	}
	if len(env.payment.refunded) != 1 {
		t.Fatalf("refunds = %d, want 1", len(env.payment.refunded))
	}
	if len(env.inventory.released) != 1 {
		t.Fatalf("releases = %d, want 1", len(env.inventory.released))
	}
}

func TestCancelOrder_NotCancellable(t *testing.T) {
	env := newTestEnv(t)
	created := decodeOrder(t, env.do(t, http.MethodPost, "/v1/orders", validCreateBody(), nil).Body)
	id := uuid.MustParse(created.ID)
	env.orders.setStatus(id, domain.StatusShipped)

	rec := env.do(t, http.MethodPost, "/v1/orders/"+created.ID+"/cancel", nil, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	if len(env.payment.refunded) != 0 || len(env.inventory.released) != 0 {
		t.Fatalf("no compensations should run for a shipped order")
	}
}

func TestListOrders(t *testing.T) {
	env := newTestEnv(t)
	cust := uuid.New().String()
	for i := 0; i < 3; i++ {
		body := validCreateBody()
		body["customer_id"] = cust
		if rec := env.do(t, http.MethodPost, "/v1/orders", body, nil); rec.Code != http.StatusCreated {
			t.Fatalf("create %d status = %d", i, rec.Code)
		}
	}
	// One more order for a different customer.
	env.do(t, http.MethodPost, "/v1/orders", validCreateBody(), nil)

	rec := env.do(t, http.MethodGet, "/v1/orders?customer_id="+cust, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var list ordersListResponse
	if err := json.NewDecoder(rec.Body).Decode(&list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if list.Count != 3 || len(list.Orders) != 3 {
		t.Fatalf("count = %d (orders %d), want 3", list.Count, len(list.Orders))
	}
}

func TestListProducts(t *testing.T) {
	env := newTestEnv(t)
	rec := env.do(t, http.MethodGet, "/v1/products", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var list productsListResponse
	if err := json.NewDecoder(rec.Body).Decode(&list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if list.Count != 2 {
		t.Fatalf("count = %d, want 2", list.Count)
	}
}

func TestGetProduct(t *testing.T) {
	env := newTestEnv(t)
	if rec := env.do(t, http.MethodGet, "/v1/products/SKU-1", nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("existing product status = %d, want 200", rec.Code)
	}
	if rec := env.do(t, http.MethodGet, "/v1/products/NOPE", nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("missing product status = %d, want 404", rec.Code)
	}
}

func TestRateLimit(t *testing.T) {
	orders := newFakeOrderStore()
	inv := newFakeInventory()
	router := NewRouter(Deps{
		Orders:      orders,
		Inventory:   inv,
		Payment:     &fakePayment{},
		Fulfillment: &fakeFulfillment{},
		RateLimiter: &fakeRateLimiter{allow: false}, // deny everything
		Metrics:     observability.NewMetrics(),
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/products", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body=%s", rec.Code, rec.Body.String())
	}

	// Operational probes are not rate limited.
	req2 := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200 (should bypass rate limit)", rec2.Code)
	}
}
