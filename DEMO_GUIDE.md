# Distributed Tracing Demo Guide — Grafana Tempo

Step-by-step walkthrough for demonstrating distributed tracing with Grafana Tempo across six microservices running on Consul Service Mesh.

---

## Setup: Get Your URLs

```bash
export FRONTEND_URL=$(kubectl get route frontend -n tracing-demo -o jsonpath='{.spec.host}')
export GRAFANA_URL=https://your-grafana-host  # replace with your Grafana URL

echo "Frontend: https://${FRONTEND_URL}"
echo "Grafana:  ${GRAFANA_URL}"
```

---

## Pre-Flight Checklist

Run these before the demo starts:

```bash
# All ocp-dc pods Running
kubectl get pods -n tracing-demo

# Tempo healthy
kubectl get pods -n tempo

# Quick smoke test — should return product list
curl -s https://${FRONTEND_URL}/products | jq '.[0]'

# Reset any injected errors from previous sessions
curl -s -X POST https://${FRONTEND_URL}/payment/admin/config \
  -H "Content-Type: application/json" \
  -d '{"failure_rate":0.02,"latency_ms":50}'

curl -s -X POST https://${FRONTEND_URL}/inventory/admin/config \
  -H "Content-Type: application/json" \
  -d '{"contention_rate":0.05}'
```

If any pods are scaled to zero:

```bash
kubectl scale deployment \
  frontend catalog cart checkout payment inventory otel-collector \
  --replicas=1 -n tracing-demo
kubectl get pods -n tracing-demo -w
```

---

## Demo Flow 1: End-to-End Trace

**Goal**: Show a clean, successful distributed trace across all five services — the core "aha" moment.

### 1. Generate Traffic

```bash
# Browse the catalog — frontend → catalog
curl -s https://${FRONTEND_URL}/products > /dev/null

# Add an item to cart — frontend → cart → catalog
curl -s -X POST https://${FRONTEND_URL}/cart/user123/items \
  -H "Content-Type: application/json" \
  -d '{"product_id":"prod-1","quantity":2}' > /dev/null

# Trigger checkout — deepest chain: frontend → checkout → cart + inventory + payment
curl -s -X POST https://${FRONTEND_URL}/checkout \
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

**Key points to highlight:**

- **Trace ID** — the same ID appears across all services. One request, one ID.
  > *"This is the thread connecting all five services. Without this you'd be correlating timestamps across five separate log files — and hoping they're in sync."*

- **Waterfall view** — each row is one service span, nested rows are child calls:
  ```
  frontend      (~80ms total)
    └─► checkout  (~60ms)
          ├─► cart      (~15ms)
          │     └─► catalog   (~5ms)
          ├─► inventory (~20ms)
          └─► payment   (~15ms)
  ```

- **Attributes panel** — point out custom tags set by the application:
  `user.id`, `order.id`, `payment.status`, `cart.total`

  > *"These are set by the Go services via `tracing.Tag()` — zero log instrumentation needed."*

**Demo script:**
> *"One checkout request touched five services. The trace ID propagated automatically through the Consul mesh via W3C traceparent headers. Every span landed in Tempo and they're all linked here. This is what production observability looks like."*

---

## Demo Flow 2: Finding a Slow Service

**Goal**: Show how Tempo pinpoints a performance bottleneck in seconds.

### 1. Enable Inventory Contention

```bash
curl -s -X POST https://${FRONTEND_URL}/inventory/admin/config \
  -H "Content-Type: application/json" \
  -d '{"contention_rate":1.0}'
```

### 2. Fire a Checkout

```bash
# Add an item to cart first
curl -s -X POST https://${FRONTEND_URL}/cart/demo-user/items \
  -H "Content-Type: application/json" \
  -d '{"product_id":"prod-1","quantity":1}' > /dev/null

# Trigger checkout — will fail with 409 due to contention
curl -s -X POST https://${FRONTEND_URL}/checkout \
  -H "Content-Type: application/json" \
  -d '{"user_id":"demo-user"}' \
  -w "\nHTTP %{http_code} — Time: %{time_total}s\n"
```

> Returns `409 Conflict` — inventory is always contending. Expected. Creates an interesting trace.

### 3. In Grafana → Explore → Tempo

1. Search service: **checkout**, sort by **Duration**
2. Click the slow/failed trace
3. Point to the **inventory span**

**Key points:**
- The inventory span has `reservation.success = false` and `error.message = "stock contention..."`
- The checkout span inherits the error because its inventory call failed
- **The waterfall makes it immediately obvious** — inventory is the culprit

**Demo script:**
> *"We didn't grep logs. We didn't check dashboards one-by-one. We opened Explore, sorted by duration, and inventory lit up. That's the payoff."*

### 4. TraceQL — Find All Contention Errors

```
{ .service.name = "inventory" && span.error = "true" }
```

### 5. Reset and Show Recovery

```bash
# Disable contention and zero out payment failures for a clean success
curl -s -X POST https://${FRONTEND_URL}/inventory/admin/config \
  -H "Content-Type: application/json" \
  -d '{"contention_rate":0.0}'

