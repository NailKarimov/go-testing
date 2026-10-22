// Command orders runs the orders service in front of an inventory service (INVENTORY_URL).
package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/NailKarimov/go-testing/orders"
)

func main() {
	addr := envOr("ADDR", ":8080")
	svc := orders.New(orders.Config{InventoryURL: envOr("INVENTORY_URL", "http://localhost:8081")})
	srv := &http.Server{Addr: addr, Handler: svc.Handler(), ReadHeaderTimeout: 5 * time.Second}
	log.Printf("orders on %s", addr)
	log.Fatal(srv.ListenAndServe())
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
