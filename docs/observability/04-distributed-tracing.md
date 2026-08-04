# Distributed Tracing with Grafana Tempo

## Architecture

```
App Services  ──OTLP/gRPC──►  OTel Collector  ──OTLP/gRPC──►  Grafana Tempo
(6 Go services)               (in-cluster)                     (object storage)
       │                                                               │
       └── W3C traceparent headers propagated across Consul mesh ──────┘
                                                                       │
                                                              Grafana Explore ◄── operators/SREs
```

### Enterprise observability architecture

All six Go services share a common tracing library in [`pkg/tracing`](pkg/tracing/tracing.go) that initializes the OpenTelemetry SDK and exports spans over OTLP/gRPC. Each service sends traces to a local OTel Collector pod rather than exporting directly to Tempo.

The OTel Collector is deployed as a dedicated aggregation pod in the `tracing-demo` namespace. It provides:

- **Aggregation:** receives spans from every application service
- **Normalization:** converts SDK exports into Tempo-compatible OTLP payloads
- **Buffering:** protects against temporary Tempo or network outages
- **Retry/Backoff:** handles transient exporter failures gracefully
- **Decoupling:** avoids hard-coding Tempo endpoints in the application services

### Namespace topology

- **`tracing-demo`** — application services and `otel-collector`
- **`tempo`** — Grafana Tempo microservices: distributor, query frontend, ingester, querier, compactor
- **`prometheus`** — Prometheus Operator / kube-prometheus-stack and ServiceMonitors
- **`grafana`** — Grafana UI, dashboards, and datasource provisioning
- **`loki`** — Loki gateway, distributor, ingester, querier, compactor

This namespace model supports enterprise operational domains by separating RBAC, quotas, upgrade windows, and lifecycle management for traces, metrics, logs, and visualization.

### Signal flows

- Application services emit OTLP spans to `otel-collector:4317`
- The collector batches and forwards spans to `tempo-distributor:4317`
- Tempo stores trace chunks and indexes in object storage (S3/GCS/MinIO)
- Grafana queries Tempo via `tempo-query-frontend` for trace search and service maps
- Prometheus scrapes the collector and application metrics for dashboards and SLOs
- Loki ingests logs and enables log volume exploration and trace-linked log search

## Application developer quick start

Platform operators deploy and manage the OTel Collector and its integration with Tempo, Prometheus, Loki, and Grafana. Application teams should follow these steps.

### 1. Add OpenTelemetry SDK instrumentation

Install the OTel SDK for your language and add a shared tracing helper if available. In this repo the common library is [`pkg/tracing`](pkg/tracing/tracing.go).

- For Go apps, import `go.opentelemetry.io/otel` and `go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp`.
- Create a tracer provider once at service startup.
- Use a single shared tracer for the service.

Example startup code in Go:

```go
func main() {
  serviceName := getenv("SERVICE_NAME", "my-service")
  tracer, cleanup := tracing.Init(serviceName)
  defer cleanup()

  client := httpx.NewClient(tracer)
  router := mux.NewRouter()
  router.Use(tracing.Middleware(tracer))

  // use tracer and client throughout the service
  http.ListenAndServe(":8080", router)
}
```

For the demo app, see:
- tracer provider and propagator setup: [`pkg/tracing/tracing.go`](../../pkg/tracing/tracing.go)
- shared HTTP client instrumentation: [`pkg/httpx/client.go`](../../pkg/httpx/client.go)
- example service startup patterns: [`cmd/frontend/main.go`](../../cmd/frontend/main.go), [`cmd/checkout/main.go`](../../cmd/checkout/main.go), [`cmd/cart/main.go`](../../cmd/cart/main.go)

### 2. Configure the app to export to the shared collector

Point your service at the platform-provided collector endpoint.

```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=otel-collector:4317
```

In code, read this value from the environment and pass it to the OTLP exporter.

Example Go exporter setup:

```go
func InitExporter(ctx context.Context) *otlptracegrpc.Exporter {
  endpoint := getenv("OTEL_EXPORTER_OTLP_ENDPOINT", "otel-collector:4317")
  conn, err := grpc.DialContext(ctx, endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
  if err != nil {
    log.Fatalf("failed to connect to OTLP endpoint: %v", err)
  }

  exporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithGRPCConn(conn))
  if err != nil {
    log.Fatalf("failed to create OTLP exporter: %v", err)
  }
  return exporter
}
```

### 3. Register the global W3C propagator

Ensure your service uses W3C TraceContext so `traceparent` travels between services. This should be set once during startup, before your HTTP middleware and client transport are created.

In Go, that means:

```go
otel.SetTextMapPropagator(
  propagation.NewCompositeTextMapPropagator(
    propagation.TraceContext{},
    propagation.Baggage{},
  ),
)
```

This code belongs in the same initialization path as your tracer provider, typically in `main()` or your shared tracing helper.

This is required for distributed traces to join across service boundaries.

### 4. Instrument inbound and outbound requests

Wrap HTTP servers and clients so incoming requests continue existing traces and outgoing requests carry trace context.

- Inbound: use server middleware such as `otelhttp.NewHandler(...)` or your framework’s integration.
- Outbound: use an instrumented HTTP transport, e.g. `otelhttp.NewTransport(http.DefaultTransport)`.
- Also add spans/attributes around business operations that matter for debugging.