curl -s -X POST https://${FRONTEND_URL}/payment/admin/config \
  -H "Content-Type: application/json" \
  -d '{"failure_rate":0.0,"latency_ms":50}'

# Add to cart and checkout — should succeed now
curl -s -X POST https://${FRONTEND_URL}/cart/demo-user/items \
  -H "Content-Type: application/json" \
  -d '{"product_id":"prod-1","quantity":1}' > /dev/null

curl -s -X POST https://${FRONTEND_URL}/checkout \
  -H "Content-Type: application/json" \
  -d '{"user_id":"demo-user"}'
```

> Find the new trace — show the before/after side-by-side in two browser tabs.

---

## Demo Flow 3: Error Propagation

**Goal**: Show how an error in one downstream service surfaces in the full trace with context.

### 1. Enable Payment Failures

```bash
curl -s -X POST https://${FRONTEND_URL}/payment/admin/config \
  -H "Content-Type: application/json" \
  -d '{"failure_rate":1.0,"latency_ms":0}'
```

### 2. Fire a Checkout

```bash
# Add an item to cart first
curl -s -X POST https://${FRONTEND_URL}/cart/demo-user/items \
  -H "Content-Type: application/json" \
  -d '{"product_id":"prod-1","quantity":1}' > /dev/null

curl -s -X POST https://${FRONTEND_URL}/checkout \
  -H "Content-Type: application/json" \
  -d '{"user_id":"demo-user"}' \
  -w "\nHTTP %{http_code}\n"
```

### 3. In Tempo — Errored Spans Shown in Red

1. Search service: **payment**
2. Click the trace
3. The **payment span is red** — expand it

**Attributes to highlight:**
```
payment.status         = "declined"
payment.amount         = "209.98"
order.id               = "ord-6162..."
payment.transaction_id = "txn-7f3a..."
error                  = "true"
error.message          = "payment declined (injected)"
```

**Demo script:**
> *"Payment failed. We know: the service, the HTTP status, the order ID, the transaction ID, and the exact error message — captured automatically by OpenTelemetry. Nobody wrote a single log statement for this."*

### 4. TraceQL for Error Analysis

```
# All failed payment spans
{ .service.name = "payment" && span.error = "true" }

# Checkout traces where payment failed
{ .service.name = "checkout" } >> { .service.name = "payment" && span.error = "true" }
```

### 5. Reset

```bash
curl -s -X POST https://${FRONTEND_URL}/payment/admin/config \
  -H "Content-Type: application/json" \
  -d '{"failure_rate":0.02,"latency_ms":50}'
```

---

## Demo Flow 4: Service Graph (Dependency Map)

**Goal**: Show the real-time dependency graph built automatically from production traffic — no manual configuration, no architecture diagrams to maintain.

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

> Run this 2–3 minutes before the demo to pre-populate the graph.

### 2. Open Grafana → Explore → Tempo → Service Graph tab

1. Navigate to `${GRAFANA_URL}/explore`
2. Select **Tempo** as the data source
3. Click the **Service Graph** tab (next to Search and TraceQL)
4. Click **Run query**

You should see a live node graph: `user → frontend → cart → catalog`, `frontend → checkout → inventory/payment`, with avg latency and RPS on each edge.

**Key talking points:**
- Every arrow = real calls observed in the last few minutes — the graph builds itself from traffic
- **cart → catalog**: A back-call that would never appear in a hand-drawn diagram
- **checkout → inventory + payment**: Parallel downstream calls visible as separate edges
- Click any node → jumps to traces filtered for that service (Explore → Search tab)

**Demo script:**
> *"This is your live architecture diagram. No Visio, no runbook that goes stale. It built itself from actual traffic in the last few minutes. Every node is clickable — it drills straight into the traces."*

---

## Demo Flow 5: Cross-DC Traffic (vm-dc → ocp-dc)

**Goal**: Show that a request originating on an EC2 VM crosses the Consul mesh gateway into OpenShift, and every span lands in Tempo under the same trace ID.

> **Setup notes**: EC2 otelcol exports spans via OTLP HTTP to the otel-collector OpenShift Route (NodePort 30317 is blocked by ROSA security group). The `client` FakeService only forwards GET requests — use GET-only traffic. Stop local `web` so the ServiceResolver fails over client's Envoy to ocp-dc frontend.

### 1. Prerequisite: trigger failover (stop local web)

The `vm-client` binary (FakeService) only forwards GET requests to its upstream. To get a cross-DC trace, stop the local `web` service so Consul's ServiceResolver routes `client`'s Envoy upstream (port 9095) through the mesh gateway to ocp-dc frontend.

```bash
# On EC2 — stop local web to activate failover
sudo systemctl stop web-envoy
sudo pkill -f vm-web 2>/dev/null; true

