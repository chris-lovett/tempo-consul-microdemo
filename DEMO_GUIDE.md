# Distributed Tracing Demo Guide — Grafana Tempo

Step-by-step walkthrough for demonstrating distributed tracing with Grafana Tempo across six microservices running on Consul Service Mesh.

## Prerequisites

- Standalone Grafana and Prometheus already deployed in-cluster via Helm
- `curl`, `jq`, `helm`, and `oc` available in your terminal
- Consul Service Mesh running on your OpenShift cluster

> **Full observability stack setup** is in [`deploy/observability/README.md`](deploy/observability/README.md). Complete those steps before running the demo flows below.

### Step 1 — Deploy Grafana Tempo

```bash
# Edit tempo-values.yaml first — set your S3 bucket, region, and credentials
helm repo add grafana https://grafana.github.io/helm-charts && helm repo update
kubectl create namespace tempo
helm install tempo grafana/tempo-distributed \
  --namespace tempo \
  --values deploy/observability/tempo-values.yaml

# Wait for all Tempo pods to reach Running
kubectl get pods -n tempo -w
```

### Step 2 — Register the Tempo datasource in Grafana

Edit [`deploy/observability/grafana-tempo-datasource.yaml`](deploy/observability/grafana-tempo-datasource.yaml):
1. Set `metadata.namespace` to your Grafana namespace (find it with `helm list -A | grep grafana`)
2. Set `datasourceUid` to match your existing Prometheus datasource UID
   (Grafana → Administration → Data sources → Prometheus → copy UID from URL)

```bash
kubectl apply -f deploy/observability/grafana-tempo-datasource.yaml -n <grafana-namespace>

# Verify the datasource loaded (sidecar picks it up within ~30s)
# Grafana → Administration → Data sources → confirm "Tempo" appears
```

> If the Grafana sidecar is not enabled, use the Option B `additionalDataSources` block
> in the same file and run `helm upgrade` on your Grafana release instead.

### Step 3 — Deploy the application

```bash
kubectl create namespace tracing-demo
make helm-install NAMESPACE=tracing-demo

# Wait for all pods to be ready
kubectl get pods -n tracing-demo -w
```

> See [README.md](README.md) for image build and pull secret instructions.

### Step 4 — Wire Prometheus scraping of the OTel Collector

This enables the service graph panel in Grafana.

Edit [`deploy/observability/servicemonitor-otel-collector.yaml`](deploy/observability/servicemonitor-otel-collector.yaml) — add the label your Prometheus `serviceMonitorSelector` requires (check with `helm get values <prometheus-release> -n <prometheus-namespace> | grep -A5 serviceMonitorSelector`).

```bash
kubectl apply -f deploy/observability/servicemonitor-otel-collector.yaml -n tracing-demo

# Confirm Prometheus picked it up after ~1 minute
# Prometheus UI → Status → Targets → filter "otel-collector"
```

---

## Setup: Get Your URLs

```bash
# OpenShift
export FRONTEND_URL=$(kubectl get route frontend -n tracing-demo -o jsonpath='{.spec.host}')
export GRAFANA_URL=https://your-grafana-host  # replace with your Grafana URL

echo "Frontend: https://${FRONTEND_URL}"
echo "Grafana:  ${GRAFANA_URL}"
```

---

## Demo Flow 1: End-to-End Trace

**Goal**: Show a clean, successful distributed trace across all five services — the core "aha" moment.

### 1. Generate Traffic Across All Service Paths

```bash
# Browse the catalog — frontend → catalog
curl https://${FRONTEND_URL}/products

# Add an item to cart — frontend → cart → catalog
curl -X POST https://${FRONTEND_URL}/cart/user123/items \
  -H "Content-Type: application/json" \
  -d '{"product_id":"prod-1","quantity":2}'

# Trigger checkout — deepest chain: frontend → checkout → cart + inventory + payment
curl -X POST https://${FRONTEND_URL}/checkout \
  -H "Content-Type: application/json" \
  -d '{"user_id":"user123"}'
```

