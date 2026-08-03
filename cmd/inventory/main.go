package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"sync"

	"github.com/gorilla/mux"

	"github.com/chris-lovett/tempo-consul-microdemo/pkg/models"
	"github.com/chris-lovett/tempo-consul-microdemo/pkg/tracing"
)

// initialStock defines hardcoded starting quantities for each product.
var initialStock = map[string]int{
	"prod-1": 150,
	"prod-2": 75,
	"prod-3": 200,
	"prod-4": 120,
	"prod-5": 300,
	"prod-6": 90,
	"prod-7": 60,
	"prod-8": 180,
}

type inventoryStore struct {
	mu             sync.Mutex
	stock          map[string]int
	contentionRate float64
}

func newInventoryStore(contentionRate float64) *inventoryStore {
	stock := make(map[string]int, len(initialStock))
	for k, v := range initialStock {
		stock[k] = v
	}
	return &inventoryStore{stock: stock, contentionRate: contentionRate}
}

func (s *inventoryStore) reserve(productID string, quantity int) (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Simulate stock contention.
	if rand.Float64() < s.contentionRate {
		return false, fmt.Sprintf("stock contention for product %s, try again", productID)
	}

	qty, ok := s.stock[productID]
	if !ok {
		return false, fmt.Sprintf("product %s not found in inventory", productID)
	}
	if qty < quantity {
		return false, fmt.Sprintf("insufficient stock: have %d, need %d", qty, quantity)
	}
	s.stock[productID] = qty - quantity
	return true, fmt.Sprintf("reserved %d units of %s", quantity, productID)
}

func (s *inventoryStore) getStock(productID string) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	qty, ok := s.stock[productID]
	return qty, ok
}

func main() {
	serviceName := getenv("SERVICE_NAME", "inventory")
	port := getenv("PORT", "8085")
	contentionRate := parseFloat(getenv("INVENTORY_CONTENTION_RATE", "0.05"))
	log.SetPrefix("[" + serviceName + "] ")

	store := newInventoryStore(contentionRate)

	tracer, cleanup := tracing.Init(serviceName)
	defer cleanup()

	r := mux.NewRouter()
	r.Use(tracing.Middleware(tracer))

	r.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(models.HealthResponse{Status: "ok", Service: serviceName})
	}).Methods(http.MethodGet)

	r.HandleFunc("/reserve", reserveHandler(store)).Methods(http.MethodPost)
	r.HandleFunc("/stock/{product_id}", stockHandler(store)).Methods(http.MethodGet)
	r.HandleFunc("/admin/config", adminConfigHandler(store)).Methods(http.MethodPost)

	addr := ":" + port
	log.Printf("listening on %s (contentionRate=%.2f)", addr, contentionRate)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func reserveHandler(store *inventoryStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		span := tracing.SpanFromContext(r)

		var req models.InventoryReservation
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		tracing.Tag(span, "product.id", req.ProductID)

		ok, msg := store.reserve(req.ProductID, req.Quantity)
		tracing.Tag(span, "reservation.success", fmt.Sprintf("%v", ok))
		if !ok {
			tracing.SetError(span, msg)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(models.InventoryResponse{Success: ok, Message: msg})
	}
}

func stockHandler(store *inventoryStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		productID := mux.Vars(r)["product_id"]
		span := tracing.SpanFromContext(r)
		tracing.Tag(span, "product.id", productID)

		qty, ok := store.getStock(productID)
		if !ok {
			http.Error(w, `{"error":"product not found"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"product_id": productID,
			"quantity":   qty,
		})
	}
}

// adminConfigHandler allows live adjustment of the contention rate for demos.
func adminConfigHandler(store *inventoryStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ContentionRate float64 `json:"contention_rate"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		store.mu.Lock()
		store.contentionRate = body.ContentionRate
		store.mu.Unlock()
		log.Printf("config updated: contentionRate=%.2f", body.ContentionRate)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"contention_rate": body.ContentionRate,
		})
	}
}

func parseFloat(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0.05
	}
	return v
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