# Verify port 9090 is gone and Envoy now routes to ocp-dc
curl -s http://localhost:9090/health -o /dev/null -w "web local (expect 000): %{http_code}\n"
sleep 15   # wait for Consul health check to mark web critical
curl -s http://localhost:9095/products -o /dev/null -w "via Envoy failover (expect 200): %{http_code}\n"
```

### 2. Start vm-client and Drive Traffic (EC2)

`vm-client` is a FakeService tracing proxy: listens on `:9080`, wraps each request in an OTel span, and forwards GET requests to Envoy (`:9095`), which now routes through the Consul mesh gateway to ocp-dc frontend.

```bash
# On EC2 — Terminal 1: start the tracer (if not already running)
sudo pkill -f vm-client 2>/dev/null; sleep 1
SERVICE_NAME=client PORT=9080 UPSTREAM_URI=http://localhost:9095 \
OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317 \
  /usr/local/bin/vm-client > /tmp/vm-client.log 2>&1 &
tail -f /tmp/vm-client.log
# Expected: "[client] OTel tracer initialised" and "listening on :9080"
```

```bash
# On EC2 — Terminal 2: drive traffic through the tracer on port 9080
# Use GET only — vm-client (FakeService) converts POSTs to GET on upstream calls
while true; do
  curl -s http://localhost:9080/products > /dev/null
  curl -s http://localhost:9080/ > /dev/null
  sleep 2
done
```

Terminal 1 should log lines like:
```
[client] ... msg="upstream call completed" upstream="http://localhost:9095" code="OK"
```

If port 9095 returns errors, check the sidecar:

```bash
systemctl is-active client-envoy
curl -s http://localhost:9095/health -o /dev/null -w "%{http_code}\n"
# Expect 200 — if not: sudo systemctl restart client-envoy
```

### 3. In Grafana → Explore → Tempo

```
{ resource.service.name = "client" }
```

Open any trace — the root span is `client` (originating on EC2 / vm-dc). Expand it to see the full call chain through ocp-dc. One trace ID, two datacenters.

**Demo script:**
> *"The client service is a Go process running on an EC2 VM in a different network. It's calling through the Consul mesh gateway peering tunnel into OpenShift. The W3C traceparent header crossed that boundary — every span from both environments lands in Tempo under the same trace ID. No VPN, no special instrumentation for the gateway — the mesh handles it transparently."*

---

## Quick Reference: TraceQL

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

# All vm-dc spans
{ resource.dc = "vm-dc" }

# Cross-DC: client origin
{ resource.service.name = "client" }
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

## Demo Controls (Quick Reference)

```bash
# Inject inventory contention
curl -s -X POST https://${FRONTEND_URL}/inventory/admin/config \
  -H "Content-Type: application/json" \
  -d '{"contention_rate":1.0}'

# Inject payment failures
curl -s -X POST https://${FRONTEND_URL}/payment/admin/config \
  -H "Content-Type: application/json" \
  -d '{"failure_rate":1.0,"latency_ms":0}'

# Inject payment latency only
curl -s -X POST https://${FRONTEND_URL}/payment/admin/config \
  -H "Content-Type: application/json" \
  -d '{"failure_rate":0.0,"latency_ms":500}'

# Reset everything to production-realistic defaults
curl -s -X POST https://${FRONTEND_URL}/payment/admin/config \
  -H "Content-Type: application/json" \
  -d '{"failure_rate":0.02,"latency_ms":50}'

curl -s -X POST https://${FRONTEND_URL}/inventory/admin/config \
  -H "Content-Type: application/json" \
  -d '{"contention_rate":0.05}'
```

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

### Pods Not Ready

```bash
kubectl scale deployment \
  frontend catalog cart checkout payment inventory otel-collector \
  --replicas=1 -n tracing-demo
kubectl get pods -n tracing-demo -w
```

### Admin Endpoints Not Responding

The `/payment/admin/config` and `/inventory/admin/config` endpoints are proxied through `frontend`. Verify frontend is running:

```bash
curl -s https://${FRONTEND_URL}/products | jq '.[0].name'
```

---

## Demo Tips

1. **Pre-run the burst traffic** (Flow 4) 2–3 minutes before the demo to populate the service graph
2. **Open Grafana in a separate browser window** — switching tabs is faster than navigating during a live demo
3. **Use browser zoom (150%)** to make spans readable from the back of the room
4. **Have TraceQL queries pre-typed** in a scratch file — copy-paste is faster than typing live
5. **Keep both a "clean" and "error" trace open in separate tabs** for Flow 3's before/after contrast
6. **Demo Flows 1–4 are fully self-contained on ocp-dc** — no EC2 dependency for those flows

---

## Additional Resources

- [README.md](README.md) — Full deployment and configuration guide
- [docs/observability/04-distributed-tracing.md](docs/observability/04-distributed-tracing.md) — Architecture and troubleshooting
- [deploy/observability/README.md](deploy/observability/README.md) — Full observability stack setup
- [Grafana Tempo documentation](https://grafana.com/docs/tempo/latest/)
- [TraceQL reference](https://grafana.com/docs/tempo/latest/traceql/)
- [OpenTelemetry Go SDK](https://opentelemetry.io/docs/instrumentation/go/)
