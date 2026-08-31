// vm-web is the front-facing HTTP service for the EC2 (vm-dc) demo node.
//
// Routes:
//
//	GET  /health               — liveness probe
//	GET  /                     — status page (shows service name + dc)
//	POST /checkout             — full checkout flow:
//	                             1. look up product price via vm-api (local, vm-dc span)
//	                             2. delegate full cart+payment to ocp-dc frontend (peer upstream)
//	                             The traceparent header crosses the mesh gateway so all spans
//	                             — including frontend → checkout → cart → payment — appear
//	                             under the same trace ID in Grafana Tempo.
//
// Environment variables:
//
//	SERVICE_NAME                   service name reported in traces (default: web)
//	PORT                           HTTP listen port (default: 9090)
//	API_URI                        vm-api base URL via Envoy upstream (default: http://localhost:9091)
//	FRONTEND_URI                   ocp-dc frontend base URL via Envoy peer upstream (default: http://localhost:9093)
//	OTEL_EXPORTER_OTLP_ENDPOINT    OTel Collector gRPC endpoint (default: otel-collector:4317)
//
// Build:
//
//	GOOS=linux GOARCH=amd64 go build -o vm-web ./cmd/vm-web
//
// Run:
//
//	SERVICE_NAME=web PORT=9090 \
//	API_URI=http://localhost:9091 \
//	FRONTEND_URI=http://localhost:9093 \
//	OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317 \
//	./vm-web
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/mux"

	"github.com/chris-lovett/tempo-consul-microdemo/pkg/httpx"
	"github.com/chris-lovett/tempo-consul-microdemo/pkg/tracing"
)

// Product is the vm-api product response shape.
type Product struct {
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	Price float64 `json:"price"`
}

// checkoutResp is the shape returned by ocp-dc frontend /checkout.
type checkoutResp struct {
	OrderID       string  `json:"order_id"`
	Total         float64 `json:"total"`
	PaymentStatus string  `json:"payment_status"`
}

// clientRequest is the inbound body from vm-client.
type clientRequest struct {
	UserID    string `json:"user_id"`
	ProductID string `json:"product_id"`
	Quantity  int    `json:"quantity"`
}

func main() {
	serviceName := getenv("SERVICE_NAME", "web")
	port := getenv("PORT", "9090")
	apiURI := getenv("API_URI", "http://localhost:9091")
	frontendURI := getenv("FRONTEND_URI", "http://localhost:9093")
	log.SetPrefix("[" + serviceName + "] ")

	tracer, cleanup := tracing.Init(serviceName)
	defer cleanup()

	client := httpx.NewClient(tracer)

	r := mux.NewRouter()
	r.Use(tracing.Middleware(tracer))

	// Health
	r.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "service": serviceName})
	}).Methods(http.MethodGet)

	// Status — used by the demo traffic loop to show which DC is serving.
	// Returns the service name and datacenter so the loop output is readable.
	r.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		span := tracing.SpanFromContext(r)
		tracing.Tag(span, "service.dc", "vm-dc")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"name": serviceName,
			"dc":   "vm-dc",
		})
	}).Methods(http.MethodGet)

	// Checkout — cross-DC flow:
	//   vm-client → vm-web → vm-api (price lookup) → frontend (peer) → checkout → cart → inventory → payment
	// The same traceparent header crosses every hop.
	r.HandleFunc("/checkout", checkoutHandler(client, serviceName, apiURI, frontendURI)).Methods(http.MethodPost)

	addr := ":" + port
	log.Printf("listening on %s (api=%s frontend=%s)", addr, apiURI, frontendURI)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func checkoutHandler(client *http.Client, serviceName, apiURI, frontendURI string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		span := tracing.SpanFromContext(r)
		tracing.Tag(span, "service.dc", "vm-dc")

		var req clientRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.UserID == "" {
			http.Error(w, `{"error":"user_id and product_id required"}`, http.StatusBadRequest)
			return
		}
		if req.Quantity <= 0 {
			req.Quantity = 1
		}
		tracing.Tag(span, "user.id", req.UserID)
		tracing.Tag(span, "product.id", req.ProductID)
		tracing.SpanLog(r.Context(), "INFO", "checkout started",
			"user.id", req.UserID, "product.id", req.ProductID)

		// Step 1 — resolve product price from vm-api.
		// This is a local call within vm-dc; the span shows up in Tempo tagged dc=vm-dc.
		product, err := fetchProduct(r.Context(), client, apiURI, req.ProductID)
		if err != nil {
			tracing.SetError(span, "api lookup failed: "+err.Error())
			http.Error(w, fmt.Sprintf(`{"error":"product lookup failed: %v"}`, err), http.StatusBadGateway)
			return
		}
		tracing.Tag(span, "product.name", product.Name)
		tracing.Tag(span, "product.price", fmt.Sprintf("%.2f", product.Price))

		// Step 2 — add item to cart via ocp-dc frontend (peer upstream).
		// frontend exposes POST /cart/{user_id}/items which proxies to the cart service.
		if err := addToCart(r.Context(), client, frontendURI, req.UserID, req.ProductID, req.Quantity); err != nil {
			tracing.SetError(span, "add-to-cart failed: "+err.Error())
			http.Error(w, fmt.Sprintf(`{"error":"add-to-cart failed: %v"}`, err), http.StatusBadGateway)
			return
		}

		// Step 3 — trigger checkout via ocp-dc frontend.
		// frontend POST /checkout delegates to checkout → cart → inventory → payment.
		// The traceparent header injected by httpx.NewClient() ensures all those
		// spans appear as children of this web span in Grafana Tempo.
		order, err := triggerCheckout(r.Context(), client, frontendURI, req.UserID)
		if err != nil {
			tracing.SetError(span, "checkout failed: "+err.Error())
			http.Error(w, fmt.Sprintf(`{"error":"checkout failed: %v"}`, err), http.StatusBadGateway)
			return
		}
		tracing.Tag(span, "order.id", order.OrderID)
		tracing.Tag(span, "payment.status", order.PaymentStatus)

		duration := time.Since(start)
		tracing.SpanLog(r.Context(), "INFO", "checkout complete",
			"order.id", order.OrderID, "total", fmt.Sprintf("%.2f", order.Total),
			"payment.status", order.PaymentStatus, "duration", duration.String())

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"service":        serviceName,
			"dc":             "vm-dc",
			"user_id":        req.UserID,
			"product":        product.Name,
			"order_id":       order.OrderID,
			"total":          order.Total,
			"payment_status": order.PaymentStatus,
			"duration":       duration.String(),
		})
	}
}

func fetchProduct(ctx context.Context, client *http.Client, apiURI, productID string) (*Product, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURI+"/products/"+productID, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("api returned %d", resp.StatusCode)
	}
	var p Product
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

func addToCart(ctx context.Context, client *http.Client, frontendURI, userID, productID string, quantity int) error {
	body, _ := json.Marshal(map[string]interface{}{
		"product_id": productID,
		"quantity":   quantity,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		frontendURI+"/cart/"+userID+"/items", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errBody map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		return fmt.Errorf("cart returned %d: %s", resp.StatusCode, errBody["error"])
	}
	return nil
}

func triggerCheckout(ctx context.Context, client *http.Client, frontendURI, userID string) (*checkoutResp, error) {
	body, _ := json.Marshal(map[string]string{"user_id": userID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		frontendURI+"/checkout", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errBody map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		return nil, fmt.Errorf("checkout returned %d: %s", resp.StatusCode, errBody["error"])
	}
	var result checkoutResp
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
