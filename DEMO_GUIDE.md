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

---

## Demo Flow 5: Cross-DC Failover (vm-dc ↔ ocp-dc)

**Goal**: Show Consul Cluster Peering, automatic cross-datacenter failover, and unbroken trace continuity — a single trace ID spanning an EC2 VM and OpenShift.

### Architecture

```
┌─────────────────────────────────────────────────────────┐
│  ocp-dc  (OpenShift / ROSA)                             │
│                                                         │
│  frontend ──► checkout ──► cart ──► catalog             │
│                        ──► inventory                    │
│                        ──► payment                      │
│                                                         │
│  otel-collector ──► Tempo ──► Grafana                   │
└───────────────────────┬─────────────────────────────────┘
                        │  WAN Peering (mTLS)
                        │  Mesh Gateway ↔ Mesh Gateway
┌───────────────────────┴─────────────────────────────────┐
│  vm-dc  (EC2 / aws-vm-node-1)                           │
│                                                         │
│  client ──► web ──► api (price lookup)                  │
│              │                                          │
│              └──► frontend (ocp-dc peer) ──► checkout   │
│                                          ──► cart       │
│                                          ──► inventory  │
│                                          ──► payment    │
└─────────────────────────────────────────────────────────┘
```

**What this demo proves:**

1. **True cross-DC trace waterfall** — A single trace ID spans both datacenters. The Tempo waterfall shows: `client → web → api → frontend → checkout → cart → inventory → payment`. Every span is tagged with its originating datacenter.
2. **Automatic failover with zero app changes** — When `web` on EC2 fails its health check, Consul transparently reroutes traffic to `frontend` in ocp-dc. No DNS changes, no app restarts, no redeployment.
3. **Unbroken trace continuity through failover** — The W3C `traceparent` header travels through the mesh gateway. During failover, Tempo shows: `client → frontend → checkout → cart → inventory → payment` — still one trace ID.

### Normal operation call flow

```
vm-client
  └─ POST /checkout ──► vm-web (Envoy port 9095)
       ├─ GET /products/{id} ──► vm-api        [vm-dc span, price lookup]
       ├─ POST /cart/{user}/items ──► frontend  [ocp-dc span, via peer port 9093]
       │    └─ POST /cart/{user}/items ──► cart [ocp-dc span]
       │         └─ GET /products/{id} ──► catalog [ocp-dc span]
       └─ POST /checkout ──► frontend           [ocp-dc span]
            └─ POST /checkout ──► checkout      [ocp-dc span]
                 ├─ GET /cart/{user} ──► cart
                 ├─ POST /reserve ──► inventory
                 └─ POST /authorize ──► payment
```

### Pre-Flight Checklist

```bash
# ocp-dc — all pods Running
kubectl get pods -n tracing-demo

# ocp-dc — Tempo healthy
kubectl get pods -n tempo

# vm-dc — web and api running (on EC2)
sudo systemctl status web api --no-pager | grep -E "Active:"

# vm-dc — peering ACTIVE and frontend imported
export CONSUL_HTTP_TOKEN=<bootstrap-token>
curl -s -H "X-Consul-Token: $CONSUL_HTTP_TOKEN" \
  http://127.0.0.1:8500/v1/peering/ocp-dc \
  | jq '{State:.State, Imported:.StreamStatus.ImportedServices}'
# Expected: "State": "ACTIVE" and "default/tracing-demo/frontend" in Imported
```

If any ocp-dc pods are scaled to zero:

```bash
kubectl scale deployment \
  frontend catalog cart checkout payment inventory otel-collector \
  --replicas=1 -n tracing-demo
kubectl get pods -n tracing-demo -w
```

### Setup: Apply All Config (First Time or After Reset)

If rebuilding from scratch, see [`deploy/consul/peering-setup.md`](deploy/consul/peering-setup.md) for the full peering establishment sequence. For an existing environment:

**vm-dc (on EC2):**

```bash
export CONSUL_HTTP_TOKEN=<bootstrap-token>
export CONSUL_HTTP_ADDR=http://127.0.0.1:8500
cd /path/to/repo

bash deploy/ec2/setup.sh
```

Applies in order: namespace creation, service definitions, exported services, intentions, service defaults, and the service resolver.

**ocp-dc (on Mac):**

