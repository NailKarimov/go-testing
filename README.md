# go-testing

[![Tests](https://github.com/NailKarimov/go-testing/actions/workflows/tests.yml/badge.svg)](https://github.com/NailKarimov/go-testing/actions/workflows/tests.yml)
[![Report](https://img.shields.io/badge/report-coverage-blue)](https://nailkarimov.github.io/go-testing/coverage.html)
[![Release](https://img.shields.io/github/v/release/NailKarimov/go-testing)](https://github.com/NailKarimov/go-testing/releases)

### ▶ Open online

| Page | What it is |
| --- | --- |
| **[Go resilience tests](https://nailkarimov.github.io/go-testing/go.html)** | interactive guide |
| [Coverage report](https://nailkarimov.github.io/go-testing/coverage.html) | latest CI run |

Click these links: opening an `.html` file in the file list shows its source code, GitHub does not run it.

Resilience and concurrency tests **in Go**, on two microservices of our own written with the standard library
only. `orders` calls `inventory` over HTTP through a **fault-injecting proxy**, which adds latency, answers 500,
drops connections or swallows requests. Each test checks what the user saw *and* what really happened in
inventory.

| | |
| --- | --- |
| **System under test** | own services `orders` and `inventory`, with idempotency keys, retries, timeouts and a circuit breaker |
| **Language and tools** | Go 1.24 · `testing` + `httptest` · race detector · native fuzzing · benchmarks · golangci-lint |
| **Types of testing** | functional API, resilience and chaos, concurrency, fuzzing, performance (benchmarks) |
| **Tests** | 25 tests + 18 table cases + a fuzz target; coverage of the four packages 94.5 % |
| **Test quality** | 14/14 planted bugs caught (`scripts/mutants.go`) |
| **Reports** | [coverage](https://nailkarimov.github.io/go-testing/coverage.html) · [visual guide](https://nailkarimov.github.io/go-testing/go.html) |

## What is tested

| Suite | Tool | What it proves |
| --- | --- | --- |
| `inventory` table tests | `httptest` | 3 + 15 cases: boundaries, malformed JSON, unknown fields, the body limit (413), 404 and 409 |
| `inventory` idempotency | `httptest` | the same `Idempotency-Key` reserves once and always gets the same answer, a refusal too |
| `inventory` concurrency | `-race` | 50 goroutines for 10 copies: exactly 10 × 201 and 40 × 409, never oversold |
| `FuzzParseReservation` | `go test -fuzz` | never panics, accepts only valid input, the round trip is lossless |
| `breaker` | fake clock | opens after 5 failures in a row, fails fast, half-open lets exactly one trial through |
| `orders` chaos scenarios | `chaos.Proxy` | a slow response is retried without a double reservation; a 500 burst; a reset after the reservation; a black hole bounded by timeouts; an outage opens the breaker and recovery closes it; 30 concurrent orders under faults stay consistent |
| benchmarks | `go test -bench` | `Reserve` and the handler, with `-benchmem`, in the CI summary |

## Findings

- **The fault injector needed a test of its own.** The first black-hole run hung the suite. A Go server notices
  that the client hung up only after the request body has been read, so the proxy now drains the body first.
- **golangci-lint in CI** found unchecked `resp.Body.Close` errors that `go vet` let through.
- **A missing mutex is caught only by the race detector.** All the numbers still added up.

## Running it

Requirements: Go 1.24.

```bash
go test -race ./...
go test ./inventory -run '^$' -fuzz FuzzParseReservation -fuzztime 30s
go test -run '^$' -bench . -benchmem ./inventory
go run scripts/mutants.go                 # mutation check
go run ./cmd/inventory & go run ./cmd/orders &
curl -d '{"sku":"BOOK-2","quantity":1}' localhost:8080/orders
```

## Project structure

```text
inventory/        stock service with idempotency keys; table, concurrency and fuzz tests
orders/           orders service: timeout, retries, circuit breaker; chaos scenarios
breaker/          circuit breaker with an injectable clock
chaos/            fault-injecting reverse proxy
cmd/              runnable inventory and orders servers
scripts/          mutation check
docs/             test plan, test report, guides
```

## CI

`.github/workflows/tests.yml` runs on push, pull request, nightly at 04:00 UTC and on demand:
- the `test` job runs gofmt, go vet and `go test -race` with cross-package coverage, then golangci-lint, and puts
  the benchmarks in the summary;
- the `fuzz` job fuzzes the reservation parser for 60 s;
- the `mutants` job runs the mutation check.

The coverage report and the guide are published on GitHub Pages.

Releases: committing `.github/releases/<tag>.md` runs the Release workflow, which creates the tag and the GitHub release with that file as its notes.

## Documentation

| Document | What it is |
| --- | --- |
| [`docs/test-plan.md`](docs/test-plan.md) | test plan (ISO/IEC/IEEE 29119-3) |
| [`docs/test-report.md`](docs/test-report.md) | test summary report: latest results, findings, recommendations |
| [`docs/go.md`](docs/go.md) | guide: the fault table, why the second half of each assertion matters, the mutation table |
| [`docs/go.html`](https://nailkarimov.github.io/go-testing/go.html) | the same guide, interactive: a fault switchboard and a circuit breaker to drive by hand |

## Part of a portfolio

One of 13 test automation projects by [Nail Karimov](https://github.com/NailKarimov):
[all projects](https://github.com/NailKarimov) ·
[testing map](https://nailkarimov.github.io/api-test-automation/testing-map.html) ·
[toolbox](https://nailkarimov.github.io/api-test-automation/toolbox.html).
