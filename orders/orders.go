// Package orders is the front service: POST /orders reserves stock in the inventory service and records the order.
//
// The call to inventory is where things go wrong in a distributed system, so it is wrapped in three defences:
//   - a timeout per attempt, so a slow inventory cannot hang the order;
//   - retries with backoff for timeouts, resets and 5xx, all with the same Idempotency-Key, so a retry after a lost
//     response never reserves twice;
//   - a circuit breaker, so when inventory is down orders fail fast with 503 instead of piling up.
package orders

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NailKarimov/go-testing/breaker"
)

// Config tunes the client side of the inventory call.
type Config struct {
	InventoryURL string
	Timeout      time.Duration // per attempt
	Attempts     int           // first try + retries
	Backoff      time.Duration // doubled after each retry
	Breaker      *breaker.Breaker
	Client       *http.Client
}

// Order is what the service stores and returns.
type Order struct {
	ID       int    `json:"id"`
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
	Status   string `json:"status"`
}

// Service is the orders service.
type Service struct {
	cfg    Config
	mu     sync.Mutex
	orders []Order
	nextID atomic.Int64
	// Attempts counts calls made to inventory, for tests that check retry behaviour.
	Attempts atomic.Int64
}

// New creates the service with defaults for anything left empty.
func New(cfg Config) *Service {
	if cfg.Timeout == 0 {
		cfg.Timeout = 300 * time.Millisecond
	}
	if cfg.Attempts == 0 {
		cfg.Attempts = 3
	}
	if cfg.Backoff == 0 {
		cfg.Backoff = 20 * time.Millisecond
	}
	if cfg.Breaker == nil {
		cfg.Breaker = breaker.New(5, 2*time.Second)
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{}
	}
	return &Service{cfg: cfg}
}

// errRetryable marks failures worth another attempt: timeouts, resets, 5xx.
type errRetryable struct{ cause error }

func (e errRetryable) Error() string { return e.cause.Error() }

// reply is a final answer from inventory: 201 created, 404 or 409 are business answers, not failures.
type reply struct {
	status int
	body   map[string]any
}

func (s *Service) reserveOnce(ctx context.Context, key string, payload []byte) (reply, error) {
	s.Attempts.Add(1)
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.InventoryURL+"/reservations", bytes.NewReader(payload))
	if err != nil {
		return reply{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	resp, err := s.cfg.Client.Do(req)
	if err != nil {
		return reply{}, errRetryable{err}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return reply{}, errRetryable{err} // connection dropped mid-body
	}
	if resp.StatusCode >= 500 {
		return reply{}, errRetryable{fmt.Errorf("inventory answered %d", resp.StatusCode)}
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return reply{}, errRetryable{fmt.Errorf("inventory sent invalid JSON: %w", err)}
	}
	return reply{resp.StatusCode, body}, nil
}

// reserve calls inventory through the breaker with retries. Every attempt carries the same idempotency key.
func (s *Service) reserve(ctx context.Context, key string, payload []byte) (reply, error) {
	var out reply
	err := s.cfg.Breaker.Do(func() error {
		wait := s.cfg.Backoff
		var last error
		for i := 0; i < s.cfg.Attempts; i++ {
			if i > 0 {
				select {
				case <-time.After(wait):
				case <-ctx.Done():
					return ctx.Err()
				}
				wait *= 2
			}
			r, err := s.reserveOnce(ctx, key, payload)
			if err == nil {
				out = r
				return nil
			}
			last = err
			var re errRetryable
			if !errors.As(err, &re) {
				return err
			}
		}
		return last
	})
	return out, err
}

// Handler serves POST /orders and GET /orders.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /orders", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			SKU      string `json:"sku"`
			Quantity int    `json:"quantity"`
		}
		dec := json.NewDecoder(io.LimitReader(r.Body, 4096))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil || in.SKU == "" || in.Quantity < 1 || in.Quantity > 100 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sku and quantity 1-100 are required"})
			return
		}
		id := int(s.nextID.Add(1))
		payload, _ := json.Marshal(in)
		res, err := s.reserve(r.Context(), "order-"+strconv.Itoa(id), payload)
		switch {
		case errors.Is(err, breaker.ErrOpen):
			w.Header().Set("Retry-After", "2")
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "inventory unavailable (circuit open)"})
			return
		case err != nil:
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "inventory failed: " + err.Error()})
			return
		case res.status == http.StatusNotFound:
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "unknown sku"})
			return
		case res.status == http.StatusConflict:
			writeJSON(w, http.StatusConflict, map[string]any{"error": "out of stock", "available": res.body["available"]})
			return
		case res.status != http.StatusCreated:
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("unexpected inventory status %d", res.status)})
			return
		}
		o := Order{ID: id, SKU: in.SKU, Quantity: in.Quantity, Status: "confirmed"}
		s.mu.Lock()
		s.orders = append(s.orders, o)
		s.mu.Unlock()
		writeJSON(w, http.StatusCreated, o)
	})
	mux.HandleFunc("GET /orders", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		writeJSON(w, http.StatusOK, append([]Order{}, s.orders...))
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"breaker": string(s.cfg.Breaker.State())})
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