### 2. Open Grafana → Explore → Tempo

1. Navigate to `${GRAFANA_URL}/explore`
2. Select **Tempo** as the data source
3. Set query type to **Search**
4. Service Name: **frontend**
5. Click **Run query**

### 3. Find the Checkout Trace

- Sort by **Duration** (longest first)
- Click the most recent long trace

### 4. Walk Through the Trace

**Key Points to Highlight:**

- **Trace ID**: The same ID appears across all services — one request, one ID.
  > *"This is the thread connecting all five services. Without this, you'd be correlating timestamps across five separate log files — and hoping they're in sync."*

- **Waterfall View**:
  - Each row = one service span
  - Nested rows = child calls
  - Click any span to show attributes (HTTP method, status, `user.id`, `order.id`, etc.)

- **Service Call Chain**:
  ```
  frontend      (~80ms total)
    └─► checkout  (~60ms)
          ├─► cart      (~15ms)
          │     └─► catalog   (~5ms)
          ├─► inventory (~20ms)
          └─► payment   (~15ms)
  ```

- **Attributes panel**: Point out custom tags set by the application:
  - `user.id`, `order.id`, `payment.status`, `cart.total`
  - These are set by the Go services via `tracing.Tag()` — zero log instrumentation needed.

**Demo Script**:
> "One checkout request touched five services. The trace ID propagated automatically through the Consul mesh via W3C traceparent headers. Every span landed in Tempo and they're all linked here. This is what production observability looks like."

---

## Demo Flow 2: Finding a Slow Service

**Goal**: Show how Tempo pinpoints a performance bottleneck in seconds.

### 1. Enable Inventory Contention

```bash
# Simulate a database lock or slow query — 100% contention for a clean demo
curl -X POST https://${FRONTEND_URL}/inventory/admin/config \
  -H "Content-Type: application/json" \
  -d '{"contention_rate":1.0}'
```

### 2. Fire a Checkout — It Will Take Longer

```bash
curl -X POST https://${FRONTEND_URL}/checkout \
  -H "Content-Type: application/json" \
  -d '{"user_id":"demo-user"}' \
  -w "\nHTTP %{http_code} — Time: %{time_total}s\n"
```

> The checkout will fail with a `409 Conflict` because inventory is always contending — that's expected and creates an interesting trace.

### 3. In Grafana Explore → Tempo

1. Search for service **checkout**, sort by **Duration**
2. Click the slow/failed trace
3. Point to the **inventory span**

**Key Points to Highlight:**

- The inventory span has `reservation.success = false` and `error.message = "stock contention for product..."`
- The checkout span inherits the error because its inventory call failed
- **The waterfall makes it immediately obvious** — inventory is the culprit, not frontend, not payment

**Demo Script**:
> "We didn't grep logs. We didn't check dashboards one-by-one. We opened Explore, sorted by duration, and inventory lit up. That's the payoff."

### 4. Use TraceQL to Find All Contention Errors

```
{ .service.name = "inventory" && span.error = "true" }
```

This returns every inventory span that errored — filterable by time range, linkable to parent traces.

### 5. Disable Contention and Show Recovery

```bash
curl -X POST https://${FRONTEND_URL}/inventory/admin/config \
  -H "Content-Type: application/json" \
  -d '{"contention_rate":0.0}'

# Run another checkout — it succeeds now
curl -X POST https://${FRONTEND_URL}/checkout \
  -H "Content-Type: application/json" \
  -d '{"user_id":"demo-user"}'
```

> Find the new trace and show the before/after side-by-side in two browser tabs.

---

## Demo Flow 3: Error Propagation

**Goal**: Show how an error in one downstream service surfaces in the full trace with context.

### 1. Enable Payment Failures

