// Command inventory runs the inventory service. Stock is seeded with a few SKUs.
package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/NailKarimov/go-testing/inventory"
)

func main() {
	addr := envOr("ADDR", ":8081")
	store := inventory.NewStore(map[string]int{"BOOK-1": 10, "BOOK-2": 3, "PEN-1": 100})
	srv := &http.Server{Addr: addr, Handler: store.Handler(), ReadHeaderTimeout: 5 * time.Second}
	log.Printf("inventory on %s", addr)
	log.Fatal(srv.ListenAndServe())
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
