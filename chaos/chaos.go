// Package chaos is a fault-injecting reverse proxy. It sits between two services and, per request, can add
// latency, answer 500, cut the connection, or swallow the request (black hole) — the faults a real network gives.
//
// Faults are set from the test through Set, so one scenario can switch them on and off mid-flight.
package chaos

import (
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

// Fault describes what the proxy does to the next requests.
type Fault struct {
	Latency   time.Duration // added before forwarding
	SlowFor   int           // if > 0, latency applies only to the next N requests
	Error500  int           // answer the next N requests with 500 without forwarding
	Reset     int           // forward the next N requests, then drop the connection before the response
	BlackHole bool          // never answer (until the client gives up)
	Down      bool          // refuse every request with a connection drop, as if the service were gone
}

// Proxy is the fault injector.
type Proxy struct {
	mu      sync.Mutex
	fault   Fault
	target  *httputil.ReverseProxy
	Seen    atomic.Int64 // requests that reached the proxy
	Dropped atomic.Int64 // requests answered by a fault instead of the target
}

// New creates a proxy in front of target.
func New(target string) *Proxy {
	u, err := url.Parse(target)
	if err != nil {
		panic(err)
	}
	return &Proxy{target: httputil.NewSingleHostReverseProxy(u)}
}

// Set replaces the current fault.
func (p *Proxy) Set(f Fault) {
	p.mu.Lock()
	p.fault = f
	p.mu.Unlock()
}

// take decides the fault for one request and consumes counters.
func (p *Proxy) take() (latency time.Duration, err500, reset, hole, down bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f := &p.fault
	latency, hole, down = f.Latency, f.BlackHole, f.Down
	if f.SlowFor > 0 {
		f.SlowFor--
		if f.SlowFor == 0 {
			f.Latency = 0
		}
	}
	if f.Error500 > 0 {
		f.Error500--
		err500 = true
	} else if f.Reset > 0 {
		f.Reset--
		reset = true
	}
	return
}

func hijackClose(w http.ResponseWriter) {
	if hj, ok := w.(http.Hijacker); ok {
		if conn, _, err := hj.Hijack(); err == nil {
			_ = conn.Close()
			return
		}
	}
	panic(http.ErrAbortHandler)
}

// ServeHTTP applies the fault, then forwards.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.Seen.Add(1)
	latency, err500, reset, hole, down := p.take()
	if down {
		p.Dropped.Add(1)
		hijackClose(w)
		return
	}
	if hole {
		p.Dropped.Add(1)
		// Read the body first: the server only notices the client hanging up once the body is consumed.
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(time.Minute):
		}
		return
	}
	if latency > 0 {
		select {
		case <-time.After(latency):
		case <-r.Context().Done():
			// The client gave up. The request is still forwarded, the way a slow network still delivers it:
			// that is what makes a retry after a timeout dangerous without an idempotency key.
		}
	}
	if err500 {
		p.Dropped.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"injected"}`))
		return
	}
	if reset {
		// Forward for real (the reservation happens), then drop the connection instead of the response.
		p.Dropped.Add(1)
		rec := &discard{header: http.Header{}}
		p.target.ServeHTTP(rec, r.Clone(contextWithoutCancel(r)))
		hijackClose(w)
		return
	}
	p.target.ServeHTTP(w, r.Clone(contextWithoutCancel(r)))
}

type discard struct{ header http.Header }

func (d *discard) Header() http.Header         { return d.header }
func (d *discard) Write(b []byte) (int, error) { return len(b), nil }
func (d *discard) WriteHeader(int)             {}
