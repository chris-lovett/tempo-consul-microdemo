// vm-api is a lightweight HTTP service for the EC2 (vm-dc) demo node.
// It is the upstream called by vm-web. It has no further upstreams.
// Instrumented with OTel exactly like the other services in this repo.
//
// Build:
//   GOOS=linux GOARCH=amd64 go build -o vm-api ./cmd/vm-api
//
// Run:
//   SERVICE_NAME=api PORT=9092 \
//   OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317 ./vm-api
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/mux"

	"github.com/chris-lovett/tempo-consul-microdemo/pkg/tracing"
)

type response struct {
	Name        string   `json:"name"`
	URI         string   `json:"uri"`
	Type        string   `json:"type"`
	IPAddresses []string `json:"ip_addresses"`
	StartTime   string   `json:"start_time"`
	EndTime     string   `json:"end_time"`
	Duration    string   `json:"duration"`
	Body        string   `json:"body"`
	Code        int      `json:"code"`
}

func main() {
	serviceName := getenv("SERVICE_NAME", "api")
	port := getenv("PORT", "9092")
	message := getenv("MESSAGE", "Hello from vm-dc VM (local api)")
	log.SetPrefix("[" + serviceName + "] ")

	tracer, cleanup := tracing.Init(serviceName)
	defer cleanup()

	r := mux.NewRouter()
	r.Use(tracing.Middleware(tracer))

	r.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "service": serviceName})
	}).Methods(http.MethodGet)

	r.PathPrefix("/").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		span := tracing.SpanFromContext(r)
		tracing.Tag(span, "service.dc", "vm-dc")

		end := time.Now()
		resp := response{
			Name:      serviceName,
			URI:       r.RequestURI,
			Type:      "HTTP",
			StartTime: start.Format(time.RFC3339Nano),
			EndTime:   end.Format(time.RFC3339Nano),
			Duration:  end.Sub(start).String(),
			Body:      message,
			Code:      http.StatusOK,
		}

		tracing.SpanLog(r.Context(), "INFO", "request handled",
			"service", serviceName, "uri", r.RequestURI)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

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
