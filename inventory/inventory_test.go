package inventory_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/NailKarimov/go-testing/inventory"
)

func newStore() *inventory.Store {
	return inventory.NewStore(map[string]int{"BOOK-1": 10, "BOOK-2": 3})
}

func do(t *testing.T, h http.Handler, method, path, body string, headers ...string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestGetItem(t *testing.T) {
	h := newStore().Handler()
	cases := []struct {
		name, path string
		status     int
		stock      any
	}{
		{"known sku", "/items/BOOK-1", 200, 10.0},
		{"low stock sku", "/items/BOOK-2", 200, 3.0},
		{"unknown sku", "/items/NOPE", 404, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := do(t, h, "GET", tc.path, "")
			if status != tc.status {
				t.Fatalf("status %d, want %d", status, tc.status)
			}
			if tc.stock != nil && body["stock"] != tc.stock {
				t.Fatalf("stock %v, want %v", body["stock"], tc.stock)
			}
		})
	}
}

func TestReserveValidation(t *testing.T) {
	cases := []struct {
		name, body string
		status     int
	}{
		{"valid", `{"sku":"BOOK-1","quantity":2}`, 201},
		{"exactly the stock", `{"sku":"BOOK-2","quantity":3}`, 201},
		{"more than the stock", `{"sku":"BOOK-2","quantity":4}`, 409},
		{"unknown sku", `{"sku":"NOPE","quantity":1}`, 404},
		{"zero quantity", `{"sku":"BOOK-1","quantity":0}`, 400},
		{"negative quantity", `{"sku":"BOOK-1","quantity":-1}`, 400},
		{"quantity over 100", `{"sku":"BOOK-1","quantity":101}`, 400},
		{"blank sku", `{"sku":"  ","quantity":1}`, 400},
		{"sku over 32 chars", `{"sku":"` + strings.Repeat("X", 33) + `","quantity":1}`, 400},
		{"quantity as string", `{"sku":"BOOK-1","quantity":"1"}`, 400},
		{"unknown field", `{"sku":"BOOK-1","quantity":1,"price":0}`, 400},
		{"trailing data", `{"sku":"BOOK-1","quantity":1}{}`, 400},
		{"not JSON", `sku=BOOK-1`, 400},
		{"empty body", ``, 400},
		{"body over 4 KB", `{"sku":"BOOK-1","quantity":1,"x":"` + strings.Repeat("a", 5000) + `"}`, 413},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := do(t, newStore().Handler(), "POST", "/reservations", tc.body)
			if status != tc.status {
				t.Fatalf("status %d, want %d (%v)", status, tc.status, body)
			}
		})
	}
}

func TestConflictReportsWhatIsLeft(t *testing.T) {
	_, body := do(t, newStore().Handler(), "POST", "/reservations", `{"sku":"BOOK-2","quantity":5}`)
	if body["available"] != 3.0 {
		t.Fatalf("available %v, want 3", body["available"])
	}
}

func TestSameIdempotencyKeyReservesOnce(t *testing.T) {
	s := newStore()
	h := s.Handler()
	for i := 0; i < 3; i++ {
		status, body := do(t, h, "POST", "/reservations", `{"sku":"BOOK-1","quantity":4}`, "Idempotency-Key", "k1")
		if status != 201 || body["left"] != 6.0 {
			t.Fatalf("attempt %d: %d %v, want 201 with left=6 every time", i, status, body)
		}
	}
	if n, _ := s.Stock("BOOK-1"); n != 6 {
		t.Fatalf("stock %d, want 6: the repeats must not reserve again", n)
	}
	if s.Requests != 3 {
		t.Fatalf("requests %d, want 3", s.Requests)
	}
}

func TestDifferentKeysReserveSeparately(t *testing.T) {
	s := newStore()
	h := s.Handler()
	do(t, h, "POST", "/reservations", `{"sku":"BOOK-1","quantity":4}`, "Idempotency-Key", "a")
	do(t, h, "POST", "/reservations", `{"sku":"BOOK-1","quantity":4}`, "Idempotency-Key", "b")
	if n, _ := s.Stock("BOOK-1"); n != 2 {
		t.Fatalf("stock %d, want 2", n)
	}
}

func TestIdempotencyKeyRemembersARefusal(t *testing.T) {
	// A key that got 409 keeps getting 409, even if stock appears later: the answer for a key never changes.
	s := newStore()
	h := s.Handler()
	if st, _ := do(t, h, "POST", "/reservations", `{"sku":"BOOK-2","quantity":3}`, "Idempotency-Key", "x"); st != 201 {
		t.Fatal(st)
	}
	if st, _ := do(t, h, "POST", "/reservations", `{"sku":"BOOK-2","quantity":1}`, "Idempotency-Key", "y"); st != 409 {
		t.Fatal(st)
	}
	if st, _ := do(t, h, "POST", "/reservations", `{"sku":"BOOK-2","quantity":1}`, "Idempotency-Key", "y"); st != 409 {
		t.Fatalf("repeat of a refused key got %d", st)
	}
}

// Run with -race: 50 buyers for 10 copies at once. Exactly 10 get one, nobody gets a copy that is not there.
func TestConcurrentReservationsNeverOversell(t *testing.T) {
	s := newStore()
	h := s.Handler()
	var wg sync.WaitGroup
	var mu sync.Mutex
	codes := map[int]int{}
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest("POST", "/reservations", bytes.NewBufferString(`{"sku":"BOOK-1","quantity":1}`))
			req.Header.Set("Idempotency-Key", fmt.Sprintf("buyer-%d", i))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			mu.Lock()
			codes[rec.Code]++
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	if codes[201] != 10 || codes[409] != 40 {
		t.Fatalf("codes %v, want 10×201 and 40×409", codes)
	}
	if n, _ := s.Stock("BOOK-1"); n != 0 || s.Reserved != 10 {
		t.Fatalf("stock %d reserved %d, want 0 and 10", n, s.Reserved)
	}
}

func BenchmarkReserve(b *testing.B) {
	s := inventory.NewStore(map[string]int{"SKU": 1 << 30})
	r := inventory.Reservation{SKU: "SKU", Quantity: 1}
	for i := 0; b.Loop(); i++ {
		s.Reserve(fmt.Sprintf("k%d", i), r)
	}
}

func BenchmarkHandler(b *testing.B) {
	h := inventory.NewStore(map[string]int{"SKU": 1 << 30}).Handler()
	for b.Loop() {
		req := httptest.NewRequest("POST", "/reservations", strings.NewReader(`{"sku":"SKU","quantity":1}`))
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
}