```bash
# 100% failure rate — clean demo with no ambiguity
curl -X POST https://${FRONTEND_URL}/payment/admin/config \
  -H "Content-Type: application/json" \
  -d '{"failure_rate":1.0,"latency_ms":0}'
```

### 2. Fire a Checkout — It Will Fail

```bash
curl -X POST https://${FRONTEND_URL}/checkout \
  -H "Content-Type: application/json" \
  -d '{"user_id":"demo-user"}' \
  -w "\nHTTP %{http_code}\n"
```

### 3. In Tempo — Errored Spans Shown in Red

1. Search service: **payment**
2. Click the trace
3. The **payment span is red** — expand it

**Key Points to Highlight:**

```
payment span attributes:
  payment.status         = "declined"
  payment.amount         = "209.98"
  order.id               = "ord-6162636465..."
  payment.transaction_id = "txn-7f3a..."
  error                  = "true"
  error.message          = "payment declined (injected)"
```

**Demo Script**:
> "Payment failed. We know: the service, the HTTP status, the order ID, the transaction ID, and the exact error message — captured automatically by OpenTelemetry. Nobody wrote a single log statement for this."

### 4. TraceQL for Error Analysis

```
# All failed payment spans in the last hour
{ .service.name = "payment" && span.error = "true" }

# Checkout traces where payment failed
{ .service.name = "checkout" } >> { .service.name = "payment" && span.error = "true" }
```

### 5. Reset and Show Recovery

```bash
curl -X POST https://${FRONTEND_URL}/payment/admin/config \
  -H "Content-Type: application/json" \
  -d '{"failure_rate":0.02,"latency_ms":50}'
```

---

## Demo Flow 4: Service Graph (Dependency Map)

**Goal**: Show the real-time dependency graph built automatically from production traffic.

### 1. Generate Burst Traffic

```bash
for i in $(seq 1 20); do
  curl -s https://${FRONTEND_URL}/products > /dev/null
  curl -s -X POST https://${FRONTEND_URL}/cart/user123/items \
    -H "Content-Type: application/json" \
    -d '{"product_id":"prod-1","quantity":1}' > /dev/null
  curl -s -X POST https://${FRONTEND_URL}/checkout \
    -H "Content-Type: application/json" \
    -d '{"user_id":"demo-user"}' > /dev/null
  sleep 0.5
done
```

### 2. Open Grafana → Dashboards → "Tempo / Service Graph"

Or navigate to: `${GRAFANA_URL}/d/tempo-service-graph`

**Key Points to Highlight:**

- Every arrow = real calls that happened — the graph builds itself from traffic
- **cart → catalog**: A back-call not obvious from architecture diagrams
- **checkout → inventory + payment**: Parallel calls visible in the graph
- Click any node to filter traces for that service

**Demo Script**:
> "This is your live architecture diagram. No Visio, no documentation that goes stale. It built itself from actual traffic in the last few minutes. And every node is clickable — it drills straight into the traces."

---

## Advanced Scenarios

### Scenario A: TraceQL — Before/After Latency Comparison

```bash
# Enable latency
curl -X POST https://${FRONTEND_URL}/payment/admin/config \
  -H "Content-Type: application/json" \
  -d '{"failure_rate":0.0,"latency_ms":500}'
```

In Tempo:
```
# Find slow checkout traces
{ .service.name = "checkout" && duration > 600ms }
```

Compare against:
```
# Find fast checkout traces (from before)
{ .service.name = "checkout" && duration < 100ms }
```

### Scenario B: Cascading Failures

```bash
# 50% payment failure rate
curl -X POST https://${FRONTEND_URL}/payment/admin/config \
  -H "Content-Type: application/json" \
  -d '{"failure_rate":0.5,"latency_ms":50}'

# Generate 10 checkouts
for i in $(seq 1 10); do
  curl -s -X POST https://${FRONTEND_URL}/checkout \
    -H "Content-Type: application/json" \
    -d '{"user_id":"user-'"$i"'"}' > /dev/null
done
```

