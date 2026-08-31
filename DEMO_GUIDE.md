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

# Reset inventory stock to initial values (run after any long traffic session)
curl -s -X POST https://${FRONTEND_URL}/inventory/admin/stock/reset | jq '{reset,prod1:.stock."prod-1"}'

# Clear demo-user cart (run if previous session left items)
curl -s -X DELETE https://${FRONTEND_URL}/cart/demo-user

# vm-dc (EC2) — all services active
ssh -i ~/hashi/aws/vm-dc-demo.pem ubuntu@3.149.3.205 \
  "sudo systemctl is-active web api web-envoy api-envoy client-envoy client otelcol"
# Expected: 7 lines of "active"

# vm-dc — peering to ocp-dc ACTIVE
ssh -i ~/hashi/aws/vm-dc-demo.pem ubuntu@3.149.3.205 \
  "consul peering list -token=c0f2d09d-a9a7-653b-8ddd-88ca6fa101d8"
# Expected: ocp-dc  ACTIVE  2  2
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

1. **True cross-DC trace waterfall** — A single trace ID spans both datacenters. Every span is tagged with its originating datacenter.
2. **Automatic failover with zero app changes** — When `web` on EC2 fails its health check, Consul transparently reroutes traffic to `frontend` in ocp-dc. No DNS changes, no app restarts, no redeployment.
3. **Unbroken trace continuity through failover** — The W3C `traceparent` header travels through the mesh gateway. During failover, Tempo still shows one trace ID across both environments.

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

> **Setup notes:**
> - EC2 otelcol exports spans via OTLP HTTP to the otel-collector OpenShift Route (NodePort 30317 is blocked by the ROSA security group).
> - `vm-client` (FakeService) only forwards GET requests to its upstream — use GET-only traffic for the simple cross-DC flow in Scenario A below. The full checkout pipeline (Scenario B) requires vm-web.
> - Stop the local `web` service to trigger Consul's ServiceResolver failover; `client`'s Envoy (port 9095) will then route to ocp-dc frontend.

### Pre-Flight Checklist

```bash
# ocp-dc — all pods Running
kubectl get pods -n tracing-demo

# ocp-dc — Tempo healthy
kubectl get pods -n tempo

# vm-dc — web and api running (on EC2)
sudo systemctl status web api --no-pager | grep -E "Active:"

# vm-dc — peering ACTIVE and frontend imported
# (vm-dc.env lives on your Mac; use the bootstrap token directly on EC2)
curl -s -H "X-Consul-Token: c0f2d09d-a9a7-653b-8ddd-88ca6fa101d8" \
  http://127.0.0.1:8500/v1/peering/ocp-dc \
  | jq '{State:.State, Imported:.StreamStatus.ImportedServices}'
# Expected: "State": "ACTIVE" and "default/tracing-demo/frontend" in Imported
```

### Setup: Apply All Config (First Time or After Reset)

> The Consul config entries on vm-dc were applied by Terraform at provision time — the repo is not cloned on EC2. For a full rebuild from scratch, see [`deploy/consul/peering-setup.md`](deploy/consul/peering-setup.md).

**vm-dc — verify and apply config entries (on EC2):**

```bash
TOKEN=c0f2d09d-a9a7-653b-8ddd-88ca6fa101d8

# Both should return a Kind value — if either returns null, apply the block below
CONSUL_HTTP_TOKEN=$TOKEN consul config list -kind exported-services
CONSUL_HTTP_TOKEN=$TOKEN consul config list -kind service-resolver
```

If `service-resolver` is missing (returns empty), apply it:

```bash
TOKEN=c0f2d09d-a9a7-653b-8ddd-88ca6fa101d8
CONSUL_HTTP_TOKEN=$TOKEN CONSUL_HTTP_ADDR=http://127.0.0.1:8500 \
consul config write - <<EOF
Kind = "service-resolver"
Name = "web"

Failover = {
  "*" = {
    Targets = [
      {
        Peer      = "ocp-dc"
        Service   = "frontend"
        Namespace = "tracing-demo"
      }
    ]
  }
}
EOF
```

