package inventory_test

import (
	"encoding/json"
	"testing"

	"github.com/NailKarimov/go-testing/inventory"
)

// FuzzParseReservation feeds random bodies to the parser. Whatever comes in:
//   - it never panics;
//   - anything it accepts is valid (1-32 char sku, quantity 1-100);
//   - an accepted reservation survives a JSON round trip unchanged.
//
// go test ./inventory -run '^$' -fuzz FuzzParseReservation -fuzztime 30s
func FuzzParseReservation(f *testing.F) {
	for _, seed := range []string{
		`{"sku":"BOOK-1","quantity":1}`, `{"sku":"","quantity":1}`, `{"sku":"A","quantity":100}`,
		`{"sku":"A","quantity":101}`, `{"quantity":1e2,"sku":"A"}`, `{"sku":"\u0000","quantity":1}`,
		`[]`, `null`, `{"sku":"A","quantity":1}x`, `{"sku":"A","quantity":1,"sku":"B"}`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		r, err := inventory.ParseReservation(body)
		if err != nil {
			return
		}
		if r.Validate() != nil {
			t.Fatalf("accepted an invalid reservation %+v from %q", r, body)
		}
		raw, _ := json.Marshal(r)
		back, err := inventory.ParseReservation(raw)
		if err != nil || back != r {
			t.Fatalf("round trip changed %+v into %+v (%v)", r, back, err)
		}
	})
}