```bash
# Export otel-collector and frontend to vm-dc peer
kubectl apply -f deploy/consul/exported-services-ocp-dc-failover.yaml -n tracing-demo

# Allow vm-dc services to reach otel-collector and frontend
kubectl apply -f deploy/consul/service-intentions-otel-collector.yaml -n tracing-demo

# Patch the frontend intentions to allow vm-dc web + client peers
kubectl patch serviceintentions allow-frontend-failover -n tracing-demo \
  --type=merge -p '{
    "spec": {
      "sources": [
        {"action": "allow", "name": "*"},
        {"action": "allow", "name": "web",    "peer": "vm-dc"},
        {"action": "allow", "name": "client", "peer": "vm-dc"}
      ]
    }
  }'

# Verify all synced
kubectl get exportedservices,serviceintentions -n tracing-demo
```

### Starting the Traffic Loop (EC2)

```bash
# Start services if not already running
sudo systemctl start web api
sudo systemctl start otelcol

# Start vm-client (kills any stale instance first)
sudo pkill -f vm-client 2>/dev/null; sleep 1

SERVICE_NAME=client \
PORT=9080 \
UPSTREAM_URI=http://localhost:9095 \
OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317 \
  /usr/local/bin/vm-client > /tmp/vm-client.log 2>&1 &

# Tail the traffic log — one checkout per second
tail -f /tmp/vm-client.log
```

You'll see output like:

```
20:40:31 http=200 dc=vm-dc   product="Wireless Headphones" order=ord-abc123 payment=authorized total=79.99 elapsed=142ms
20:40:32 http=200 dc=vm-dc   product="Mechanical Keyboard" order=ord-def456 payment=authorized total=129.99 elapsed=138ms
```

---

### Scenario A: VM Fails → Failover to OpenShift

**Story:** The EC2 VM web service becomes unhealthy. Consul detects the failure and routes `client` traffic to `frontend` in ocp-dc — automatically, with no app changes, no DNS changes, no redeployment.

**Step 1 — Start traffic loop and show baseline in Grafana**

With the loop running (see above), open Grafana → Explore → Tempo and run:

```
{ resource.service.name = "client" }
```

Open a trace — shows the full waterfall:
`client → web → api → frontend → checkout → cart → inventory → payment`

Spans tagged `dc=vm-dc`: `client`, `web`, `api`
Spans from ocp-dc: `frontend`, `checkout`, `cart`, `inventory`, `payment`

**Demo script:**
> *"This is a single transaction. The client is on an EC2 VM. It calls the local web service, which looks up the product price from api — both on the VM, both tagged dc=vm-dc. Then web crosses the Consul mesh gateway peering tunnel to frontend in OpenShift, which runs the full checkout pipeline. One trace ID, two datacenters, seven services."*

**Step 2 — Trigger the failure (EC2 Terminal 2)**

```bash
sudo systemctl stop web
```

Watch the traffic log — within ~10 seconds:

```
20:40:40 http=200 dc=vm-dc   product="Running Shoes" order=ord-xyz789 payment=authorized total=94.95 elapsed=135ms
20:40:41 http=502 dc=unknown  ← single in-flight during health check window
20:40:42 http=200 dc=unknown  ← Consul failover active; client now routes to frontend directly
20:40:43 http=200 dc=unknown  product="Yoga Mat" order=ord-uvw012 payment=authorized total=34.99 elapsed=201ms
```

> `dc=unknown` appears because `client` is now hitting `frontend` directly — frontend returns its own response shape, not vm-web's. Correct behaviour.

Consul UI (vm-dc) → Services → `web`: health check turns red.

**Demo script:**
> *"Web on the EC2 VM just stopped. Consul's ServiceResolver saw zero healthy local instances and began routing the client's upstream through the mesh gateway to frontend in OpenShift. The traffic loop never stopped. No app changes. No DNS changes. Under 10 seconds."*

**Step 3 — Show the failover trace in Tempo**

```
{ resource.service.name = "client" }
```

Sort by most recent. Click a trace from after the failover — the waterfall now shows:
`client → frontend → checkout → cart → inventory → payment`

The `client` span is the same root as before failover. The `web` and `api` spans are gone (web is down). `frontend` is the first child, inheriting the trace context from `client` through the mesh gateway.

**Demo script:**
> *"The W3C traceparent header crossed the mesh gateway. The request that started on the EC2 VM was served by frontend in Kubernetes — and every span is in Tempo under the same trace ID. The trace itself documents the datacenter handoff."*

**Step 4 — Restore and show recovery (EC2 Terminal 2)**