**ocp-dc — re-apply if needed (on Mac):**

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

### Re-Establishing Peering After a Consul Raft Reset

If vm-dc's Consul raft state was wiped (e.g. during troubleshooting) the ocp-dc side will show `vm-dc FAILING`. Use the `PeeringAcceptor` CRD to regenerate the token cleanly — no manual base64/DER manipulation:

```bash
# 1. Delete the stale FAILING peering on ocp-dc
oc -n consul exec consul-server-0 -- consul peering delete -name vm-dc \
  -token=a8adcfaf-9d6a-1aad-2f84-c7ccf2d80066

# 2. Ensure the mesh config entry enables peering through the mesh gateway
#    (only needed once; skip if already applied)
oc apply -f deploy/consul/mesh-config.yaml

# 3. Delete and re-create the PeeringAcceptor so a fresh token is generated
#    with the mesh-gateway LB address (not the internal pod IP)
oc delete -f deploy/consul/peering-acceptor.yaml
oc apply  -f deploy/consul/peering-acceptor.yaml

# 4. Wait for Synced=True, then extract the token
oc -n consul get peeringacceptor vm-dc -o jsonpath='{.status.conditions[0]}'
oc -n consul get secret peering-token-vm-dc \
  -o jsonpath='{.data.data}' | base64 -d > /tmp/peering-token-vm-dc.txt

# 5. Verify the token encodes the mesh-gateway address (not 10.x.x.x:8502)
cat /tmp/peering-token-vm-dc.txt | base64 -d | \
  python3 -c "import sys,json; d=json.load(sys.stdin); print(d['ServerAddresses'])"
# Expected: ['ac7b0cfc403cd47af8603a93f1fe2a86-462273150.us-east-2.elb.amazonaws.com:443']

# 6. Copy to vm-dc and establish
scp -i ~/hashi/aws/vm-dc-demo.pem /tmp/peering-token-vm-dc.txt ubuntu@3.149.3.205:/tmp/
ssh -i ~/hashi/aws/vm-dc-demo.pem ubuntu@3.149.3.205 \
  "consul peering establish -name ocp-dc \
   -peering-token \$(cat /tmp/peering-token-vm-dc.txt) \
   -token=c0f2d09d-a9a7-653b-8ddd-88ca6fa101d8"

# 7. Verify both sides ACTIVE
oc -n consul exec consul-server-0 -- consul peering list \
  -token=a8adcfaf-9d6a-1aad-2f84-c7ccf2d80066
ssh -i ~/hashi/aws/vm-dc-demo.pem ubuntu@3.149.3.205 \
  "consul peering list -token=c0f2d09d-a9a7-653b-8ddd-88ca6fa101d8"
# Both should show: State=ACTIVE  Imported=2  Exported=2
```

---

### Scenario A: Simple Cross-DC Trace (vm-client → ocp-dc frontend)

**Story:** Drive GET traffic from the EC2 client, which routes through the Consul mesh gateway into OpenShift frontend. Show the cross-DC span in Tempo.

#### Step 1 — Trigger failover (stop local web)

```bash
# On EC2 — stop local web so Consul's ServiceResolver routes client's upstream to ocp-dc
sudo systemctl stop web-envoy
sudo pkill -f vm-web 2>/dev/null; true

# Verify port 9090 is gone and Envoy now routes to ocp-dc
curl -s http://localhost:9090/health -o /dev/null -w "web local (expect 000): %{http_code}\n"
sleep 15   # wait for Consul health check to mark web critical
curl -s http://localhost:9095/products -o /dev/null -w "via Envoy failover (expect 200): %{http_code}\n"
```

#### Step 2 — Start vm-client and drive traffic (EC2)

