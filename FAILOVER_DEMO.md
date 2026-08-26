# Consul Peering Failover Demo

Cross-datacenter service failover between **ocp-dc** (OpenShift/ROSA) and
**vm-dc** (EC2) using Consul Cluster Peering, Mesh Gateways, and
ServiceResolvers — with full distributed trace continuity in Grafana Tempo.

---

## Architecture

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

### What the demo proves

1. **True cross-DC trace waterfall** — A single trace ID spans both datacenters.
   The Tempo waterfall shows: `client → web → api → frontend → checkout → cart → inventory → payment`.
   Every span is tagged with its originating datacenter.
2. **Automatic failover with zero app changes** — When `web` on EC2 fails its health
   check, Consul transparently reroutes traffic to `frontend` in ocp-dc. No DNS changes,
   no app restarts, no redeployment.
3. **Unbroken trace continuity through failover** — The W3C `traceparent` header
   travels through the mesh gateway. During failover, Tempo shows:
   `client → frontend → checkout → cart → inventory → payment` — still one trace ID.

### Normal operation flow (Scenario A baseline)

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

### Failover flow (Scenario A: web down)

```
vm-client
  └─ POST /checkout ──► frontend (Envoy port 9095, failover target)
       ├─ POST /cart/{user}/items ──► cart
       └─ POST /checkout ──► checkout
            ├─ GET /cart/{user} ──► cart
            ├─ POST /reserve ──► inventory
            └─ POST /authorize ──► payment
```

All spans still land in Tempo under the same trace root from `client`.

---

## Pre-Flight Checklist

```bash
# ocp-dc — all pods Running
kubectl get pods -n tracing-demo

# ocp-dc — Tempo healthy
kubectl get pods -n tempo

# vm-dc — web and api running (on EC2)
sudo systemctl status web api --no-pager | grep -E "Active:"

# vm-dc — peering ACTIVE and frontend imported
export CONSUL_HTTP_TOKEN=REDACTED_BOOTSTRAP_TOKEN
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

---

## Setup: Apply All Config (First Time or After Reset)

If rebuilding from scratch, see [`deploy/consul/peering-setup.md`](deploy/consul/peering-setup.md)
for the full peering establishment sequence. For an existing environment, run:

### vm-dc (on EC2)

```bash
export CONSUL_HTTP_TOKEN=REDACTED_BOOTSTRAP_TOKEN
export CONSUL_HTTP_ADDR=http://127.0.0.1:8500
cd /path/to/repo