```bash
sudo systemctl start web
```

Within 10 seconds the health check passes, Consul stops using the failover target, and the traffic log returns to `dc=vm-dc` with the full cross-DC waterfall.

---

### Scenario B: OpenShift Fails → Failover to VM

**Story:** The OpenShift frontend deployment is scaled to zero. Consul routes incoming ocp-dc requests to `web` on the EC2 VM.

**Step 1 — Start a traffic loop against ocp-dc (Mac terminal)**

```bash
FRONTEND=frontend-tracing-demo.apps.rosa.cluster2.6cxo.p3.openshiftapps.com

while true; do
  response=$(curl -sk -X POST \
    "https://${FRONTEND}/checkout" \
    -H "Content-Type: application/json" \
    -d '{"user_id":"ocp-user"}')
  status=$(echo "$response" | jq -r '.payment_status // "error"' 2>/dev/null)
  echo "$(date +%H:%M:%S) ocp-dc frontend payment=$status"
  sleep 1
done
```

**Step 2 — Trigger the failure**

```bash
kubectl scale deployment frontend --replicas=0 -n tracing-demo
```

Consul health check for `frontend` fails within 10 seconds. The ServiceResolver kicks in and routes requests to `web` on EC2 via the mesh gateway.

**Demo script:**
> *"We just scaled frontend to zero in OpenShift. Consul's health check saw no healthy instances and, using the ServiceResolver failover config, began routing those HTTPS requests through the mesh gateway to the EC2 VM. No Ingress change. No DNS TTL. No load balancer update. Under 10 seconds."*

**Step 3 — Show cross-DC trace in Tempo**

```
{ resource.dc = "vm-dc" }
```

Open the most recent trace — shows `web → api` spans tagged `dc=vm-dc`, produced by requests that entered through the ocp-dc Ingress but were served by the EC2 VM.

**Step 4 — Restore OpenShift**

```bash
kubectl scale deployment frontend --replicas=1 -n tracing-demo
```

---

### Scenario C: Side-by-Side — Both DCs Healthy

Show both DCs generating traces simultaneously in two Grafana tabs.

**Tab 1 — full cross-DC waterfall:**
```
{ resource.service.name = "client" }
```

**Tab 2 — ocp-dc only (direct traffic):**
```
{ resource.service.name = "frontend" && duration > 50ms }
```

Generate direct ocp-dc traffic:

```bash
FRONTEND=frontend-tracing-demo.apps.rosa.cluster2.6cxo.p3.openshiftapps.com

for i in $(seq 1 10); do
  curl -sk -X POST "https://${FRONTEND}/cart/direct-user/items" \
    -H "Content-Type: application/json" \
    -d '{"product_id":"prod-2","quantity":1}' > /dev/null
  curl -sk -X POST "https://${FRONTEND}/checkout" \
    -H "Content-Type: application/json" \
    -d '{"user_id":"direct-user"}' > /dev/null
  sleep 0.5
done
```

**Demo script:**
> *"Two entry points, one observability plane. Tab 1 shows checkout requests that originated on the EC2 VM and crossed the mesh gateway to OpenShift. Tab 2 shows requests that hit the ocp-dc Ingress directly. Same Tempo, same trace format, regardless of where the request started."*

---

### vm-dc Quick Reference

**Demo controls:**

```bash
# Trigger Scenario A (VM failure)
sudo systemctl stop web          # on EC2

# Trigger Scenario B (OpenShift failure)
kubectl scale deployment frontend --replicas=0 -n tracing-demo

# Restore EC2 web
sudo systemctl start web         # on EC2

# Restore ocp-dc frontend
kubectl scale deployment frontend --replicas=1 -n tracing-demo

# Inject payment errors (cross-DC)
curl -sk -X POST \
  "https://frontend-tracing-demo.apps.rosa.cluster2.6cxo.p3.openshiftapps.com/payment/admin/config" \
  -H "Content-Type: application/json" \
  -d '{"failure_rate":1.0,"latency_ms":0}'

# Reset payment errors
curl -sk -X POST \
  "https://frontend-tracing-demo.apps.rosa.cluster2.6cxo.p3.openshiftapps.com/payment/admin/config" \
  -H "Content-Type: application/json" \
  -d '{"failure_rate":0.02,"latency_ms":50}'
```

**Key TraceQL queries:**

