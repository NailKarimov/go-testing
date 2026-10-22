# Testing in Go: resilience between services

Two services of our own, written with the standard library only:

- **inventory** — `GET /items/{sku}`, `POST /reservations` `{sku, quantity}` → 201 / 404 / 409. A request with an
  `Idempotency-Key` header is executed at most once; repeats get the stored answer.
- **orders** — `POST /orders` reserves stock in inventory, then stores the order. The call has three defences:
  a **timeout per attempt** (300 ms by default), **retries** with backoff for timeouts, dropped connections and
  5xx — all with the same idempotency key — and a **circuit breaker** (open after 5 failures in a row, fast 503,
  one trial call after the cool-down).

Between them in the tests sits **`chaos.Proxy`**, a reverse proxy that injects one fault at a time.

| Fault | What the proxy does | What the test checks |
| --- | --- | --- |
| `Latency` + `SlowFor: 1` | delays the first request beyond the timeout, but still delivers it | order confirmed, stock reduced **once**, 2 attempts |
| `Error500: 2` | answers 500 twice without forwarding | the third attempt succeeds |
| `Error500: 3` | more failures than attempts | 502 to the user, stock untouched |
| `Reset: 1` | forwards (the reservation happens), then drops the connection | retry gets the stored answer, stock reduced once |
| `BlackHole` | never answers | the order fails in ≈ 0.5 s, not "forever" |
| `Down` | refuses every connection | 5 × 502, breaker opens, the 6th order is a fast 503 and the proxy sees no request; after the cool-down a trial closes it |
| `Error500: 6, Reset: 6` with 30 concurrent orders | random mix of faults | confirmed units = reserved units = stock taken, never more than 10 |

## 1. What Go is good at here

- **`net/http/httptest`**: a handler can be tested without a network (`NewRecorder`), and a real server on a free
  port is one line (`NewServer`). The chaos scenarios start three of them per test in milliseconds.
- **The race detector** (`-race`): the concurrency tests are not only "the numbers add up" — any unsynchronised
  access fails the run. Removing the mutex from `Reserve` is caught by the race detector before any assertion.
- **Native fuzzing** (`go test -fuzz`): the parser gets ~4 600 random bodies a second; a failing input is saved to
  `testdata/fuzz/` and replayed as an ordinary test from then on.
- **Table-driven tests with `t.Run`**: 15 validation cases are 15 lines, each a named subtest.
- **Benchmarks** in the same files: `go test -bench . -benchmem` (`Reserve` ≈ 2.4 µs, 6 allocations).
- **A fake clock**: the breaker takes `Now func() time.Time`, so "after 30 s" is `clock.add(30 * time.Second)`
  and the tests never sleep.

## 2. Why the second half of each assertion matters

A retry test that only checks "the user got 201" passes even when the order reserved the books twice. The
dangerous cases are exactly the ones where the first request **did** arrive: a slow response and a connection
dropped after the work was done. So every scenario also reads inventory: units reserved, requests received, what
the proxy saw. Mutant M1 (retries without the idempotency key) passes every user-facing check and is caught only by
this second half.

## 3. Mutation check

`go run scripts/mutants.go` plants 14 bugs one at a time and runs `go test -race ./...`:

| # | Bug | Caught by |
| --- | --- | --- |
| M1 | retries without the idempotency key | slow-response scenario (reserved twice) |
| M2 | inventory ignores the key | idempotency test |
| M3 | no mutex in `Reserve` | race detector |
| M4 | oversell by one unit | validation table |
| M5 | 5xx not retried | 500-burst scenario |
| M6 | connection errors not retried | slow-response scenario |
| M7 | no per-attempt timeout | slow-response scenario |
| M8 | breaker opens one failure late | breaker test |
| M9 | half-open lets every call through | one-trial test |
| M10 | breaker never recovers | cool-down test |
| M11 | unknown fields accepted | validation table |
| M12 | no body size limit | validation table |
| M13 | out of stock reported as 502 | business-answer test |
| M14 | zero quantity accepted | validation table |

## 4. CI

`.github/workflows/tests.yml`: `gofmt`, `go vet`, golangci-lint (standard set + bodyclose, errorlint, noctx…),
tests with `-race` and coverage across packages (`-coverpkg`), benchmarks in the job summary, a 60-second fuzz job,
the mutation job; the HTML coverage report and this guide go to GitHub Pages.

## 5. A bug the tests found in the test tool

The first black-hole run hung the whole suite: the proxy waited for the client to hang up, and the client did, but
a Go server notices a closed connection only after the request body has been read. The proxy now drains the body
first (and gives up after a minute regardless). A fault injector needs tests of its own behaviour too.
