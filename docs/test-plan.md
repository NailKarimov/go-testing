# Test plan: orders and inventory (go-testing)

The structure follows ISO/IEC/IEEE 29119-3 (test plan), shortened for one project.

## 1. Context

- **System under test:** two services of our own, written in Go with the standard library. `inventory` reserves
  stock and is idempotent by key. `orders` calls inventory with a per-attempt timeout, retries with backoff and
  a circuit breaker.
- **Goal:** show that an order is never lost and never reserved twice, whatever the network does between the
  services. Prove that the tests catch real bugs.
- **References:** ISO/IEC 25010 (reliability), Principles of Chaos Engineering, the Go memory model (data races).

## 2. Scope

| In scope | Out of scope |
| --- | --- |
| the inventory API: validation, reservations, idempotency | persistence (stock is in memory) |
| the orders API and its client: timeouts, retries, breaker | authentication |
| network faults: latency, 500, reset, black hole, outage | real infrastructure faults (k8s, DNS), covered in microservices-testing |
| concurrency of reservations and orders | load tests (only micro-benchmarks here) |

## 3. Quality characteristics (ISO/IEC 25010)

| Characteristic | Covered by |
| --- | --- |
| Functional correctness | table tests, idempotency tests |
| Reliability (fault tolerance, recoverability) | chaos scenarios through the fault-injecting proxy, circuit breaker tests |
| Reliability (integrity under concurrency) | `-race` tests: 50 goroutines, 30 orders under faults |
| Robustness | native fuzzing of the parser |
| Performance efficiency | benchmarks with `-benchmem` |

## 4. Approach

- **Levels:** handler tests with `httptest.NewRecorder`, and scenarios with three real servers on loopback:
  orders → proxy → inventory.
- **Techniques:**
  - boundary values;
  - fault injection, one fault per scenario;
  - a fake clock for the breaker;
  - assertions on both sides: what the user got and what inventory reserved.
- **Test data:** every scenario builds its own servers and stock.
- **Test quality:** `scripts/mutants.go` plants 14 bugs and runs `go test -race ./...` for each.

## 5. Risks

| Risk | Mitigation |
| --- | --- |
| Timing-based tests are flaky on CI runners | generous bounds (black hole < 1.5 s for a ≈ 0.5 s budget), `-count=5` checked locally |
| A hanging fault (black hole) blocks the suite | the proxy drains the body and gives up after a minute; `-timeout 120s` |
| Races hidden by luck | every test run uses `-race` |

## 6. Entry and exit criteria

- **Entry:** gofmt, go vet and golangci-lint are clean.
- **Exit:** all tests pass under `-race`; fuzzing for 60 s finds nothing; every planted bug is caught.

## 7. Deliverables

The test suite, the coverage report on GitHub Pages, [`test-report.md`](test-report.md), [`go.md`](go.md) and
its visual guide.
