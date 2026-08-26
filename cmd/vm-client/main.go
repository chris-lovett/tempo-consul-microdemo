// vm-client is the traffic-generating service for the EC2 (vm-dc) demo node.
//
// It POSTs a shop order to vm-web every second, cycling through products.
// vm-web looks up the price locally (vm-api), then delegates the full
// cart+checkout+payment pipeline to ocp-dc frontend via the Consul mesh gateway.
//
// When vm-web fails its health check, Consul's ServiceResolver transparently
// reroutes the upstream port to ocp-dc frontend — the client code never changes.
// The W3C traceparent header is propagated through every hop so the complete
// waterfall (client → web → api → frontend → checkout → cart → inventory → payment)
// appears under one trace ID in Grafana Tempo.
//
// Build:
//
//	GOOS=linux GOARCH=amd64 go build -o vm-client ./cmd/vm-client
//
// Run:
//
//	SERVICE_NAME=client PORT=9080 \
//	UPSTREAM_URI=http://localhost:9095 \
//	OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317 \
//	./vm-client
package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/mux"

	"github.com/chris-lovett/tempo-consul-microdemo/pkg/httpx"
	"github.com/chris-lovett/tempo-consul-microdemo/pkg/tracing"
)

// products cycles through the catalog so each request exercises a different item.
var products = []struct {
	id   string
	name string
}{
	{"prod-1", "Wireless Headphones"},
	{"prod-2", "Mechanical Keyboard"},
	{"prod-3", "Running Shoes"},
	{"prod-4", "Yoga Mat"},
	{"prod-5", "Water Bottle"},
	{"prod-7", "USB-C Hub"},
}

// orderResp is the shape returned by vm-web /checkout.
type orderResp struct {
	Service       string  `json:"service"`
	DC            string  `json:"dc"`
	UserID        string  `json:"user_id"`
	Product       string  `json:"product"`
	OrderID       string  `json:"order_id"`
	Total         float64 `json:"total"`
	PaymentStatus string  `json:"payment_status"`
}

func main() {
	serviceName := getenv("SERVICE_NAME", "client")
	port := getenv("PORT", "9080")
	// UPSTREAM_URI points at the Envoy local bind port for web (9095).
	// Consul's ServiceResolver handles failover to ocp-dc frontend transparently —
	// this app code never changes regardless of which datacenter serves the request.
	upstreamURI := getenv("UPSTREAM_URI", "http://localhost:9095")
	userID := getenv("USER_ID", "demo-user")
	log.SetPrefix("[" + serviceName + "] ")

	tracer, cleanup := tracing.Init(serviceName)
	defer cleanup()

	client := httpx.NewClient(tracer)

	r := mux.NewRouter()
	r.Use(tracing.Middleware(tracer))

	r.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "service": serviceName})
	}).Methods(http.MethodGet)

	// Trigger a single checkout manually.
	r.HandleFunc("/order", func(w http.ResponseWriter, r *http.Request) {
		span := tracing.SpanFromContext(r)
		tracing.Tag(span, "service.dc", "vm-dc")
		result, code := placeOrder(r, client, upstreamURI, userID, "prod-1", 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(result)
	}).Methods(http.MethodPost)

	// Start continuous traffic loop in the background.
	go trafficLoop(client, upstreamURI, userID)

	addr := ":" + port
	log.Printf("listening on %s (upstream=%s)", addr, upstreamURI)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

// trafficLoop posts one checkout per second, cycling through products.
// Log lines are prefixed with the result so the demo traffic tail is readable:
//
//	20:40:31 dc=vm-dc  product="Wireless Headphones" order=ord-abc123 payment=authorized total=79.99
//	20:40:33 dc=unknown http=502  ← failover in progress
//	20:40:34 dc=ocp-dc  product="Wireless Headphones" order=ord-def456 payment=authorized total=79.99
func trafficLoop(client *http.Client, upstreamURI, userID string) {
	idx := 0
	for {
		p := products[idx%len(products)]
		idx++

		body, _ := json.Marshal(map[string]interface{}{
			"user_id":    userID,
			"product_id": p.id,
			"quantity":   1,
		})

		req, err := http.NewRequest(http.MethodPost, upstreamURI+"/checkout", bytes.NewReader(body))
		if err != nil {
			log.Printf("build request error: %v", err)
			time.Sleep(time.Second)
			continue
		}
		req.Header.Set("Content-Type", "application/json")

		start := time.Now()
		resp, err := client.Do(req)
		elapsed := time.Since(start).Round(time.Millisecond)

		ts := time.Now().Format("15:04:05")
		if err != nil {
			log.Printf("%s upstream_error=%v elapsed=%s", ts, err, elapsed)
			time.Sleep(time.Second)
			continue
		}

		var result orderResp
		_ = json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()

		dc := result.DC
		if dc == "" {
			dc = "unknown"
		}
		product := result.Product
		if product == "" {
			product = p.name
		}

		log.Printf("%s http=%d dc=%-8s product=%q order=%s payment=%s total=%.2f elapsed=%s",
			ts, resp.StatusCode, dc, product,
			result.OrderID, result.PaymentStatus, result.Total, elapsed)

		time.Sleep(time.Second)
	}
}

// placeOrder issues a single checkout request from within an HTTP handler context,
// so the trace context from the incoming request is propagated to the upstream call.
func placeOrder(r *http.Request, client *http.Client, upstreamURI, userID, productID string, quantity int) (interface{}, int) {
	body, _ := json.Marshal(map[string]interface{}{
		"user_id":    userID,
		"product_id": productID,
		"quantity":   quantity,
	})
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		upstreamURI+"/checkout", bytes.NewReader(body))
	if err != nil {
		return map[string]string{"error": err.Error()}, http.StatusInternalServerError
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return map[string]string{"error": err.Error()}, http.StatusBadGateway
	}
	defer resp.Body.Close()
	var result orderResp
	_ = json.NewDecoder(resp.Body).Decode(&result)
	return result, resp.StatusCode
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