The important rule is: every request that crosses service boundaries should be traced and context-propagated.

### 5. Verify traces in Grafana Tempo

After deployment, generate traffic through your service and check Tempo.

- Look up traces for your service name.
- Confirm a trace includes spans from multiple services (e.g. `frontend` → `cart` → `checkout`).
- If a trace stops at one service, check that W3C propagation and outbound instrumentation are both active.

This is the minimal path: instrument the service, export to the shared collector, propagate trace context, and verify the resulting multi-service trace.

### Who owns what

- Platform operators: deploy and manage the collector, Tempo, Prometheus, Loki, dashboards, and metrics integration.
- Application developers: add SDK instrumentation, export to the shared collector endpoint, and enable request context propagation.

### Why this is enough

This repo assumes the platform provides the collector and observability stack. Application owners do not need to configure Tempo, service graph metrics, or Grafana directly — only the service-side OTel instrumentation.

### Storage and security

- Tempo persists traces in object storage and uses retention policies to manage lifecycle
- Loki stores log chunks and indexes in object storage in microservices mode
- Wherever possible, IRSA/OIDC should be used for S3 access so no static credentials reside in the cluster
- Grafana links trace, metrics, and logs through separate datasources for Tempo, Prometheus, and Loki

### Kubernetes resource map

Namespace `tracing-demo`
- frontend
- catalog
- cart
- checkout
- inventory
- payment
- otel-collector

Namespace `tempo`
- tempo-distributor
- tempo-query-frontend
- tempo-querier
- tempo-ingester
- tempo-compactor

Namespace `prometheus`
- prometheus
- prometheus-operator
- ServiceMonitor CRDs

Namespace `grafana`
- grafana
- Grafana sidecar for datasource provisioning

Namespace `loki`
- loki-loki-distributed-gateway
- loki-distributor
- loki-ingester
- loki-querier
- loki-compactor

Grafana provides the unified observability surface while each backend retains ownership of its signal type.

Spans travel:
1. **App → OTel Collector** over `otel-collector:4317` (OTLP/gRPC) — no Zipkin, no agent sidecar.
2. **OTel Collector → Tempo distributor** over `tempo-distributor.tempo.svc.cluster.local:4317` (OTLP/gRPC).
3. **Grafana → Tempo query-frontend** for search and trace retrieval.

Trace context is propagated using **W3C TraceContext** (`traceparent` / `tracestate` headers), which Envoy sidecars in Consul service mesh pass through transparently. No additional Envoy tracing configuration is required.

## Key Differences from the Zipkin POC

| Aspect | Zipkin POC | Tempo (this repo) |
|---|---|---|
| Instrumentation library | `zipkin-go` SDK | OpenTelemetry SDK (`otel`) |
| Wire format | Zipkin JSON v2 | OTLP/gRPC |
| Header propagation | B3 (single/multi) | W3C TraceContext |
| Collector | Zipkin server (in-cluster) | OTel Collector → Tempo |
| Backend storage | In-memory (lost on restart) | Object storage (S3/GCS/MinIO) |
| Grafana integration | Zipkin data source | Native Tempo data source |
| Service graph | Zipkin Dependencies tab | Grafana service map panel |

## Required Components

- Application deployment: `charts/tempo-consul-microdemo/`
- OpenTelemetry Collector: deployed by the same Helm chart (`otelCollector.enabled: true`)
- Grafana Tempo: `deploy/observability/tempo-values.yaml`
- Grafana data source: `deploy/observability/grafana-tempo-datasource.yaml`

## Tracing Verification

```bash
# 1. Port-forward the Tempo query frontend
kubectl port-forward -n tempo svc/tempo-query-frontend 3200:3200 &

# 2. Generate traffic
export FRONTEND_URL=$(kubectl get route frontend -n tracing-demo -o jsonpath='{.spec.host}')
curl -X POST http://${FRONTEND_URL}/checkout \
  -H "Content-Type: application/json" -d '{"user_id":"demo"}'

# 3. Search Tempo API for recent traces
curl 'http://localhost:3200/api/search?service.name=frontend&limit=5' | jq .

# 4. Fetch a specific trace by ID (replace TRACE_ID from step 3 output)
curl http://localhost:3200/api/traces/TRACE_ID | jq .
```

## OTel Collector Health

```bash
# Check collector readiness
kubectl exec -n tracing-demo deploy/otel-collector -- \
  wget -qO- http://localhost:13133/

# Tail collector logs for pipeline errors
kubectl logs -n tracing-demo deploy/otel-collector -f
```

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| No traces in Tempo | Collector → Tempo connection refused | Verify `otelCollector.tempoEndpoint` in values.yaml |
| Services missing from trace | W3C header not forwarded by Envoy | Confirm `consul.transparentProxy: true` is set |
| Spans missing attributes | `tracing.Tag()` called on nil span | OTel always returns a no-op span; check span context propagation |
| High cardinality in Tempo | Too many unique attribute values | Use `SAMPLE_RATE < 1.0` for high-traffic services |

## Related

- [../../DEMO_GUIDE.md](../../DEMO_GUIDE.md) — step-by-step demo scenarios
- [../../deploy/observability/README.md](../../deploy/observability/README.md) — full stack setup
- [Grafana Tempo docs](https://grafana.com/docs/tempo/latest/)
- [OpenTelemetry Go SDK](https://opentelemetry.io/docs/instrumentation/go/)