```
# Full cross-DC waterfall (client origin on vm-dc)
{ resource.service.name = "client" }

# All vm-dc spans
{ resource.dc = "vm-dc" }

# All ocp-dc frontend traces
{ resource.service.name = "frontend" }

# Traces that crossed DCs (both client and frontend in same trace)
{ resource.service.name = "client" } | select(resource.dc)

# Slow traces (latency injection active)
{ resource.service.name = "frontend" && duration > 500ms }

# Error / declined payment traces
{ status = error }
```

**vm-dc Consul tokens:**

| Role | Token |
|---|---|
| Bootstrap / mgmt | `<bootstrap-token>` |
| web service | `<web-token>` |
| api service | `<api-token>` |
| mesh-gateway | `<mgw-token>` |

**vm-dc Troubleshooting:**

| Symptom | Check | Fix |
|---|---|---|
| `frontend` not in ImportedServices | `kubectl get exportedservices -n tracing-demo` | Re-apply `exported-services-ocp-dc-failover.yaml`; ensure `namespace: tracing-demo` per service |
| `frontend` imported but health API empty | `curl .../v1/health/service/frontend?peer=ocp-dc` | Create `tracing-demo` namespace: `curl -X PUT .../v1/namespace -d '{"Name":"tracing-demo"}'` |
| Traffic blanks but doesn't flip to frontend | Check `ejections_overflow` stat | Ensure `service-defaults-client.hcl` has `MaxEjectionPercent=100` |
| `403 RBAC: access denied` on upstream | Check intentions on ocp-dc | Patch `allow-frontend-failover` to include `web` and `client` from vm-dc peer |
| vm-web returns 502 on /checkout | `journalctl -u web` on EC2 | Verify port 9093 upstream is healthy: `curl -v http://localhost:9093/health` |
| `failed_eds_health` on failover cluster | `curl localhost:<admin>/clusters \| grep failover` | Verify `service-resolver-web-failover.hcl` is applied |
| Client sidecar fails to start | `cat /tmp/envoy-client.log` | Kill stale envoy processes; use `-grpc-addr 127.0.0.1:8502` |
| OTel spans not reaching Tempo | `journalctl -u otelcol -n 30` | Confirm `localhost:9317` upstream is healthy |
| `consul reload` returns 403 | Token lacks `agent:write` | Use bootstrap token |

**vm-dc File Reference:**

| File | Purpose | Apply where |
|---|---|---|
| `deploy/ec2/setup.sh` | Applies all vm-dc config entries in order | EC2: `bash deploy/ec2/setup.sh` |
| `deploy/ec2/web.hcl` | Consul service def for web (api + frontend peer + otel-collector upstreams) | EC2: `/etc/consul.d/web.hcl` |
| `deploy/ec2/api.hcl` | Consul service def for api | EC2: `/etc/consul.d/api.hcl` |
| `deploy/ec2/client.hcl` | Consul service def for client (upstream: web:9095) | EC2: `/etc/consul.d/client.hcl` |
| `deploy/ec2/exported-services-vm-dc.hcl` | Export web+api to ocp-dc peer | EC2: `consul config write` |
| `deploy/ec2/service-intentions-vm-dc.hcl` | Allow web+api to reach otel-collector | EC2: `consul config write` |
| `deploy/ec2/service-intentions-vm-dc-failover.hcl` | Allow ocp-dc frontend to reach vm-dc web | EC2: `consul config write` |
| `deploy/ec2/service-defaults-web.hcl` | MeshGateway mode=none for local routing | EC2: `consul config write` |
| `deploy/ec2/service-defaults-client.hcl` | PassiveHealthCheck with MaxEjectionPercent=100 | EC2: `consul config write` |
| `deploy/ec2/service-resolver-web-failover.hcl` | web fails over to ocp-dc frontend (tracing-demo ns) | EC2: `consul config write` |
| `deploy/consul/exported-services-ocp-dc-failover.yaml` | Export otel-collector+frontend to vm-dc | ocp-dc: `kubectl apply -n tracing-demo` |
| `deploy/consul/service-intentions-otel-collector.yaml` | Allow vm-dc peers to reach otel-collector | ocp-dc: `kubectl apply -n tracing-demo` |
| `deploy/consul/service-intentions-frontend-peer.yaml` | Patch command: allow vm-dc web+client → frontend | ocp-dc: `kubectl patch` (see Setup section) |
| `deploy/consul/peering-setup.md` | Full peering setup sequence (rebuild from scratch) | Reference |
