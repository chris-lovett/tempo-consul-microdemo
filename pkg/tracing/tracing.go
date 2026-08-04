// Package tracing provides OpenTelemetry tracer initialisation and HTTP middleware
// shared across all microservices. Spans are exported via OTLP/gRPC to the
// OpenTelemetry Collector, which forwards them to Grafana Tempo.
//
// Environment variables:
//
//	OTEL_EXPORTER_OTLP_ENDPOINT  – gRPC endpoint of the OTel Collector
//	                                (default: otel-collector:4317)
//	SERVICE_NAME                  – override the service name at runtime
//	SAMPLE_RATE                   – fraction of traces to sample, 0.0–1.0
//	                                (default: 1.0)
package tracing

import (
	"context"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Init bootstraps the global OTel TracerProvider and returns a named Tracer plus
// a cleanup function that must be called on shutdown (flushes buffered spans).
func Init(serviceName string) (trace.Tracer, func()) {
	if sn := os.Getenv("SERVICE_NAME"); sn != "" {
		serviceName = sn
	}

	endpoint := getenv("OTEL_EXPORTER_OTLP_ENDPOINT", "otel-collector:4317")

	sampleRate := 1.0
	if sr := os.Getenv("SAMPLE_RATE"); sr != "" {
		if v, err := strconv.ParseFloat(sr, 64); err == nil {
			sampleRate = v
		}
	}

	ctx := context.Background()

	// Connect to the OTel Collector gRPC receiver.
	conn, err := grpc.NewClient(
		endpoint,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalf("[%s] could not connect to OTel Collector (%s): %v", serviceName, endpoint, err)
	}

	exporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithGRPCConn(conn))
	if err != nil {
		log.Fatalf("[%s] could not create OTLP exporter: %v", serviceName, err)
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceNameKey.String(serviceName),
		),
	)
	if err != nil {
		log.Fatalf("[%s] could not create OTel resource: %v", serviceName, err)
	}

	var sampler sdktrace.Sampler
	if sampleRate >= 1.0 {
		sampler = sdktrace.AlwaysSample()
	} else if sampleRate <= 0.0 {
		sampler = sdktrace.NeverSample()
	} else {
		sampler = sdktrace.TraceIDRatioBased(sampleRate)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler),
	)
	otel.SetTracerProvider(tp)
	// Register W3C TraceContext + Baggage as the global propagator so that
	// otelhttp injects/extracts traceparent headers on all inbound and outbound
	// HTTP requests. Without this the global propagator is a no-op and every
	// service starts a new root span, breaking the multi-service waterfall.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	log.Printf("[%s] OTel tracer initialised (collector=%s, sampleRate=%.2f)", serviceName, endpoint, sampleRate)

	cleanup := func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tp.Shutdown(shutdownCtx); err != nil {
			log.Printf("[%s] error shutting down TracerProvider: %v", serviceName, err)
		}
	}

	return tp.Tracer(serviceName), cleanup
}

// Middleware returns an HTTP server middleware that creates OTel server spans and
// propagates W3C TraceContext + Baggage headers from incoming requests.
func Middleware(tracer trace.Tracer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return otelhttp.NewHandler(next, "http.server",
			otelhttp.WithTracerProvider(otel.GetTracerProvider()),
			otelhttp.WithPropagators(otel.GetTextMapPropagator()),
		)
	}
}

// SpanFromContext extracts the current OTel span from the request context.
// Returns a no-op span if none is active, so callers never need a nil check.
func SpanFromContext(r *http.Request) trace.Span {
	return trace.SpanFromContext(r.Context())
}

// Tag adds a string attribute to span.
func Tag(span trace.Span, key, value string) {
	span.SetAttributes(attribute.String(key, value))
}

// SetError marks span as errored and records the message.
func SetError(span trace.Span, msg string) {
	span.SetAttributes(
		attribute.String("error", "true"),
		attribute.String("error.message", msg),
	)
	span.RecordError(nil) // status code set via SetStatus for richer UI display
}

func getenv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
