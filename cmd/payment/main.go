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
	"time"

	"github.com/gorilla/mux"

	"github.com/chris-lovett/tempo-consul-microdemo/pkg/models"
	"github.com/chris-lovett/tempo-consul-microdemo/pkg/tracing"
)

// config holds runtime-adjustable payment parameters.
type config struct {
	mu          sync.RWMutex
	failureRate float64
	latencyMS   int
}

func (c *config) get() (float64, int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.failureRate, c.latencyMS
}

func (c *config) set(failureRate float64, latencyMS int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failureRate = failureRate
	c.latencyMS = latencyMS
}

func main() {
	serviceName := getenv("SERVICE_NAME", "payment")
	port := getenv("PORT", "8084")
	log.SetPrefix("[" + serviceName + "] ")

	failureRate := parseFloat(getenv("PAYMENT_FAILURE_RATE", "0.02"))
	latencyMS := parseInt(getenv("PAYMENT_LATENCY_MS", "50"))

	cfg := &config{failureRate: failureRate, latencyMS: latencyMS}

	tracer, cleanup := tracing.Init(serviceName)
	defer cleanup()

	r := mux.NewRouter()
	r.Use(tracing.Middleware(tracer))

	r.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(models.HealthResponse{Status: "ok", Service: serviceName})
	}).Methods(http.MethodGet)

	r.HandleFunc("/authorize", authorizeHandler(cfg)).Methods(http.MethodPost)
	r.HandleFunc("/admin/config", adminConfigHandler(cfg)).Methods(http.MethodPost)

	addr := ":" + port
	log.Printf("listening on %s (failureRate=%.2f, latencyMS=%d)", addr, failureRate, latencyMS)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func authorizeHandler(cfg *config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		span := tracing.SpanFromContext(r)

		var req models.PaymentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		tracing.Tag(span, "payment.amount", fmt.Sprintf("%.2f", req.Amount))
		tracing.Tag(span, "order.id", req.OrderID)
		tracing.SpanLog(r.Context(), "INFO", "authorizing payment", "order.id", req.OrderID, "payment.amount", fmt.Sprintf("%.2f", req.Amount))

		failureRate, latencyMS := cfg.get()

		// Inject latency with ±30% jitter.
		if latencyMS > 0 {
			jitter := float64(latencyMS) * 0.3
			delay := float64(latencyMS) + (rand.Float64()*2-1)*jitter
			if delay > 0 {
				time.Sleep(time.Duration(delay) * time.Millisecond)
			}
		}

		txID := fmt.Sprintf("txn-%x", rand.Int63())
		tracing.Tag(span, "payment.transaction_id", txID)

		// Inject failures.
		if rand.Float64() < failureRate {
			tracing.Tag(span, "payment.status", "declined")
			tracing.SetError(span, "payment declined (injected)")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusPaymentRequired)
			_ = json.NewEncoder(w).Encode(models.PaymentResponse{
				TransactionID: txID,
				Status:        "declined",
				Message:       "payment declined",
			})
			return
		}

		tracing.Tag(span, "payment.status", "authorized")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(models.PaymentResponse{
			TransactionID: txID,
			Status:        "authorized",
			Message:       "payment authorized",
		})
	}
}

func adminConfigHandler(cfg *config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			FailureRate float64 `json:"failure_rate"`
			LatencyMS   int     `json:"latency_ms"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		cfg.set(body.FailureRate, body.LatencyMS)
		log.Printf("config updated: failureRate=%.2f, latencyMS=%d", body.FailureRate, body.LatencyMS)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"failure_rate": body.FailureRate,
			"latency_ms":   body.LatencyMS,
		})
	}
}

func parseFloat(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0.02
	}
	return v
}

func parseInt(s string) int {
	v, err := strconv.Atoi(s)
	if err != nil {
		return 50
	}
	return v
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
