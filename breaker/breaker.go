// Package breaker is a small circuit breaker: after N failures in a row it stops calling a dependency for a
// cool-down period and fails fast, then lets one trial call through (half-open) to see if it has recovered.
package breaker

import (
	"errors"
	"sync"
	"time"
)

// ErrOpen is returned while the circuit is open: the call was not attempted.
var ErrOpen = errors.New("circuit open")

// State of the breaker.
type State string

const (
	Closed   State = "closed"
	Open     State = "open"
	HalfOpen State = "half-open"
)

// Breaker counts consecutive failures. Now is replaceable so tests can move time instead of sleeping.
type Breaker struct {
	mu        sync.Mutex
	threshold int
	cooldown  time.Duration
	failures  int
	openedAt  time.Time
	state     State
	trial     bool
	Now       func() time.Time
}

// New creates a breaker that opens after threshold consecutive failures and stays open for cooldown.
func New(threshold int, cooldown time.Duration) *Breaker {
	return &Breaker{threshold: threshold, cooldown: cooldown, state: Closed, Now: time.Now}
}

// State reports the current state.
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.advance()
	return b.state
}

func (b *Breaker) advance() {
	if b.state == Open && b.Now().Sub(b.openedAt) >= b.cooldown {
		b.state = HalfOpen
		b.trial = false
	}
}

// Do runs fn unless the circuit is open. In half-open state only one trial call runs at a time.
func (b *Breaker) Do(fn func() error) error {
	b.mu.Lock()
	b.advance()
	switch {
	case b.state == Open:
		b.mu.Unlock()
		return ErrOpen
	case b.state == HalfOpen && b.trial:
		b.mu.Unlock()
		return ErrOpen
	case b.state == HalfOpen:
		b.trial = true
	}
	b.mu.Unlock()

	err := fn()

	b.mu.Lock()
	defer b.mu.Unlock()
	if err == nil {
		b.failures = 0
		b.state = Closed
		b.trial = false
		return nil
	}
	b.failures++
	if b.state == HalfOpen || b.failures >= b.threshold {
		b.state = Open
		b.openedAt = b.Now()
		b.trial = false
	}
	return err
}
