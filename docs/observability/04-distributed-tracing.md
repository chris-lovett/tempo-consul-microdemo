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
