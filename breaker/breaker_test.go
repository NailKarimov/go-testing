package breaker_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/NailKarimov/go-testing/breaker"
)

var boom = errors.New("boom")

// clock is a fake time source: tests move it forward instead of sleeping.
type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }
func newBreaker(c *clock) *breaker.Breaker {
	b := breaker.New(3, time.Minute)
	b.Now = c.now
	return b
}

func fail() error { return boom }
func ok() error   { return nil }

func TestOpensAfterThresholdConsecutiveFailures(t *testing.T) {
	b := newBreaker(&clock{time.Unix(0, 0)})
	for i := 0; i < 2; i++ {
		_ = b.Do(fail)
	}
	if b.State() != breaker.Closed {
		t.Fatalf("open after 2 failures, threshold is 3")
	}
	_ = b.Do(fail)
	if b.State() != breaker.Open {
		t.Fatalf("state %s after 3 failures, want open", b.State())
	}
}

func TestSuccessResetsTheCount(t *testing.T) {
	b := newBreaker(&clock{time.Unix(0, 0)})
	_, _ = b.Do(fail), b.Do(fail)
	_ = b.Do(ok)
	_, _ = b.Do(fail), b.Do(fail)
	if b.State() != breaker.Closed {
		t.Fatal("failures are not consecutive, the breaker must stay closed")
	}
}

func TestOpenFailsFastWithoutCalling(t *testing.T) {
	b := newBreaker(&clock{time.Unix(0, 0)})
	for i := 0; i < 3; i++ {
		_ = b.Do(fail)
	}
	called := false
	err := b.Do(func() error { called = true; return nil })
	if !errors.Is(err, breaker.ErrOpen) || called {
		t.Fatalf("err %v called %v, want ErrOpen and no call", err, called)
	}
}

func TestHalfOpenAfterCooldownThenClosesOnSuccess(t *testing.T) {
	c := &clock{time.Unix(0, 0)}
	b := newBreaker(c)
	for i := 0; i < 3; i++ {
		_ = b.Do(fail)
	}
	c.add(59 * time.Second)
	if b.State() != breaker.Open {
		t.Fatal("half-open before the cool-down ended")
	}
	c.add(time.Second)
	if b.State() != breaker.HalfOpen {
		t.Fatalf("state %s after the cool-down, want half-open", b.State())
	}
	if err := b.Do(ok); err != nil || b.State() != breaker.Closed {
		t.Fatalf("trial success: err %v state %s, want closed", err, b.State())
	}
}

func TestHalfOpenFailureReopensImmediately(t *testing.T) {
	c := &clock{time.Unix(0, 0)}
	b := newBreaker(c)
	for i := 0; i < 3; i++ {
		_ = b.Do(fail)
	}
	c.add(time.Minute)
	_ = b.Do(fail)
	if b.State() != breaker.Open {
		t.Fatalf("one failed trial must reopen, state %s", b.State())
	}
}

func TestHalfOpenLetsOnlyOneTrialThrough(t *testing.T) {
	c := &clock{time.Unix(0, 0)}
	b := newBreaker(c)
	for i := 0; i < 3; i++ {
		_ = b.Do(fail)
	}
	c.add(time.Minute)
	release := make(chan struct{})
	started := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = b.Do(func() error { close(started); <-release; return nil })
	}()
	<-started
	if err := b.Do(ok); !errors.Is(err, breaker.ErrOpen) {
		t.Fatalf("second call during the trial got %v, want ErrOpen", err)
	}
	close(release)
	wg.Wait()
	if b.State() != breaker.Closed {
		t.Fatalf("state %s after the trial succeeded", b.State())
	}
}
