// Package httpx provides an instrumented HTTP client that propagates W3C
// TraceContext headers via the global OTel TextMapPropagator.
package httpx

import (
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/trace"
)

// NewClient returns an *http.Client whose transport injects W3C traceparent /
// tracestate headers into every outbound request, enabling end-to-end trace
// propagation through Consul service mesh to Grafana Tempo.
func NewClient(tracer trace.Tracer) *http.Client {
	transport := otelhttp.NewTransport(
		http.DefaultTransport,
	)
	return &http.Client{Transport: transport}
}