In Tempo, show the mix of successful (green) and failed (red) traces.

### Scenario C: Consul Mesh + OTel Integration

Highlight for architecture-focused audiences:

1. Show the **dual-span pattern**: Each Envoy sidecar creates a span for the proxy layer; the Go application creates its own span. Together they show both the network hop and the application logic duration.
2. Explain that W3C TraceContext headers are **forwarded transparently** by Envoy — no Consul configuration changes were needed vs. the Zipkin POC.
3. Show service intentions and how blocked connections produce trace errors at the Envoy layer (visible as proxy-level errors separate from application errors).

---

## Quick Reference: TraceQL Cheat Sheet

```
# Search by service
{ .service.name = "checkout" }

# Errors only
{ .service.name = "payment" && span.error = "true" }

# Slow spans
{ duration > 500ms }

# Specific user
{ span.user.id = "user123" }

# Full checkout pipeline (structural query)
{ .service.name = "checkout" } >> { .service.name = "payment" }

# Combined: slow + errored checkout
{ .service.name = "checkout" && duration > 300ms && rootSpan = true }
```

---

## Quick Reference: Service Endpoints

| Service | Port | Key Endpoints |
|---|---|---|
| frontend | 8080 | `/products`, `/cart/:user/items`, `/checkout` |
| catalog | 8081 | `/products`, `/products/:id` |
| cart | 8082 | `/cart/:user`, `/cart/:user/items` |
| checkout | 8083 | `/checkout` |
| payment | 8084 | `/authorize`, `/admin/config` |
| inventory | 8085 | `/reserve`, `/stock/:id`, `/admin/config` |
| otel-collector | 4317 | gRPC OTLP receiver |

---

## Troubleshooting During Demo

### No Traces Appearing in Tempo

```bash
# Check OTel Collector health
kubectl exec -n tracing-demo deploy/otel-collector -- wget -qO- http://localhost:13133/

# Check collector logs for export errors
kubectl logs -n tracing-demo deploy/otel-collector --tail=50

# Verify Tempo is receiving spans
kubectl logs -n tempo deploy/tempo-distributor --tail=50 | grep -i "spans received"
```

### Traces Missing Services

- Check `OTEL_EXPORTER_OTLP_ENDPOINT` in pod environment: `kubectl exec deploy/frontend -n tracing-demo -- env | grep OTEL`
- Verify Consul transparent proxy is active: all six services should have Envoy sidecars
- Confirm the OTel Collector `Service` resolves: `kubectl exec deploy/frontend -n tracing-demo -- nslookup otel-collector`

### Admin Endpoints Not Responding

The `/payment/admin/config` and `/inventory/admin/config` endpoints are proxied through `frontend`. Verify frontend has `PAYMENT_URL` and `INVENTORY_URL` set correctly in its environment.

---

## Demo Tips

1. **Pre-run the traffic burst** (Flow 4) 2–3 minutes before the live demo to populate the service graph
2. **Open Grafana in a separate browser window** — switching tabs is faster than navigating during a live demo
3. **Use browser zoom (150%)** to make spans readable from the back of the room
4. **Have TraceQL queries pre-typed** — copy-paste is faster than typing live
5. **Keep both a "clean" and "error" trace open in separate tabs** for Flow 3's before/after contrast

---

## Additional Resources

- [README.md](README.md) — Full deployment and configuration guide
- [docs/observability/04-distributed-tracing.md](docs/observability/04-distributed-tracing.md) — Architecture and troubleshooting
- [Grafana Tempo documentation](https://grafana.com/docs/tempo/latest/)
- [TraceQL reference](https://grafana.com/docs/tempo/latest/traceql/)
- [OpenTelemetry Go SDK](https://opentelemetry.io/docs/instrumentation/go/)
