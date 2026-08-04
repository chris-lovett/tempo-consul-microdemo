# tempo-consul-microdemo

Production-ready distributed tracing demo: six Go microservices running on **Consul Service Mesh**, instrumented with the **OpenTelemetry SDK**, emitting spans via an **OTel Collector** to **Grafana Tempo**.

This repo is the production successor to [`zipkin-otel-microdemo`](https://github.com/chris-lovett/zipkin-otel-microdemo).  The demo application and scenarios are preserved; the tracing backend has been completely replaced.

---

## What Changed from the Zipkin POC

| Aspect | Zipkin POC | This repo |
|---|---|---|
| Instrumentation library | `zipkin-go` SDK | OpenTelemetry Go SDK (`go.opentelemetry.io/otel`) |
| Wire format | Zipkin JSON v2 (`/api/v2/spans`) | OTLP/gRPC |
| Header propagation | B3 (Zipkin proprietary) | **W3C TraceContext** (`traceparent`) |
| In-cluster collector | Zipkin server (in-memory) | **OpenTelemetry Collector** |
| Trace storage | Lost on pod restart | **Grafana Tempo** (object storage — S3/GCS/MinIO) |
| Grafana integration | Zipkin data source | Native **Tempo** data source |
| Service graph | Zipkin Dependencies tab | Grafana **service map** panel |
| Retention | None (ephemeral) | Configurable (default 30 days) |

---

## Architecture

```
                        Consul Service Mesh (mTLS, transparent proxy)
  ┌──────────────────────────────────────────────────────────────┐
  │                                                              │
  │  frontend ──► catalog                                        │
  │     │                                                        │
  │     └──► cart ──► catalog                                    │
  │                                                              │
  │     └──► checkout ──► cart                                   │
  │                   ├──► inventory                             │
  │                   └──► payment                               │
  │                                                              │
  └──────────────────────────────────────────────────────────────┘
          │  (each service sends spans via OTLP/gRPC)
          ▼
   OTel Collector  ──OTLP/gRPC──►  Grafana Tempo
          │                              │
          │                              ▼
          │                    Grafana Explore / Dashboards
          │
          └── W3C traceparent propagated through Envoy sidecars
```

### Enterprise-grade architecture

All six Go services share a single [`pkg/tracing`](pkg/tracing/tracing.go) package that initializes the OpenTelemetry SDK and exports spans via OTLP/gRPC. Traces are not sent directly to Tempo; they are ingested by a dedicated OTel Collector instead.

The OTel Collector runs as a separate pod in the application namespace and serves as the primary aggregation and export point for tracing traffic. It:

- aggregates spans from all application services
- normalizes exporter data for Tempo
- buffers traces during short outages
- retries on transient failures
- forwards spans to `tempo-distributor:4317`

### Namespace design

- **`tracing-demo`** — application services and `otel-collector`
- **`tempo`** — Grafana Tempo microservices, including distributor, query frontend, ingester, querier, and compactor
- **`prometheus`** — Prometheus Operator / kube-prometheus-stack for cluster and application metrics
- **`grafana`** — Grafana UI, dashboards, and datasource provisioning
- **`loki`** — Loki microservices for log ingestion and query

Each namespace boundary is intentional: it separates lifecycle, RBAC, resource quotas, and upgrade domains while preserving end-to-end telemetry.

### Signal flow

1. Application services emit traces to `otel-collector:4317` over OTLP/gRPC
2. The OTel Collector buffers and forwards spans to `tempo-distributor:4317`
3. Tempo writes trace chunks and indexes to object storage
4. Grafana queries Tempo via `tempo-query-frontend` for trace search and service maps
5. Prometheus scrapes `otel-collector` and application targets for metrics
6. Loki ingests logs and provides log volume, app logs, and trace-linked log search

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
- service monitor CRDs

Namespace `grafana`
- grafana
- Grafana sidecar for datasource provisioning

Namespace `loki`
- loki-loki-distributed-gateway
- loki-distributor
- loki-ingester
- loki-querier
- loki-compactor

Grafana provides the unified observability surface while each backend maintains ownership of its signal type: traces, metrics, or logs.

---

## Services

| Service | Port | Role |
|---|---|---|
| `frontend` | 8080 | API gateway, proxies all routes |
| `catalog` | 8081 | Product catalog (static data) |
| `cart` | 8082 | Shopping cart (in-memory, calls catalog) |
| `checkout` | 8083 | Orchestrates cart → inventory → payment |
| `payment` | 8084 | Payment processor (configurable failure/latency) |
| `inventory` | 8085 | Inventory reservation (configurable contention) |
| `otel-collector` | 4317/4318 | OTLP receiver, forwards to Tempo |

---

## Prerequisites

- Kubernetes 1.25+ or OpenShift 4.12+
- Helm 3.x
- Consul Service Mesh installed ([HashiCorp Consul Helm chart](https://developer.hashicorp.com/consul/docs/k8s/installation/install))
- Grafana Tempo deployed (see [deploy/observability/](deploy/observability/))
- Container registry credentials (default: `quay.io/chris_lovett/tempo-consul-microdemo`)

---

## Deploying to OpenShift / Kubernetes

### 1. Deploy Grafana Tempo

```bash
# Edit deploy/observability/tempo-values.yaml first — set S3 bucket, region, credentials
helm repo add grafana https://grafana.github.io/helm-charts && helm repo update
kubectl create namespace tempo
helm install tempo grafana/tempo-distributed \
  --namespace tempo \
  --values deploy/observability/tempo-values.yaml
```

### 2. Add the Tempo datasource to Grafana

Edit `deploy/observability/grafana-tempo-datasource.yaml` — set `metadata.namespace` to your Grafana namespace and update `datasourceUid` to match your existing Prometheus datasource.

```bash
kubectl apply -f deploy/observability/grafana-tempo-datasource.yaml -n <grafana-namespace>
```

If you also deploy Loki, register the Loki datasource in Grafana by applying `deploy/observability/grafana-loki-datasource.yaml` in the same Grafana namespace:

```bash
kubectl apply -f deploy/observability/grafana-loki-datasource.yaml -n <grafana-namespace>
```

Then verify log volume in Grafana Explore using a query like:

```logql
sum(rate({namespace="tracing-demo"}[5m]))
```

> See [`deploy/observability/README.md`](deploy/observability/README.md) for the full step-by-step, including how to handle Grafana installs without the sidecar enabled.

### 3. Build and push service images

```bash
export IMAGE_REGISTRY=quay.io/<your-org>/tempo-consul-microdemo
export IMAGE_TAG=0.2.0

# Multi-arch (recommended for OpenShift on arm64 nodes)
make build-multiarch IMAGE_REGISTRY=${IMAGE_REGISTRY} IMAGE_TAG=${IMAGE_TAG}
```

Update `charts/tempo-consul-microdemo/values.yaml` → `global.imageRegistry` to your registry.

### 4. Deploy the application

```bash
kubectl create namespace tracing-demo

# If using a private registry, create the pull secret first:
kubectl create secret docker-registry quay-pull \
  --docker-server=quay.io \
  --docker-username=<user> \
  --docker-password=<token> \
  -n tracing-demo

make helm-install NAMESPACE=tracing-demo
```

### 5. Configure Consul for metrics + Grafana deep-links (optional)

```bash
helm upgrade consul hashicorp/consul -n consul \
  -f <your-existing-consul-values.yaml> \
  -f deploy/observability/consul-values-observability.yaml
```

Update the `dashboardURLTemplates.service` URL in `consul-values-observability.yaml` to point at your actual Grafana host.

### 6. Access the application

```bash
# OpenShift route
export FRONTEND_URL=$(kubectl get route frontend -n tracing-demo -o jsonpath='{.spec.host}')
echo "Frontend: https://${FRONTEND_URL}"

# Standard Kubernetes (if ingress is enabled)
export FRONTEND_URL=$(kubectl get ingress frontend -n tracing-demo -o jsonpath='{.spec.rules[0].host}')
```

---

## Runtime Configuration (Demo Controls)

All values are adjustable at runtime without redeployment via the `/admin/config` endpoints, accessible through the frontend proxy.

```bash
# Inject payment failures (100% for demo)
curl -X POST http://${FRONTEND_URL}/payment/admin/config \
  -H "Content-Type: application/json" \
  -d '{"failure_rate":1.0,"latency_ms":0}'

# Inject inventory contention (100% for demo)
curl -X POST http://${FRONTEND_URL}/inventory/admin/config \
  -H "Content-Type: application/json" \
  -d '{"contention_rate":1.0}'

# Reset both to production-realistic defaults
curl -X POST http://${FRONTEND_URL}/payment/admin/config \
  -H "Content-Type: application/json" \
  -d '{"failure_rate":0.02,"latency_ms":50}'

curl -X POST http://${FRONTEND_URL}/inventory/admin/config \
  -H "Content-Type: application/json" \
  -d '{"contention_rate":0.05}'
```

### Common Environment Variables

| Variable | Default | Description |
|---|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `otel-collector:4317` | OTel Collector gRPC endpoint |
| `SAMPLE_RATE` | `1.0` | Trace sampling ratio (0.0–1.0) |
| `SERVICE_NAME` | service binary name | Override service name in Tempo |
| `PAYMENT_FAILURE_RATE` | `0.02` | Fraction of payments that fail |
| `PAYMENT_LATENCY_MS` | `50` | Base payment latency in ms |
| `INVENTORY_CONTENTION_RATE` | `0.05` | Fraction of reservations that contend |

---

## Exploring Traces in Grafana Tempo

### Access Grafana

Open your Grafana instance and navigate to **Explore** → select the **Tempo** data source.

### Search by Service

1. Set query type to **Search**
2. Service Name: `frontend` (or any service)
3. Click **Run query**

### TraceQL (Tempo's query language)

```
# All checkout spans
{ .service.name = "checkout" }

# Slow requests (> 500ms)
{ .service.name = "checkout" && duration > 500ms }

# Failed payment spans
{ .service.name = "payment" && span.error = "true" }

# Full checkout traces with inventory spans
{ .service.name = "checkout" } >> { .service.name = "inventory" }
```

### Service Map

Navigate to **Dashboards** → search for "Tempo / Service Graph" to see the dependency map auto-built from real traffic.

---

## Request Flows

```
GET  /products                    → frontend → catalog
GET  /products/{id}               → frontend → catalog
GET  /cart/{user_id}              → frontend → cart
POST /cart/{user_id}/items        → frontend → cart → catalog
POST /checkout                    → frontend → checkout → cart → inventory → payment
```

---

## Local Development

```bash
# Run all services locally (traces sent to localhost:4317)
# Start a local OTel Collector + Tempo with Docker Compose:
docker compose -f docker-compose.dev.yml up -d

# Run a service
OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317 go run ./cmd/frontend
```

---

## Consul Service Mesh Integration

### Key Annotations (applied via Helm chart)

```yaml
consul.hashicorp.com/connect-inject: "true"
consul.hashicorp.com/transparent-proxy: "true"
consul.hashicorp.com/connect-service-upstreams: "catalog:8081,cart:8082,checkout:8083"
```

### W3C TraceContext + Envoy

Envoy sidecars injected by Consul automatically propagate the `traceparent` header (W3C TraceContext) on all proxied requests.  **No additional Envoy tracing configuration is required** — the application-level OTel instrumentation and Envoy's transparent header forwarding work together automatically.

This is a key improvement over the Zipkin POC, which required B3 header propagation to be explicitly configured in Envoy.

### mTLS

All service-to-service communication is automatically encrypted and mutually authenticated by Consul's CA. The OTel Collector endpoint (`otel-collector:4317`) is also injected via Consul transparent proxy.

---

## Uninstall

```bash
make helm-uninstall NAMESPACE=tracing-demo
kubectl delete namespace tracing-demo
helm uninstall tempo -n tempo
kubectl delete namespace tempo
```

---

## Documentation

| File | Description |
|---|---|
| [DEMO_GUIDE.md](DEMO_GUIDE.md) | Step-by-step interactive demo scenarios |
| [docs/observability/04-distributed-tracing.md](docs/observability/04-distributed-tracing.md) | Architecture, verification, troubleshooting |
| [deploy/observability/README.md](deploy/observability/README.md) | Full observability stack setup |
| [deploy/observability/tempo-values.yaml](deploy/observability/tempo-values.yaml) | Tempo Helm values (production) |
| [deploy/observability/grafana-tempo-datasource.yaml](deploy/observability/grafana-tempo-datasource.yaml) | Grafana data source CR |
| [deploy/observability/consul-values-observability.yaml](deploy/observability/consul-values-observability.yaml) | Consul metrics + UI deep-links |
