package main

import (
	"bytes"
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

	// Checkout — handles two callers transparently:
	//   1. Internal (checkout service): body has {user_id} only, cart already populated.
	//      → proxied straight to checkout service.
	//   2. vm-client failover: body has {user_id, product_id, quantity}.
	//      → adds item to cart first, then triggers checkout.
	//      This mirrors what vm-web does so the failover is transparent to vm-client.
	r.HandleFunc("/checkout", checkoutHandler(client, cartURL, checkoutURL)).Methods(http.MethodPost)

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

// checkoutHandler handles POST /checkout for two callers:
//   - Internal path: body is {user_id} — cart already has items, proxy straight to checkout service.
//   - Failover path (vm-client): body is {user_id, product_id, quantity} — add to cart first,
//     then trigger checkout. This makes the Consul service-resolver failover transparent to vm-client.
func checkoutHandler(client *http.Client, cartURL, checkoutURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		span := tracing.SpanFromContext(r)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, `{"error":"failed to read body"}`, http.StatusBadRequest)
			return
		}

		var req struct {
			UserID    string `json:"user_id"`
			ProductID string `json:"product_id"`
			Quantity  int    `json:"quantity"`
		}
		_ = json.Unmarshal(body, &req)

		if req.UserID == "" {
			http.Error(w, `{"error":"user_id required"}`, http.StatusBadRequest)
			return
		}
		tracing.Tag(span, "user.id", req.UserID)
		tracing.Tag(span, "dc", "ocp-dc")

		// If product_id is present this is the vm-client failover path — add to cart first.
		if req.ProductID != "" {
			tracing.Tag(span, "product.id", req.ProductID)
			tracing.SpanLog(r.Context(), "INFO", "failover path: adding to cart before checkout",
				"product.id", req.ProductID)
			qty := req.Quantity
			if qty <= 0 {
				qty = 1
			}
			cartBody, _ := json.Marshal(map[string]interface{}{
				"product_id": req.ProductID,
				"quantity":   qty,
			})
			cartReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
				cartURL+"/cart/"+req.UserID+"/items", bytes.NewReader(cartBody))
			if err != nil {
				http.Error(w, fmt.Sprintf(`{"error":"cart request failed: %v"}`, err), http.StatusInternalServerError)
				return
			}
			cartReq.Header.Set("Content-Type", "application/json")
			cartResp, err := client.Do(cartReq)
			if err != nil {
				tracing.SetError(span, "add-to-cart failed: "+err.Error())
				http.Error(w, fmt.Sprintf(`{"error":"add-to-cart failed: %v"}`, err), http.StatusBadGateway)
				return
			}
			cartResp.Body.Close()
			if cartResp.StatusCode < 200 || cartResp.StatusCode >= 300 {
				tracing.SetError(span, fmt.Sprintf("cart returned %d", cartResp.StatusCode))
				http.Error(w, fmt.Sprintf(`{"error":"cart returned %d"}`, cartResp.StatusCode), cartResp.StatusCode)
				return
			}
		}

		// Trigger checkout — cart now has items.
		checkoutBody, _ := json.Marshal(map[string]string{"user_id": req.UserID})
		checkoutReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
			checkoutURL+"/checkout", bytes.NewReader(checkoutBody))
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"checkout request failed: %v"}`, err), http.StatusInternalServerError)
			return
		}
		checkoutReq.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(checkoutReq)
		if err != nil {
			tracing.SetError(span, "checkout failed: "+err.Error())
			http.Error(w, fmt.Sprintf(`{"error":"checkout failed: %v"}`, err), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		// Wrap the checkout response to include dc=ocp-dc so the client log shows the failover.
		var order map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&order); err != nil || resp.StatusCode >= 300 {
			w.WriteHeader(resp.StatusCode)
			return
		}
		order["dc"] = "ocp-dc"
		order["service"] = "frontend"
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_ = json.NewEncoder(w).Encode(order)
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
