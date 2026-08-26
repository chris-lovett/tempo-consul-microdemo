// vm-client is a demo client service for the EC2 (vm-dc) node.
// It calls the web service via its Consul upstream port (Envoy local bind port 9095).
//
// Consul handles failover transparently via a SamenessGroup:
// - When web is healthy in vm-dc, traffic stays local
// - When web fails its health check, Consul's xDS automatically reroutes
//   the 9095 upstream to the peer frontend service in ocp-dc
// - The app code is unchanged — it always calls localhost:9095
//
// Build:
//   GOOS=linux GOARCH=amd64 go build -o vm-client ./cmd/vm-client
//
// Run:
//   SERVICE_NAME=client PORT=9080 \
//   UPSTREAM_URI=http://localhost:9095 \
//   OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317 \
//   ./vm-client
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
	StartTime     string              `json:"start_time"`
	EndTime       string              `json:"end_time"`
	Duration      string              `json:"duration"`
	Body          string              `json:"body"`
	UpstreamCalls map[string]upstream `json:"upstream_calls,omitempty"`
	Code          int                 `json:"code"`
}

type upstream struct {
	Name  string `json:"name"`
	URI   string `json:"uri"`
	Body  string `json:"body"`
	Code  int    `json:"code"`
	Error string `json:"error,omitempty"`
}

func main() {
	serviceName := getenv("SERVICE_NAME", "client")
	port := getenv("PORT", "9080")
	// UPSTREAM_URI points at the Envoy local bind port for web (9095).
	// Consul's SamenessGroup handles failover to ocp-dc transparently —
	// this app code never changes regardless of which datacenter serves it.
	upstreamURI := getenv("UPSTREAM_URI", "http://localhost:9095")
	message := getenv("MESSAGE", "Hello from vm-dc client")
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
		tracing.Tag(span, "upstream.uri", upstreamURI)

		resp := response{
			Name:      serviceName,
			URI:       r.RequestURI,
			Type:      "HTTP",
			StartTime: start.Format(time.RFC3339Nano),
			Body:      message,
			Code:      http.StatusOK,
		}

		// Call web via Envoy upstream port 9095.
		// Consul transparently fails over to ocp-dc frontend when web is unhealthy.
		upReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstreamURI+r.RequestURI, nil)
		if err == nil {
			upResp, upErr := client.Do(upReq)
			up := upstream{URI: upstreamURI}
			if upErr != nil {
				up.Error = upErr.Error()
				up.Code = http.StatusBadGateway
				tracing.SetError(span, upErr.Error())
			} else {
				defer upResp.Body.Close()
				var upBody response
				if decErr := json.NewDecoder(upResp.Body).Decode(&upBody); decErr == nil {
					up.Name = upBody.Name
					up.Body = upBody.Body
				}
				up.Code = upResp.StatusCode
				tracing.Tag(span, "upstream.served_by", up.Name)
			}
			tracing.SpanLog(r.Context(), "INFO", "upstream call completed",
				"upstream", upstreamURI, "served_by", up.Name, "code", http.StatusText(up.Code))
			resp.UpstreamCalls = map[string]upstream{upstreamURI: up}
		}

		end := time.Now()
		resp.EndTime = end.Format(time.RFC3339Nano)
		resp.Duration = end.Sub(start).String()

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