bash deploy/ec2/setup.sh
```

Applies in order: namespace creation, service definitions, exported services,
intentions, service defaults, and the service resolver.

### ocp-dc (on Mac)

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

---

## Starting the Traffic Loop (EC2)

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

## Scenario A: VM Fails → Failover to OpenShift

**Story:** The EC2 VM web service becomes unhealthy. Consul detects the failure
and routes `client` traffic to `frontend` in ocp-dc — automatically, with no app
changes, no DNS changes, no redeployment.

### Step 1 — Start traffic loop and show baseline in Grafana

With the loop running (see above), open Grafana → Explore → Tempo and run:

```
{ resource.service.name = "client" }
```

Open a trace — shows the full waterfall:
`client → web → api → frontend → checkout → cart → inventory → payment`

Spans tagged `dc=vm-dc`: `client`, `web`, `api`
Spans from ocp-dc: `frontend`, `checkout`, `cart`, `inventory`, `payment`

**Demo script:**
> *"This is a single transaction. The client is on an EC2 VM. It calls the
> local web service, which looks up the product price from api — both on the VM,
> both tagged dc=vm-dc. Then web crosses the Consul mesh gateway peering tunnel
> to frontend in OpenShift, which runs the full checkout pipeline. One trace ID,
> two datacenters, seven services."*

### Step 2 — Trigger the failure (EC2 Terminal 2)

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

> `dc=unknown` appears because `client` is now hitting `frontend` directly —
> frontend returns its own response shape, not vm-web's. Correct behaviour.

Consul UI (vm-dc) → Services → `web`: health check turns red.

**Demo script:**
> *"Web on the EC2 VM just stopped. Consul's ServiceResolver saw zero healthy
> local instances and began routing the client's upstream through the mesh
> gateway to frontend in OpenShift. The traffic loop never stopped.
> No app changes. No DNS changes. Under 10 seconds."*

### Step 3 — Show the failover trace in Tempo

```
{ resource.service.name = "client" }
```

Sort by most recent. Click a trace from after the failover — the waterfall now shows:
`client → frontend → checkout → cart → inventory → payment`

The `client` span is the same root as before failover. The `web` and `api` spans
are gone (web is down). `frontend` is the first child, inheriting the trace context
from `client` through the mesh gateway.

**Demo script:**
> *"The W3C traceparent header crossed the mesh gateway. The request that started
> on the EC2 VM was served by frontend in Kubernetes — and every span is in Tempo
> under the same trace ID. The trace itself documents the datacenter handoff."*

### Step 4 — Restore and show recovery (EC2 Terminal 2)

```bash
sudo systemctl start web
```

Within 10 seconds the health check passes, Consul stops using the failover target,
and the traffic log returns to `dc=vm-dc` with the full cross-DC waterfall.

---

## Scenario B: OpenShift Fails → Failover to VM

**Story:** The OpenShift frontend deployment is scaled to zero. Consul routes
incoming ocp-dc requests to `web` on the EC2 VM.

### Step 1 — Start a traffic loop against ocp-dc (Mac terminal)

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

### Step 2 — Trigger the failure

```bash
kubectl scale deployment frontend --replicas=0 -n tracing-demo
```

Consul health check for `frontend` fails within 10 seconds. The ServiceResolver
kicks in and routes requests to `web` on EC2 via the mesh gateway.

**Demo script:**
> *"We just scaled frontend to zero in OpenShift. Consul's health check saw
> no healthy instances and, using the ServiceResolver failover config, began
> routing those HTTPS requests through the mesh gateway to the EC2 VM.
> No Ingress change. No DNS TTL. No load balancer update. Under 10 seconds."*

### Step 3 — Show cross-DC trace in Tempo

```
{ resource.dc = "vm-dc" }
```

Open the most recent trace — shows `web → api` spans tagged `dc=vm-dc`, produced
by requests that entered through the ocp-dc Ingress but were served by the EC2 VM.

### Step 4 — Restore OpenShift

```bash
kubectl scale deployment frontend --replicas=1 -n tracing-demo
```

---

## Scenario C: Side-by-Side — Both DCs Healthy

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
> *"Two entry points, one observability plane. Tab 1 shows checkout requests
> that originated on the EC2 VM and crossed the mesh gateway to OpenShift.
> Tab 2 shows requests that hit the ocp-dc Ingress directly. Same Tempo,
> same trace format, regardless of where the request started."*

---

## Quick Reference

### Demo controls

```bash
# Trigger Scenario A (VM failure)
sudo systemctl stop web          # on EC2

# Trigger Scenario B (OpenShift failure)
kubectl scale deployment frontend --replicas=0 -n tracing-demo

# Restore EC2 web
sudo systemctl start web         # on EC2

# Restore ocp-dc frontend
kubectl scale deployment frontend --replicas=1 -n tracing-demo

# Inject payment errors
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

### Key TraceQL queries

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

### Consul tokens (vm-dc)

| Role | Token |
|---|---|
| Bootstrap / mgmt | `REDACTED_BOOTSTRAP_TOKEN` |
| web service | `REDACTED_WEB_TOKEN` |
| api service | `REDACTED_API_TOKEN` |
| mesh-gateway | `REDACTED_MGW_TOKEN` |

---

## Troubleshooting

| Symptom | Check | Fix |
|---|---|---|
| `frontend` not in ImportedServices | `kubectl get exportedservices -n tracing-demo` | Re-apply `exported-services-ocp-dc-failover.yaml`; ensure `namespace: tracing-demo` per service |
| `frontend` imported but health API empty | `curl .../v1/health/service/frontend?peer=ocp-dc` | Create `tracing-demo` namespace: `curl -X PUT .../v1/namespace -d '{"Name":"tracing-demo"}'` |
| Traffic blanks but doesn't flip to frontend | Check `ejections_overflow` stat | Ensure `service-defaults-client.hcl` has `MaxEjectionPercent=100` |
| `403 RBAC: access denied` on upstream | Check intentions on ocp-dc | Patch `allow-frontend-failover` to include `web` and `client` from vm-dc peer |
| vm-web returns 502 on /checkout | `journalctl -u web` on EC2 | Verify port 9093 upstream is healthy: `curl -v http://localhost:9093/health` |
| `failed_eds_health` on failover cluster | `curl localhost:<admin>/clusters \| grep failover` | Verify service-resolver-web-failover.hcl is applied |
| Client sidecar fails to start | `cat /tmp/envoy-client.log` | Kill stale envoy processes; use `-grpc-addr 127.0.0.1:8502` |
| OTel spans not reaching Tempo | `journalctl -u otelcol -n 30` | Confirm `localhost:9317` upstream is healthy |
| `consul reload` returns 403 | Token lacks `agent:write` | Use bootstrap token `REDACTED_BOOTSTRAP_TOKEN` |

---

## File Reference

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