```bash
# Terminal 1: start the tracer
sudo pkill -f vm-client 2>/dev/null; sleep 1
NAME=client LISTEN_ADDR=0.0.0.0:9080 UPSTREAM_URIS=http://localhost:9095 \
OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317 \
  /usr/local/bin/vm-client > /tmp/vm-client.log 2>&1 &
tail -f /tmp/vm-client.log
# Expected: "Started service: name=client ... listenAddress=0.0.0.0:9080"
```

```bash
# Terminal 2: drive GET traffic (vm-client/FakeService only forwards GETs)
while true; do
  curl -s http://localhost:9080/products > /dev/null
  curl -s http://localhost:9080/ > /dev/null
  sleep 2
done
```

Terminal 1 should log:
```
[client] ... msg="upstream call completed" upstream="http://localhost:9095" code="OK"
```

#### Step 3 — Show the trace in Grafana → Explore → Tempo

Use the **Search tab** — the Grafana UI validator rejects `resource.` syntax client-side even though Tempo's server accepts it:

1. Select **Search** tab, set **Service Name = `client`**, click **Run query**

Or switch to **TraceQL** tab:
```
{ resource.service.name = "client" }
```
> If Grafana shows a client-side parse error on `resource.`, click **Run query** anyway — Tempo evaluates it correctly server-side.

Open any trace. The root span is `client` (originating on EC2 / vm-dc). Look for **`dc = vm-dc`** in the Resource Attributes panel — that's the stamp proving this span originated on the EC2 VM.

**Demo script:**
> *"The client service is a Go process running on an EC2 VM in a different network. It's calling through the Consul mesh gateway peering tunnel into OpenShift. The W3C traceparent header crossed that boundary — every span from both environments lands in Tempo under the same trace ID. No VPN, no special instrumentation for the gateway — the mesh handles it transparently."*

---

### Scenario B: Full Checkout Pipeline Failover (vm-web → ocp-dc)

**Story:** The EC2 `web` service becomes unhealthy. Consul detects the failure and routes `client` traffic to `frontend` in ocp-dc — automatically, with no app changes, no DNS changes, no redeployment.

#### Step 1 — Start the traffic loop and show the baseline (EC2 Terminal 1)

```bash
# Start services if not already running
sudo systemctl start web api otelcol

# Start vm-client
sudo pkill -f vm-client 2>/dev/null; sleep 1
NAME=client \
LISTEN_ADDR=0.0.0.0:9080 \
UPSTREAM_URIS=http://localhost:9095 \
OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317 \
  /usr/local/bin/vm-client > /tmp/vm-client.log 2>&1 &

# Continuous traffic loop
while true; do
  response=$(curl -s --max-time 3 http://localhost:9080/)
  name=$(echo "$response" | jq -r '.upstream_calls | to_entries[0].value.name // empty' 2>/dev/null)
  code=$(echo "$response" | jq -r '.upstream_calls | to_entries[0].value.code // empty' 2>/dev/null)
  echo "$(date +%H:%M:%S) served_by=${name:-unknown} http=$code"
  sleep 1
done
```

You'll see `served_by=web http=200` on every line while EC2 is healthy.

#### Step 2 — Show baseline in Grafana

Open Grafana → Explore → Tempo, Search tab, Service Name = `client`. Open a trace — shows `client → web → api → frontend → checkout → cart → inventory → payment`. Spans tagged `dc=vm-dc`: `client`, `web`, `api`.

**Demo script:**
> *"This is a single transaction. The client is on an EC2 VM. It calls the local web service, which looks up the product price from api — both on the VM, both tagged dc=vm-dc. Then web crosses the Consul mesh gateway peering tunnel to frontend in OpenShift, which runs the full checkout pipeline. One trace ID, two datacenters, seven services."*

#### Step 3 — Trigger the failure (EC2 Terminal 2)

```bash
sudo systemctl stop web
```

Watch Terminal 1 — within ~10 seconds:

```
20:40:40 served_by=web    http=200
20:40:41 served_by=unknown http=503   ← single in-flight during health check window
20:40:42 served_by=unknown http=404   ← Consul failover active, client now routes to ocp-dc frontend directly
20:40:43 served_by=unknown http=404
```

