# go-testing

[![Tests](https://github.com/NailKarimov/go-testing/actions/workflows/tests.yml/badge.svg)](https://github.com/NailKarimov/go-testing/actions/workflows/tests.yml)
[![Coverage](https://img.shields.io/badge/report-coverage-blue)](https://nailkarimov.github.io/go-testing/coverage.html)

Resilience and concurrency tests **in Go** on two microservices of our own (standard library only): `orders` calls
`inventory` over HTTP through a **fault-injecting proxy** that adds latency, answers 500, drops connections or
swallows requests. The tests check what the user sees *and* what really happened in inventory.

| Package / test | What it proves |
| --- | --- |
| `inventory` table tests | 3 + 15 cases with `httptest`: boundaries, malformed JSON, unknown fields, body limit (413), 404 / 409 |
| `inventory` idempotency | the same `Idempotency-Key` reserves once and always gets the same answer, also a refusal |
| `inventory` concurrency | 50 goroutines for 10 copies under `-race`: exactly 10 × 201 and 40 × 409, never oversold |
| `FuzzParseReservation` | native Go fuzzing: never panics, accepts only valid input, round trip is lossless |
| `breaker` | circuit breaker with a fake clock: opens after 5 failures in a row, fails fast, half-open lets **one** trial through |
| `orders` chaos scenarios | slow response → retry without a double reservation; 500 burst; connection reset after the reservation; black hole bounded by timeouts; outage opens the breaker, recovery closes it; 30 concurrent orders under faults stay consistent |
| benchmarks | `Reserve` and the handler, `-benchmem`, in the CI summary |

**Mutation check:** 14 planted bugs (no idempotency key, no lock, no per-attempt timeout, a breaker that never
recovers…), all caught — the missing lock only by the race detector. Coverage of the four packages: 94.5 %.
Details: [`docs/go.md`](docs/go.md) · visual guide: [`docs/go.html`](https://nailkarimov.github.io/go-testing/).

## Running it

```bash
go test -race ./...
go test ./inventory -run '^$' -fuzz FuzzParseReservation -fuzztime 30s
go test -run '^$' -bench . -benchmem ./inventory
go run scripts/mutants.go
```

Two terminals for a manual try: `go run ./cmd/inventory` and `go run ./cmd/orders`, then
`curl -d '{"sku":"BOOK-2","quantity":1}' localhost:8080/orders`.

Part of a QA portfolio: [github.com/NailKarimov](https://github.com/NailKarimov).
