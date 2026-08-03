package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"

	"github.com/gorilla/mux"

	"github.com/chris-lovett/tempo-consul-microdemo/pkg/models"
	"github.com/chris-lovett/tempo-consul-microdemo/pkg/tracing"
)

var catalog = []models.Product{
	{ID: "prod-1", Name: "Wireless Headphones", Description: "Noise-cancelling over-ear headphones with 30h battery", Price: 79.99, Category: "electronics"},
	{ID: "prod-2", Name: "Mechanical Keyboard", Description: "TKL layout, Cherry MX Red switches, RGB backlight", Price: 129.99, Category: "electronics"},
	{ID: "prod-3", Name: "Running Shoes", Description: "Lightweight trail-running shoes with Vibram sole", Price: 94.95, Category: "clothing"},
	{ID: "prod-4", Name: "Yoga Mat", Description: "6mm non-slip TPE mat, 183 × 61 cm", Price: 34.99, Category: "sports"},
	{ID: "prod-5", Name: "Stainless Steel Water Bottle", Description: "750 ml double-wall vacuum insulated", Price: 24.95, Category: "outdoors"},
	{ID: "prod-6", Name: "Merino Wool Sweater", Description: "100% merino, machine-washable, mid-weight", Price: 89.00, Category: "clothing"},
	{ID: "prod-7", Name: "USB-C Hub 7-in-1", Description: "4K HDMI, 100W PD, 3×USB-A, SD/microSD", Price: 49.99, Category: "electronics"},
	{ID: "prod-8", Name: "Cast Iron Skillet", Description: "Pre-seasoned 10-inch skillet, oven-safe to 260 °C", Price: 39.95, Category: "kitchen"},
}

var catalogIndex map[string]models.Product

func init() {
	catalogIndex = make(map[string]models.Product, len(catalog))
	for _, p := range catalog {
		catalogIndex[p.ID] = p
	}
}

func main() {
	serviceName := getenv("SERVICE_NAME", "catalog")
	port := getenv("PORT", "8081")
	log.SetPrefix("[" + serviceName + "] ")

	tracer, cleanup := tracing.Init(serviceName)
	defer cleanup()

	r := mux.NewRouter()
	r.Use(tracing.Middleware(tracer))

	r.HandleFunc("/health", healthHandler(serviceName)).Methods(http.MethodGet)
	r.HandleFunc("/products", listProducts).Methods(http.MethodGet)
	r.HandleFunc("/products/{id}", getProduct).Methods(http.MethodGet)

	addr := ":" + port
	log.Printf("listening on %s", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func healthHandler(svc string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(models.HealthResponse{Status: "ok", Service: svc})
	}
}

func listProducts(w http.ResponseWriter, r *http.Request) {
	span := tracing.SpanFromContext(r)
	tracing.Tag(span, "product.count", "8")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(catalog)
}

func getProduct(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	span := tracing.SpanFromContext(r)
	tracing.Tag(span, "product.id", id)

	p, ok := catalogIndex[id]
	if !ok {
		tracing.SetError(span, "product not found")
		http.Error(w, `{"error":"product not found"}`, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(p)
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
