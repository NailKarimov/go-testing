package orders_test

// Chaos scenarios: orders → chaos proxy → inventory, all real HTTP servers on loopback.
// The proxy injects one network fault per scenario; each test checks what the user sees AND what really happened
// in inventory (units reserved, requests received). The second half is the part a mock cannot show.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/NailKarimov/go-testing/breaker"
	"github.com/NailKarimov/go-testing/chaos"
	"github.com/NailKarimov/go-testing/inventory"
	"github.com/NailKarimov/go-testing/orders"
)

type rig struct {
	store  *inventory.Store
	proxy  *chaos.Proxy
	svc    *orders.Service
	orders *httptest.Server
	clock  *fakeClock
	brk    *breaker.Breaker
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newRig(t *testing.T, stock int) *rig {
	t.Helper()
	store := inventory.NewStore(map[string]int{"BOOK-1": stock})
	inv := httptest.NewServer(store.Handler())
	t.Cleanup(inv.Close)
	proxy := chaos.New(inv.URL)
	px := httptest.NewServer(proxy)
	t.Cleanup(px.Close)
	clock := &fakeClock{t: time.Unix(0, 0)}
	brk := breaker.New(5, 30*time.Second)
	brk.Now = clock.now
	svc := orders.New(orders.Config{
		InventoryURL: px.URL,
		Timeout:      150 * time.Millisecond,
		Attempts:     3,
		Backoff:      10 * time.Millisecond,
		Breaker:      brk,
	})
	srv := httptest.NewServer(svc.Handler())
	t.Cleanup(srv.Close)
	return &rig{store, proxy, svc, srv, clock, brk}
}

var client = &http.Client{Timeout: 5 * time.Second}

func (r *rig) order(t *testing.T, qty int) (int, map[string]any) {
	t.Helper()
	resp, err := client.Post(r.orders.URL+"/orders", "application/json",
		bytes.NewBufferString(fmt.Sprintf(`{"sku":"BOOK-1","quantity":%d}`, qty)))
	if err != nil {
		t.Fatalf("POST /orders: %v", err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

func (r *rig) stock(t *testing.T) int {
	t.Helper()
	n, _ := r.store.Stock("BOOK-1")
	return n
}

func TestHappyPath(t *testing.T) {
	r := newRig(t, 10)
	status, body := r.order(t, 2)
	if status != 201 || body["status"] != "confirmed" {
		t.Fatalf("%d %v", status, body)
	}
	if r.stock(t) != 8 || r.svc.Attempts.Load() != 1 {
		t.Fatalf("stock %d attempts %d, want 8 and 1", r.stock(t), r.svc.Attempts.Load())
	}
}

func TestBusinessAnswersAreNotRetried(t *testing.T) {
	r := newRig(t, 1)
	if status, body := r.order(t, 5); status != 409 || body["available"] != 1.0 {
		t.Fatalf("out of stock: %d %v", status, body)
	}
	if r.svc.Attempts.Load() != 1 {
		t.Fatalf("a 409 was retried: %d attempts", r.svc.Attempts.Load())
	}
}

// The response is slower than the timeout, but the request did arrive: inventory reserved the books.
// The retry carries the same idempotency key, so it gets the first answer back instead of reserving again.
func TestSlowResponseRetryDoesNotReserveTwice(t *testing.T) {
	r := newRig(t, 10)
	r.proxy.Set(chaos.Fault{Latency: 300 * time.Millisecond, SlowFor: 1})
	status, body := r.order(t, 3)
	if status != 201 {
		t.Fatalf("%d %v", status, body)
	}
	time.Sleep(250 * time.Millisecond) // let the slow first request land
	if r.stock(t) != 7 || r.store.Reserved != 3 {
		t.Fatalf("stock %d reserved %d, want 7 and 3: the timed-out request and its retry reserved twice", r.stock(t), r.store.Reserved)
	}
	if r.svc.Attempts.Load() != 2 {
		t.Fatalf("attempts %d, want 2", r.svc.Attempts.Load())
	}
}

func TestBurstOf500sIsRetriedThrough(t *testing.T) {
	r := newRig(t, 10)
	r.proxy.Set(chaos.Fault{Error500: 2})
	if status, body := r.order(t, 1); status != 201 {
		t.Fatalf("%d %v", status, body)
	}
	if r.svc.Attempts.Load() != 3 || r.stock(t) != 9 {
		t.Fatalf("attempts %d stock %d, want 3 and 9", r.svc.Attempts.Load(), r.stock(t))
	}
}

func TestMore500sThanAttemptsGiveBadGatewayAndNoReservation(t *testing.T) {
	r := newRig(t, 10)
	r.proxy.Set(chaos.Fault{Error500: 3})
	if status, _ := r.order(t, 1); status != 502 {
		t.Fatalf("status %d, want 502", status)
	}
	if r.stock(t) != 10 {
		t.Fatalf("stock %d, want untouched 10", r.stock(t))
	}
}

// The connection drops after inventory has reserved but before the answer arrives. Same key on retry → once.
func TestConnectionResetAfterReservationIsSafeToRetry(t *testing.T) {
	r := newRig(t, 10)
	r.proxy.Set(chaos.Fault{Reset: 1})
	if status, body := r.order(t, 4); status != 201 {
		t.Fatalf("%d %v", status, body)
	}
	if r.stock(t) != 6 || r.store.Requests != 2 {
		t.Fatalf("stock %d requests %d, want 6 and 2", r.stock(t), r.store.Requests)
	}
}

func TestBlackHoleIsBoundedByTimeouts(t *testing.T) {
	r := newRig(t, 10)
	r.proxy.Set(chaos.Fault{BlackHole: true})
	start := time.Now()
	status, _ := r.order(t, 1)
	took := time.Since(start)
	if status != 502 {
		t.Fatalf("status %d, want 502", status)
	}
	// 3 attempts × 150 ms + 10 + 20 ms backoff ≈ 480 ms; allow slack for a busy CI runner.
	if took > 1500*time.Millisecond {
		t.Fatalf("took %s: a dead dependency must not hang the order", took)
	}
}

// Inventory goes down. Five failed orders open the breaker; after that orders fail fast with 503 and the
// dependency is left alone. When the cool-down ends and inventory is back, one trial closes the breaker.
func TestBreakerOpensOnOutageAndRecovers(t *testing.T) {
	r := newRig(t, 10)
	r.proxy.Set(chaos.Fault{Down: true})
	for i := 0; i < 5; i++ {
		if status, _ := r.order(t, 1); status != 502 {
			t.Fatalf("order %d during the outage: %d, want 502", i, status)
		}
	}
	if r.brk.State() != breaker.Open {
		t.Fatalf("breaker %s after 5 failed orders, want open", r.brk.State())
	}
	seen := r.proxy.Seen.Load()
	start := time.Now()
	status, _ := r.order(t, 1)
	if status != 503 || time.Since(start) > 100*time.Millisecond {
		t.Fatalf("open breaker: %d in %s, want a fast 503", status, time.Since(start))
	}
	if r.proxy.Seen.Load() != seen {
		t.Fatal("an open breaker still called inventory")
	}

	r.proxy.Set(chaos.Fault{}) // inventory is back
	r.clock.add(30 * time.Second)
	if status, _ := r.order(t, 1); status != 201 {
		t.Fatalf("trial after recovery: %d, want 201", status)
	}
	if r.brk.State() != breaker.Closed || r.stock(t) != 9 {
		t.Fatalf("breaker %s stock %d, want closed and 9", r.brk.State(), r.stock(t))
	}
}

// Many buyers at once while the network misbehaves: whatever happens, every confirmed order is backed by
// exactly its units in inventory and nothing is reserved without a confirmed order.
func TestConcurrentOrdersUnderChaosStayConsistent(t *testing.T) {
	r := newRig(t, 10)
	r.proxy.Set(chaos.Fault{Error500: 6, Reset: 6})
	var wg sync.WaitGroup
	var mu sync.Mutex
	codes := map[int]int{}
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, _ := r.order(t, 1)
			mu.Lock()
			codes[status]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	resp, err := client.Get(r.orders.URL + "/orders")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var list []orders.Order
	_ = json.NewDecoder(resp.Body).Decode(&list)
	units := 0
	for _, o := range list {
		units += o.Quantity
	}
	if units != r.store.Reserved || units != 10-r.stock(t) || units > 10 {
		t.Fatalf("confirmed units %d, reserved %d, stock %d (codes %v)", units, r.store.Reserved, r.stock(t), codes)
	}
	if codes[201] != len(list) {
		t.Fatalf("%d answers 201 but %d orders stored", codes[201], len(list))
	}
	t.Logf("codes %v, confirmed %d", codes, len(list))
}

func TestHealthShowsBreakerState(t *testing.T) {
	r := newRig(t, 10)
	resp, err := client.Get(r.orders.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["breaker"] != "closed" {
		t.Fatalf("%v", body)
	}
}

func TestOrderValidation(t *testing.T) {
	r := newRig(t, 10)
	for _, body := range []string{`{}`, `{"sku":"BOOK-1","quantity":0}`, `{"sku":"BOOK-1","quantity":1,"x":1}`, `nope`} {
		resp, err := client.Post(r.orders.URL+"/orders", "application/json", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Errorf("%s: %d, want 400", body, resp.StatusCode)
		}
	}
	if r.svc.Attempts.Load() != 0 {
		t.Fatal("an invalid order reached inventory")
	}
}

func TestUnknownSkuIs422(t *testing.T) {
	r := newRig(t, 10)
	resp, err := client.Post(r.orders.URL+"/orders", "application/json", bytes.NewBufferString(`{"sku":"NOPE","quantity":1}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 422 {
		t.Fatalf("%d, want 422", resp.StatusCode)
	}
}
