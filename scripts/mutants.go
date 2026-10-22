//go:build ignore

// Mutation check: plants one bug at a time and runs the tests with -race; every bug must make them fail.
//
//	go run scripts/mutants.go           # all mutants
//	go run scripts/mutants.go M3 M7     # some
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type mutant struct{ id, file, old, new, what string }

var mutants = []mutant{
	{"M1", "orders/orders.go", `req.Header.Set("Idempotency-Key", key)`, `_ = key`, "retries are sent without the idempotency key"},
	{"M2", "inventory/inventory.go", `if prev, ok := s.seen[key]; ok && key != ""`, `if prev, ok := s.seen[key]; ok && false`, "inventory ignores the idempotency key"},
	{"M3", "inventory/inventory.go", "s.mu.Lock()\n\tdefer s.mu.Unlock()\n\ts.Requests++", "s.Requests++", "reservations without the lock (data race)"},
	{"M4", "inventory/inventory.go", "case n < r.Quantity:", "case n < r.Quantity-1:", "oversell by one unit"},
	{"M5", "orders/orders.go", "if resp.StatusCode >= 500 {", "if resp.StatusCode >= 600 {", "5xx are not retried"},
	{"M6", "orders/orders.go", "return reply{}, errRetryable{err}\n\t}\n\tdefer", "return reply{}, err\n\t}\n\tdefer", "connection errors are not retried"},
	{"M7", "orders/orders.go", "ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)", "ctx, cancel := context.WithCancel(ctx)", "no timeout per attempt"},
	{"M8", "breaker/breaker.go", "b.failures >= b.threshold", "b.failures > b.threshold", "breaker opens one failure late"},
	{"M9", "breaker/breaker.go", "case b.state == HalfOpen && b.trial:", "case false:", "half-open lets every call through"},
	{"M10", "breaker/breaker.go", "if b.state == Open && b.Now().Sub(b.openedAt) >= b.cooldown {", "if false {", "breaker never recovers"},
	{"M11", "inventory/inventory.go", "dec.DisallowUnknownFields()", "", "unknown fields accepted"},
	{"M12", "inventory/inventory.go", "if len(body) > 4096 {", "if len(body) > 1<<20 {", "no body size limit"},
	{"M13", "orders/orders.go", "case res.status == http.StatusConflict:", "case false:", "out of stock reported as 502"},
	{"M14", "inventory/inventory.go", "r.Quantity < 1 || r.Quantity > 100", "r.Quantity < 0 || r.Quantity > 100", "zero quantity accepted"},
}

func main() {
	want := map[string]bool{}
	for _, a := range os.Args[1:] {
		want[a] = true
	}
	survived := 0
	for _, m := range mutants {
		if len(want) > 0 && !want[m.id] {
			continue
		}
		src, err := os.ReadFile(m.file)
		if err != nil {
			panic(err)
		}
		if strings.Count(string(src), m.old) != 1 {
			panic(fmt.Sprintf("%s: pattern must occur exactly once in %s", m.id, m.file))
		}
		if err := os.WriteFile(m.file, []byte(strings.Replace(string(src), m.old, m.new, 1)), 0o644); err != nil {
			panic(err)
		}
		out, err := exec.Command("go", "test", "-race", "-count=1", "-timeout", "60s", "./...").CombinedOutput()
		_ = os.WriteFile(m.file, src, 0o644)
		caught := err != nil
		first := ""
		for _, ln := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(ln, "--- FAIL") || strings.Contains(ln, "build failed") || strings.Contains(ln, "DATA RACE") {
				first = strings.TrimSpace(ln)
				break
			}
		}
		status := "caught  "
		if !caught {
			status = "SURVIVED"
			survived++
		}
		fmt.Printf("%-4s %s %-45s %s\n", m.id, status, m.what, first)
	}
	if survived > 0 {
		fmt.Printf("\n%d survived\n", survived)
		os.Exit(1)
	}
	fmt.Println("\nAll mutants caught.")
}
