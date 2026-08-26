// vm-api is the local data service for the EC2 (vm-dc) node.
// It serves a small product catalog so vm-web can look up prices before
// forwarding a checkout request to ocp-dc. It has no further upstreams.
//
// Routes:
//   GET  /health                 — liveness probe
//   GET  /products               — list all products
//   GET  /products/{id}          — fetch a single product by ID
//
// Build:
//
//	GOOS=linux GOARCH=amd64 go build -o vm-api ./cmd/vm-api
//
// Run:
//
//	SERVICE_NAME=api PORT=9092 \
//	OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317 ./vm-api
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"

	"github.com/gorilla/mux"

	"github.com/chris-lovett/tempo-consul-microdemo/pkg/tracing"
)

// Product mirrors models.Product but is defined locally so vm-api has no
// dependency on the ocp-dc-only pkg/models package.
type Product struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Price       float64 `json:"price"`
	Category    string  `json:"category"`
}

// catalog is the vm-dc product list — intentionally matches the ocp-dc
// catalog service so prices are consistent across both datacenters.
var catalog = []Product{
	{ID: "prod-1", Name: "Wireless Headphones", Description: "Noise-cancelling over-ear headphones", Price: 79.99, Category: "electronics"},
	{ID: "prod-2", Name: "Mechanical Keyboard", Description: "TKL layout, Cherry MX Red switches, RGB", Price: 129.99, Category: "electronics"},
	{ID: "prod-3", Name: "Running Shoes", Description: "Lightweight trail-running shoes, Vibram sole", Price: 94.95, Category: "clothing"},
	{ID: "prod-4", Name: "Yoga Mat", Description: "6mm non-slip TPE mat, 183×61 cm", Price: 34.99, Category: "sports"},
	{ID: "prod-5", Name: "Stainless Steel Water Bottle", Description: "750 ml double-wall vacuum insulated", Price: 24.95, Category: "outdoors"},
	{ID: "prod-6", Name: "Merino Wool Sweater", Description: "100% merino, machine-washable, mid-weight", Price: 89.00, Category: "clothing"},
	{ID: "prod-7", Name: "USB-C Hub 7-in-1", Description: "4K HDMI, 100W PD, 3×USB-A, SD/microSD", Price: 49.99, Category: "electronics"},
	{ID: "prod-8", Name: "Cast Iron Skillet", Description: "Pre-seasoned 10-inch skillet, oven-safe to 260°C", Price: 39.95, Category: "kitchen"},
}

var catalogIndex map[string]Product

func init() {
	catalogIndex = make(map[string]Product, len(catalog))
	for _, p := range catalog {
		catalogIndex[p.ID] = p
	}
}

func main() {
	serviceName := getenv("SERVICE_NAME", "api")
	port := getenv("PORT", "9092")
	log.SetPrefix("[" + serviceName + "] ")

	tracer, cleanup := tracing.Init(serviceName)
	defer cleanup()

	r := mux.NewRouter()
	r.Use(tracing.Middleware(tracer))

	r.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "service": serviceName})
	}).Methods(http.MethodGet)

	r.HandleFunc("/products", func(w http.ResponseWriter, r *http.Request) {
		span := tracing.SpanFromContext(r)
		tracing.Tag(span, "service.dc", "vm-dc")
		tracing.Tag(span, "product.count", "8")
		tracing.SpanLog(r.Context(), "INFO", "listing products", "count", "8")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(catalog)
	}).Methods(http.MethodGet)

	r.HandleFunc("/products/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := mux.Vars(r)["id"]
		span := tracing.SpanFromContext(r)
		tracing.Tag(span, "service.dc", "vm-dc")
		tracing.Tag(span, "product.id", id)
		tracing.SpanLog(r.Context(), "INFO", "getting product", "product.id", id)

		p, ok := catalogIndex[id]
		if !ok {
			tracing.SetError(span, "product not found")
			http.Error(w, `{"error":"product not found"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(p)
	}).Methods(http.MethodGet)

	addr := ":" + port
	log.Printf("listening on %s", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
