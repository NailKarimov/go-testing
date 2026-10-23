# Test summary report: orders and inventory (go-testing)

The structure follows ISO/IEC/IEEE 29119-3 (test completion report), shortened.

- **Period:** 22.10.2026 – 23.10.2026
- **Build:** `main`, latest nightly run in GitHub Actions
- **Report:** https://nailkarimov.github.io/go-testing/coverage.html

## 1. Summary

Every chaos scenario passes. No order is reserved twice and none is lost, whether the response is slow, the
connection resets after the work is done, or inventory is down. The circuit breaker opens and recovers as
designed.

## 2. Results

| Metric | Value |
| --- | --- |
| Tests | 25 tests + 18 table cases + the fuzz seed corpus, all passing with `-race` |
| Coverage | 94.5 % of `breaker`, `chaos`, `inventory`, `orders` |
| Fuzzing | 60 s per CI run, ~4 600 inputs per second, no failures |
| Benchmarks | `Reserve` ≈ 2.4 µs and 6 allocations; the handler ≈ 10 µs |
| Mutation check | 14 of 14 planted bugs caught |

## 3. Findings

| ID | Severity | Summary | Status |
| --- | --- | --- | --- |
| GO-1 | major (test tool) | the black-hole fault hung the suite: the server noticed the client hanging up only after reading the body | fixed in `chaos.Proxy` |
| GO-2 | minor | unchecked `resp.Body.Close` errors, found by golangci-lint in CI | fixed |

## 4. Deviations from the plan

None.

## 5. Recommendations

1. Keep the "what inventory reserved" half of every scenario. Mutant M1 (retries without the key) passes every
   user-facing check and is caught only there.
2. Run the race detector in every CI run. A missing mutex is visible to nothing else.
