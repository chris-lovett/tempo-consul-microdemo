// vm-web is a lightweight HTTP service for the EC2 (vm-dc) demo node.
// It replaces fake-service with a fully OTel-instrumented equivalent:
// - Accepts HTTP requests on PORT (default 9090)
// - Calls an upstream API service at UPSTREAM_URI (default http://localhost:9091)
// - Returns a JSON response mirroring fake-service's output format
// - Emits OTLP/gRPC spans to OTEL_EXPORTER_OTLP_ENDPOINT (default otel-collector:4317)
//
// Build:
//   GOOS=linux GOARCH=amd64 go build -o vm-web ./cmd/vm-web
//
// Run:
//   SERVICE_NAME=web PORT=9090 UPSTREAM_URI=http://localhost:9091 \
//   OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317 ./vm-web
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/mux"

	"github.com/chris-lovett/tempo-consul-microdemo/pkg/httpx"
	"github.com/chris-lovett/tempo-consul-microdemo/pkg/tracing"
)

type response struct {
	Name          string              `json:"name"`
	URI           string              `json:"uri"`
	Type          string              `json:"type"`
	IPAddresses   []string            `json:"ip_addresses"`
	StartTime     string              `json:"start_time"`
	EndTime       string              `json:"end_time"`
	Duration      string              `json:"duration"`
	Body          string              `json:"body"`
	UpstreamCalls map[string]upstream `json:"upstream_calls,omitempty"`
	Code          int                 `json:"code"`
}

type upstream struct {
	Name      string `json:"name"`
	URI       string `json:"uri"`
	Type      string `json:"type"`
	Body      string `json:"body"`
	Code      int    `json:"code"`
	Error     string `json:"error,omitempty"`
}

func main() {
	serviceName := getenv("SERVICE_NAME", "web")
	port := getenv("PORT", "9090")
	upstreamURI := getenv("UPSTREAM_URI", "")
	message := getenv("MESSAGE", "Hello from vm-dc VM")
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

	r.PathPrefix("/").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		span := tracing.SpanFromContext(r)
		tracing.Tag(span, "service.dc", "vm-dc")

		resp := response{
			Name:      serviceName,
			URI:       r.RequestURI,
			Type:      "HTTP",
			StartTime: start.Format(time.RFC3339Nano),
			Body:      message,
			Code:      http.StatusOK,
		}

		// Call upstream if configured
		if upstreamURI != "" {
			tracing.Tag(span, "upstream.uri", upstreamURI)
			upReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstreamURI, nil)
			if err == nil {
				upResp, err := client.Do(upReq)
				up := upstream{URI: upstreamURI, Type: "HTTP"}
				if err != nil {
					up.Error = err.Error()
					up.Code = http.StatusBadGateway
					tracing.SetError(span, err.Error())
				} else {
					defer upResp.Body.Close()
					var upBody response
					if decErr := json.NewDecoder(upResp.Body).Decode(&upBody); decErr == nil {
						up.Name = upBody.Name
						up.Body = upBody.Body
					}
					up.Code = upResp.StatusCode
				}
				resp.UpstreamCalls = map[string]upstream{upstreamURI: up}
			}
		}

		end := time.Now()
		resp.EndTime = end.Format(time.RFC3339Nano)
		resp.Duration = end.Sub(start).String()

		tracing.SpanLog(r.Context(), "INFO", "request handled",
			"service", serviceName, "uri", r.RequestURI, "duration", resp.Duration)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	addr := ":" + port
	log.Printf("listening on %s (upstream=%s)", addr, upstreamURI)
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
