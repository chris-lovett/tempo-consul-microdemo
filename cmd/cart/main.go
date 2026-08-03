package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"

	"github.com/gorilla/mux"

	"github.com/chris-lovett/tempo-consul-microdemo/pkg/httpx"
	"github.com/chris-lovett/tempo-consul-microdemo/pkg/models"
	"github.com/chris-lovett/tempo-consul-microdemo/pkg/tracing"
)

// cartStore holds all in-memory carts, keyed by user ID.
var (
	cartStore = make(map[string]*models.Cart)
	cartMu    sync.RWMutex
)

func main() {
	serviceName := getenv("SERVICE_NAME", "cart")
	port := getenv("PORT", "8082")
	catalogURL := getenv("CATALOG_URL", "http://localhost:8081")
	log.SetPrefix("[" + serviceName + "] ")

	tracer, cleanup := tracing.Init(serviceName)
	defer cleanup()

	client := httpx.NewClient(tracer)

	r := mux.NewRouter()
	r.Use(tracing.Middleware(tracer))

	r.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(models.HealthResponse{Status: "ok", Service: serviceName})
	}).Methods(http.MethodGet)

	r.HandleFunc("/cart/{user_id}", getCart).Methods(http.MethodGet)
	r.HandleFunc("/cart/{user_id}/items", addItem(client, catalogURL)).Methods(http.MethodPost)
	r.HandleFunc("/cart/{user_id}", clearCart).Methods(http.MethodDelete)

	addr := ":" + port
	log.Printf("listening on %s", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func getCart(w http.ResponseWriter, r *http.Request) {
	userID := mux.Vars(r)["user_id"]
	span := tracing.SpanFromContext(r)
	tracing.Tag(span, "user.id", userID)

	cartMu.RLock()
	cart, ok := cartStore[userID]
	cartMu.RUnlock()

	if !ok {
		cart = &models.Cart{UserID: userID, Items: []models.CartItem{}}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(cart)
}

func addItem(client *http.Client, catalogURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := mux.Vars(r)["user_id"]
		span := tracing.SpanFromContext(r)
		tracing.Tag(span, "user.id", userID)
		tracing.Tag(span, "cart.id", userID)

		var req struct {
			ProductID string `json:"product_id"`
			Quantity  int    `json:"quantity"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		tracing.Tag(span, "product.id", req.ProductID)

		// Fetch product details from catalog.
		catalogReq, _ := http.NewRequestWithContext(r.Context(), http.MethodGet,
			fmt.Sprintf("%s/products/%s", catalogURL, req.ProductID), nil)
		resp, err := client.Do(catalogReq)
		if err != nil {
			tracing.SetError(span, err.Error())
			http.Error(w, `{"error":"catalog unavailable"}`, http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			tracing.SetError(span, "product not found")
			http.Error(w, `{"error":"product not found"}`, http.StatusNotFound)
			return
		}

		var product models.Product
		if err := json.NewDecoder(resp.Body).Decode(&product); err != nil {
			tracing.SetError(span, err.Error())
			http.Error(w, `{"error":"invalid catalog response"}`, http.StatusInternalServerError)
			return
		}

		item := models.CartItem{
			ProductID: product.ID,
			Quantity:  req.Quantity,
			Price:     product.Price,
			Name:      product.Name,
		}

		cartMu.Lock()
		cart, ok := cartStore[userID]
		if !ok {
			cart = &models.Cart{UserID: userID, Items: []models.CartItem{}}
			cartStore[userID] = cart
		}
		// Merge if product already in cart.
		merged := false
		for i, ci := range cart.Items {
			if ci.ProductID == item.ProductID {
				cart.Items[i].Quantity += item.Quantity
				merged = true
				break
			}
		}
		if !merged {
			cart.Items = append(cart.Items, item)
		}
		// Recalculate total.
		total := 0.0
		for _, ci := range cart.Items {
			total += ci.Price * float64(ci.Quantity)
		}
		cart.Total = total
		cartMu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		cartMu.RLock()
		_ = json.NewEncoder(w).Encode(cart)
		cartMu.RUnlock()
	}
}

func clearCart(w http.ResponseWriter, r *http.Request) {
	userID := mux.Vars(r)["user_id"]
	span := tracing.SpanFromContext(r)
	tracing.Tag(span, "user.id", userID)

	cartMu.Lock()
	delete(cartStore, userID)
	cartMu.Unlock()

	w.WriteHeader(http.StatusNoContent)
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