> The `http=404` response is expected — `frontend` on ocp-dc doesn't expose a root `/` route. The routing itself is working; requests are reaching ocp-dc.

**Demo script:**
> *"Web on the EC2 VM just stopped. Consul's ServiceResolver saw zero healthy local instances and began routing the client's upstream through the mesh gateway to frontend in OpenShift. The traffic loop never stopped. No app changes. No DNS changes. Under 10 seconds."*

#### Step 4 — Show the failover trace in Tempo

```
{ resource.service.name = "client" }
```

Sort by most recent. Click a trace from after the failover — the waterfall now shows `client → frontend → checkout → cart → inventory → payment`. The `web` and `api` spans are gone (web is down); `frontend` is the first child, inheriting the trace context from `client` through the mesh gateway.

**Demo script:**
> *"The W3C traceparent header crossed the mesh gateway. The request that started on the EC2 VM was served by frontend in Kubernetes — and every span is in Tempo under the same trace ID. The trace itself documents the datacenter handoff."*

#### Step 5 — Restore and show recovery (EC2 Terminal 2)

```bash
sudo systemctl start web
```

Within 10 seconds the health check passes, Consul stops using the failover target, and Terminal 1 returns to `served_by=web http=200` with the full cross-DC waterfall.

---

### Scenario C: OpenShift Fails → Failover to VM

**Story:** The OpenShift frontend deployment is scaled to zero. Consul routes incoming requests to `web` on the EC2 VM.

#### Step 1 — Start a traffic loop against ocp-dc (Mac terminal)

```bash
FRONTEND=frontend-tracing-demo.apps.rosa.cluster2.6cxo.p3.openshiftapps.com

while true; do
  code=$(curl -sk -o /dev/null -w "%{http_code}" \
    "https://${FRONTEND}/health")
  echo "$(date +%H:%M:%S) ocp-dc frontend http=$code"
  sleep 1
done
```

#### Step 2 — Trigger the failure

```bash
kubectl scale deployment frontend --replicas=0 -n tracing-demo
```

**Demo script:**
> *"We just scaled frontend to zero in OpenShift. Consul's health check saw no healthy instances and, using the ServiceResolver failover config, began routing those HTTPS requests through the mesh gateway to the EC2 VM. No Ingress change. No DNS TTL. No load balancer update. Under 10 seconds."*

#### Step 3 — Show cross-DC trace in Tempo

```
{ resource.dc = "vm-dc" }
```

Open the most recent trace — shows `web → api` spans tagged `dc=vm-dc`, produced by requests that entered through the ocp-dc Ingress but were served by the EC2 VM.

#### Step 4 — Restore OpenShift

```bash
kubectl scale deployment frontend --replicas=1 -n tracing-demo
```

Traffic returns to ocp-dc frontend automatically once the health check passes.

---

### Scenario D: Side-by-Side — Both DCs Healthy

Show both DCs generating traces simultaneously in two Grafana tabs.

**Tab 1 — full cross-DC waterfall:**
```
{ resource.service.name = "client" }
```

**Tab 2 — ocp-dc direct traffic:**
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

# All vm-dc spans (use Search tab in Grafana UI, or run TraceQL directly)
{ resource.dc = "vm-dc" }

# Cross-DC: client origin (full cross-DC waterfall)
{ resource.service.name = "client" }

# NOTE: Grafana's TraceQL editor may show a client-side parse error on
# resource.* syntax. Click Run query anyway — Tempo evaluates it correctly.
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
| inventory | 8085 | `/reserve`, `/stock/:id`, `/admin/config`, `/admin/stock/reset` |
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

# Reset inventory stock to initial values (after long traffic depletes it)
curl -s -X POST https://${FRONTEND_URL}/inventory/admin/stock/reset

# Clear demo-user cart (leftover items cause 409 on next checkout)
curl -s -X DELETE https://${FRONTEND_URL}/cart/demo-user

# Trigger Scenario B/C — VM failure
sudo systemctl stop web          # on EC2

