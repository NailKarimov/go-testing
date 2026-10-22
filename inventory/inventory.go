// Package inventory is a stock service: it holds how many units of each SKU are left and reserves them.
//
// A reservation carries an Idempotency-Key. Sending the same key again returns the first result instead of
// reserving twice, which is what makes it safe for the orders service to retry after a timeout.
package inventory

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
)

// Reservation is the body of POST /reservations.
type Reservation struct {
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
}

// Validate checks a reservation before it touches the stock.
func (r Reservation) Validate() error {
	switch {
	case strings.TrimSpace(r.SKU) == "" || len(r.SKU) > 32:
		return errors.New("sku must be 1-32 characters")
	case r.Quantity < 1 || r.Quantity > 100:
		return errors.New("quantity must be 1-100")
	}
	return nil
}

// ParseReservation decodes and validates a request body. Unknown fields and trailing data are rejected.
func ParseReservation(body []byte) (Reservation, error) {
	var r Reservation
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return Reservation{}, errors.New("invalid JSON: " + err.Error())
	}
	if dec.More() {
		return Reservation{}, errors.New("invalid JSON: trailing data")
	}
	return r, r.Validate()
}

type result struct {
	status int
	body   any
}

// Store is the inventory: stock per SKU and the results of reservations already made, by idempotency key.
type Store struct {
	mu       sync.Mutex
	stock    map[string]int
	seen     map[string]result
	Reserved int // total units reserved, for tests that count side effects
	Requests int // reservation requests received, including repeats
}

// NewStore creates a store with the given stock.
func NewStore(stock map[string]int) *Store {
	s := &Store{stock: map[string]int{}, seen: map[string]result{}}
	for k, v := range stock {
		s.stock[k] = v
	}
	return s
}

// Stock returns the units left for a SKU and whether the SKU exists.
func (s *Store) Stock(sku string) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.stock[sku]
	return n, ok
}

// Reserve takes units out of stock, at most once per idempotency key.
func (s *Store) Reserve(key string, r Reservation) (int, any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Requests++
	if prev, ok := s.seen[key]; ok && key != "" {
		return prev.status, prev.body
	}
	var res result
	n, ok := s.stock[r.SKU]
	switch {
	case !ok:
		res = result{http.StatusNotFound, map[string]string{"error": "unknown sku"}}
	case n < r.Quantity:
		res = result{http.StatusConflict, map[string]any{"error": "not enough stock", "available": n}}
	default:
		s.stock[r.SKU] = n - r.Quantity
		s.Reserved += r.Quantity
		res = result{http.StatusCreated, map[string]any{"sku": r.SKU, "quantity": r.Quantity, "left": n - r.Quantity}}
	}
	if key != "" {
		s.seen[key] = res
	}
	return res.status, res.body
}

// Handler serves GET /items/{sku} and POST /reservations.
func (s *Store) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /items/{sku}", func(w http.ResponseWriter, r *http.Request) {
		n, ok := s.Stock(r.PathValue("sku"))
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown sku"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"sku": r.PathValue("sku"), "stock": n})
	})
	mux.HandleFunc("POST /reservations", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4097))
		if len(body) > 4096 {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "body too large"})
			return
		}
		res, err := ParseReservation(body)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		status, out := s.Reserve(r.Header.Get("Idempotency-Key"), res)
		writeJSON(w, status, out)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
