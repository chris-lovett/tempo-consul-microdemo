package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	"github.com/gorilla/mux"

	"github.com/chris-lovett/tempo-consul-microdemo/pkg/httpx"
	"github.com/chris-lovett/tempo-consul-microdemo/pkg/models"
	"github.com/chris-lovett/tempo-consul-microdemo/pkg/tracing"
)

func main() {
	serviceName := getenv("SERVICE_NAME", "frontend")
	port := getenv("PORT", "8080")
	catalogURL := getenv("CATALOG_URL", "http://localhost:8081")
	cartURL := getenv("CART_URL", "http://localhost:8082")
	checkoutURL := getenv("CHECKOUT_URL", "http://localhost:8083")
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

	// Product proxies
	r.HandleFunc("/products", proxy(client, catalogURL+"/products")).Methods(http.MethodGet)
	r.HandleFunc("/products/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := mux.Vars(r)["id"]
		span := tracing.SpanFromContext(r)
		tracing.Tag(span, "product.id", id)
		proxyTo(client, w, r, catalogURL+"/products/"+id)
	}).Methods(http.MethodGet)

	// Cart proxies
	r.HandleFunc("/cart/{user_id}", func(w http.ResponseWriter, r *http.Request) {
		userID := mux.Vars(r)["user_id"]
		span := tracing.SpanFromContext(r)
		tracing.Tag(span, "user.id", userID)
		proxyTo(client, w, r, cartURL+"/cart/"+userID)
	}).Methods(http.MethodGet, http.MethodDelete)

	r.HandleFunc("/cart/{user_id}/items", func(w http.ResponseWriter, r *http.Request) {
		userID := mux.Vars(r)["user_id"]
		span := tracing.SpanFromContext(r)
		tracing.Tag(span, "user.id", userID)
		proxyTo(client, w, r, cartURL+"/cart/"+userID+"/items")
	}).Methods(http.MethodPost)

	// Checkout proxy
	r.HandleFunc("/checkout", proxy(client, checkoutURL+"/checkout")).Methods(http.MethodPost)

	// Admin proxies for demo controls (passed through to downstream services)
	r.HandleFunc("/payment/admin/config", proxy(client, getenv("PAYMENT_URL", "http://localhost:8084")+"/admin/config")).Methods(http.MethodPost)
	r.HandleFunc("/inventory/admin/config", proxy(client, getenv("INVENTORY_URL", "http://localhost:8085")+"/admin/config")).Methods(http.MethodPost)
	r.HandleFunc("/inventory/admin/stock/reset", proxy(client, getenv("INVENTORY_URL", "http://localhost:8085")+"/admin/stock/reset")).Methods(http.MethodPost)

	addr := ":" + port
	log.Printf("listening on %s", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

// proxy returns a handler that forwards the request body to target and streams the response back.
func proxy(client *http.Client, target string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		proxyTo(client, w, r, target)
	}
}

func proxyTo(client *http.Client, w http.ResponseWriter, r *http.Request, target string) {
	tracing.SpanLog(r.Context(), "INFO", "proxying request", "target", target)
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		tracing.SpanLog(r.Context(), "ERROR", "proxy request failed", "error", err.Error())
		http.Error(w, fmt.Sprintf(`{"error":"%v"}`, err), http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", r.Header.Get("Content-Type"))

	resp, err := client.Do(req)
	if err != nil {
		span := tracing.SpanFromContext(r)
		tracing.SetError(span, err.Error())
		http.Error(w, fmt.Sprintf(`{"error":"%v"}`, err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