# Trigger Scenario C — OpenShift failure
kubectl scale deployment frontend --replicas=0 -n tracing-demo

# Restore EC2 web
sudo systemctl start web         # on EC2

# Restore ocp-dc frontend
kubectl scale deployment frontend --replicas=1 -n tracing-demo
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

### vm-dc Troubleshooting

| Symptom | Check | Fix |
|---|---|---|
| `frontend` not in ImportedServices | `kubectl get exportedservices -n tracing-demo` | Re-apply `exported-services-ocp-dc-failover.yaml`; ensure `namespace: tracing-demo` per service |
| `frontend` imported but health API returns empty | `curl .../v1/health/service/frontend?peer=ocp-dc` | Create `tracing-demo` namespace on vm-dc: `curl -X PUT .../v1/namespace -d '{"Name":"tracing-demo"}'` |
| Traffic blanks but doesn't flip to frontend | Check `ejections_overflow` stat on client sidecar | Ensure `service-defaults-client.hcl` has `MaxEjectionPercent=100` |
| `403 RBAC: access denied` on upstream | Check intentions on ocp-dc | Patch `allow-frontend-failover` to include `web` and `client` from vm-dc peer |
| vm-web returns 502 on /checkout | `journalctl -u web` on EC2 | Verify port 9093 upstream is healthy: `curl -v http://localhost:9093/health` |
| `failed_eds_health` on failover cluster | `curl localhost:<admin>/clusters \| grep failover` | Verify `service-resolver-web-failover.hcl` is applied |
| Client sidecar fails to start | `cat /tmp/envoy-client.log` | Kill stale envoy processes; use `-grpc-addr 127.0.0.1:8502` |
| OTel spans not reaching Tempo from EC2 | `journalctl -u otelcol -n 30` | Confirm OTLP HTTP export to OpenShift Route is reachable (NodePort 30317 is blocked by ROSA SG) |
| `consul reload` returns 403 | Token lacks `agent:write` | Use bootstrap token: `c0f2d09d-a9a7-653b-8ddd-88ca6fa101d8` |

---

## vm-dc File Reference

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
| `deploy/consul/peering-acceptor.yaml` | `PeeringAcceptor` CRD — generates the peering token as a K8s secret with mesh-gateway address | ocp-dc: `oc apply -f` |
| `deploy/consul/mesh-config.yaml` | Mesh config enabling `peerThroughMeshGateways: true` | ocp-dc: `oc apply -f` |
| `deploy/consul/peering-setup.md` | Full peering setup sequence (rebuild from scratch) | Reference |

---

## Demo Tips

1. **Pre-run the burst traffic** (Flow 4) 2–3 minutes before the demo to populate the service graph
2. **Open Grafana in a separate browser window** — switching tabs is faster than navigating during a live demo
3. **Use browser zoom (150%)** to make spans readable from the back of the room
4. **Have TraceQL queries pre-typed** in a scratch file — copy-paste is faster than typing live
5. **Keep both a "clean" and "error" trace open in separate tabs** for Flow 3's before/after contrast
6. **Demo Flows 1–4 are fully self-contained on ocp-dc** — no EC2 dependency for those flows
7. **For Flow 5 Scenario A**, the simplest path is to pre-stop `web` before the demo and just drive GET traffic from `vm-client` — no failover drama needed to show the cross-DC span

---

## Additional Resources

- [README.md](README.md) — Full deployment and configuration guide
- [docs/observability/04-distributed-tracing.md](docs/observability/04-distributed-tracing.md) — Architecture and troubleshooting
- [deploy/observability/README.md](deploy/observability/README.md) — Full observability stack setup
- [deploy/consul/peering-setup.md](deploy/consul/peering-setup.md) — Consul cluster peering setup sequence
- [Grafana Tempo documentation](https://grafana.com/docs/tempo/latest/)
- [TraceQL reference](https://grafana.com/docs/tempo/latest/traceql/)
- [OpenTelemetry Go SDK](https://opentelemetry.io/docs/instrumentation/go/)
