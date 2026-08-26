# Consul Peering Failover Demo

Cross-datacenter service failover between **ocp-dc** (OpenShift/ROSA) and
**vm-dc** (EC2) using Consul Cluster Peering, Mesh Gateways, and
ServiceResolvers — with full distributed trace continuity in Grafana Tempo.

---

## Architecture

```
┌─────────────────────────────────────────────────────┐
│  ocp-dc  (OpenShift / ROSA)                         │
│                                                     │
│  frontend ──► checkout ──► cart                     │
│                        ──► inventory                │
│                        ──► payment                  │
│                                                     │
│  otel-collector ──► Tempo ──► Grafana               │
└───────────────────────┬─────────────────────────────┘
                        │  WAN Peering (mTLS)
                        │  Mesh Gateway ↔ Mesh Gateway
┌───────────────────────┴─────────────────────────────┐
│  vm-dc  (EC2 / aws-vm-node-1)                       │
│                                                     │
│  client ──► web ──► api                             │
│                                                     │
│  otelcol-agent ──► ocp-dc otel-collector            │
└─────────────────────────────────────────────────────┘
```

### What the demo proves

1. **Unified traces across VM + Kubernetes** — A single trace waterfall shows
   spans from EC2-hosted `web`/`api` and K8s-hosted services under one trace ID.
2. **Automatic failover with zero app changes** — When `web` on EC2 fails its
   health check, Consul transparently routes traffic to `frontend` in ocp-dc
   through the mesh gateway. No DNS changes, no app restarts, no redeployment.
3. **Trace continuity through failover** — The W3C `traceparent` header travels
   through the mesh gateway so the Tempo waterfall shows which datacenter served
   each span.

### Failover directions

| Scenario | Trigger | Consul routes to |
|---|---|---|
| **A — VM → OpenShift** | `web` health check critical in vm-dc | `frontend` in ocp-dc |
| **B — OpenShift → VM** | `frontend` health check critical in ocp-dc | `web` in vm-dc |

---

## Pre-Flight Checklist

Before running the demo, confirm the following are healthy:

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

This applies in order: namespace creation, service definitions, exported services,
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

## Scenario A: VM Fails → Failover to OpenShift

**Story:** The EC2 VM web service becomes unhealthy. Consul detects the failure
and routes traffic to `frontend` in ocp-dc — automatically, with no app changes,
no DNS changes, no redeployment.

### Step 1 — Start vm-client and the traffic loop (EC2 Terminal 1)

```bash
# Start vm-client if not already running
sudo systemctl start web
sudo pkill -f vm-client 2>/dev/null

SERVICE_NAME=client PORT=9080 UPSTREAM_URI=http://localhost:9095 \
  /usr/local/bin/vm-client > /tmp/vm-client.log 2>&1 &

sleep 2

# Continuous traffic loop — keep this running
while true; do
  response=$(curl -s --max-time 3 http://localhost:9080/)
  name=$(echo "$response" | jq -r '.upstream_calls | to_entries[0].value.name // empty' 2>/dev/null)
  code=$(echo "$response" | jq -r '.upstream_calls | to_entries[0].value.code // empty' 2>/dev/null)
  echo "$(date +%H:%M:%S) served_by=${name:-unknown} http=$code"
  sleep 1
done
```

You'll see `served_by=web http=200` on every line while EC2 is healthy.

### Step 2 — Show baseline in Grafana

Open Grafana → Explore → Tempo and run:

```
{ resource.dc = "vm-dc" }
```

Open a trace — shows `web → api` waterfall, both spans tagged `dc=vm-dc`.

**Demo script:**
> *"The EC2 VM is healthy. Every request from the client service routes through
> the local web service, which calls api. Both services are instrumented with
> OpenTelemetry and every span lands in Tempo. This is our baseline."*

### Step 3 — Trigger the failure (EC2 Terminal 2)

```bash
sudo systemctl stop web
```

Watch Terminal 1 — within ~10 seconds:

```
20:40:31 served_by=web    http=200
20:40:32 served_by=unknown http=503   ← single in-flight during health check window
20:40:33 served_by=unknown http=404   ← Consul failover active, traffic on ocp-dc frontend
20:40:34 served_by=unknown http=404
```

> The `http=404` response is expected — `frontend` on ocp-dc doesn't expose a
> root `/` route. The routing itself is working; requests are reaching ocp-dc.

Consul UI (vm-dc) → Services → `web`: health check turns red.

**Demo script:**
> *"The web service on the EC2 VM just stopped. The Consul health check failed.
> The ServiceResolver saw zero healthy local instances and began routing through
> the mesh gateway peering tunnel to frontend in our OpenShift cluster.
> The traffic loop never stopped — it just switched datacenters.
> No app changes. No DNS changes. No deployment. Under 10 seconds."*

### Step 4 — Show the failover trace in Tempo

In Grafana → Explore → Tempo:

```
{ .service.name = "frontend" }
```

Sort by most recent. Requests that were in-flight during the failover window
will show the path crossing from vm-dc into ocp-dc.

**Demo script:**
> *"The W3C traceparent header crossed the mesh gateway into OpenShift. The
> request that started on the VM was served by frontend in Kubernetes — and
> every span is in Tempo under the same trace ID."*

### Step 5 — Restore and show recovery (EC2 Terminal 2)

```bash
sudo systemctl start web
```

Within 10 seconds the health check passes, Consul stops using the failover
target, and Terminal 1 returns to `served_by=web http=200`.

---

## Scenario B: OpenShift Fails → Failover to VM

**Story:** The OpenShift frontend deployment is scaled to zero. Consul detects
the failure and routes incoming requests to `web` on the EC2 VM.

### Step 1 — Start a traffic loop against ocp-dc (Mac terminal)

```bash
FRONTEND=frontend-tracing-demo.apps.rosa.cluster2.6cxo.p3.openshiftapps.com

while true; do
  code=$(curl -sk -o /dev/null -w "%{http_code}" \
    "https://${FRONTEND}/health")
  echo "$(date +%H:%M:%S) ocp-dc frontend http=$code"
  sleep 1
done
```

You'll see `http=200` while ocp-dc is healthy.

### Step 2 — Trigger the failure

```bash
kubectl scale deployment frontend --replicas=0 -n tracing-demo
```

Consul health check for `frontend` fails within 10 seconds. The ServiceResolver
kicks in and routes incoming requests to `web` on EC2 via the mesh gateway.

**Demo script:**
> *"We just scaled frontend to zero in OpenShift. The Consul health check saw
> no healthy instances and, using the ServiceResolver failover config, began
> routing those HTTPS requests through the mesh gateway to the EC2 VM.
> No Ingress change. No DNS TTL. No load balancer update. Under 10 seconds."*

### Step 3 — Show cross-DC trace in Tempo

In Grafana → Explore → Tempo:

```
{ resource.dc = "vm-dc" }
```

Open the most recent trace. Requests that entered through the ocp-dc ingress
but were served by vm-dc will show `web → api` spans tagged `dc=vm-dc`.

**Demo script:**
> *"The trace shows the request entered through the OpenShift ingress, crossed
> the mesh gateway peering tunnel to the EC2 VM, and was handled by web and api
> there. One trace ID, two datacenters, full observability."*

### Step 4 — Restore OpenShift

```bash
kubectl scale deployment frontend --replicas=1 -n tracing-demo
```

Traffic returns to ocp-dc frontend automatically once the health check passes.

---

## Scenario C: Both Healthy — Side-by-Side

Show traces from both datacenters in Grafana simultaneously.

Open two Explore tabs:

**Tab 1 — ocp-dc traces:**
```
{ .service.name = "frontend" && duration > 50ms }
```

**Tab 2 — vm-dc traces:**
```
{ resource.dc = "vm-dc" }
```

Generate traffic to both simultaneously:

```bash
# Terminal 1 (Mac) — ocp-dc
for i in $(seq 1 10); do
  curl -sk "https://frontend-tracing-demo.apps.rosa.cluster2.6cxo.p3.openshiftapps.com/checkout" \
    -H "Content-Type: application/json" -d '{"user_id":"ocp-user"}' > /dev/null
  sleep 0.5
done

# Terminal 2 (EC2) — vm-dc
for i in $(seq 1 10); do
  curl -s http://localhost:9080/ > /dev/null
  sleep 0.5
done
```

**Demo script:**
> *"Both datacenters are generating traces simultaneously. One observability
> plane for your entire estate, regardless of where services run."*

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

# Inject payment errors (existing demo feature)
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
# All vm-dc traces
{ resource.dc = "vm-dc" }

# All ocp-dc frontend traces
{ .service.name = "frontend" }

# Slow traces (latency injection active)
{ .service.name = "frontend" && duration > 500ms }

# Error traces
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
| `frontend` not in ImportedServices | `kubectl get exportedservices -n tracing-demo` | Re-apply `exported-services-ocp-dc-failover.yaml`; ensure `namespace: tracing-demo` is set per service |
| `frontend` imported but health API returns empty | `curl .../v1/health/service/frontend?peer=ocp-dc` | Create `tracing-demo` namespace on vm-dc: `curl -X PUT .../v1/namespace -d '{"Name":"tracing-demo"}'` |
| Traffic blanks but doesn't flip to `frontend` | Check `ejections_overflow` stat on client sidecar | Ensure `service-defaults-client.hcl` is applied with `MaxEjectionPercent=100` |
| `403 RBAC: access denied` on upstream | Check intentions on ocp-dc | Patch `allow-frontend-failover` to add `client` peer source from `vm-dc` |
| `failed_eds_health` on failover cluster | `curl localhost:<admin-port>/clusters \| grep failover` | Verify SamenessGroup is deleted; service-resolver-web-failover.hcl is applied |
| Client sidecar fails to start | `cat /tmp/envoy-client.log` | Kill stale envoy processes; use `-grpc-addr 127.0.0.1:8502` |
| OTel spans not reaching Tempo | `journalctl -u otelcol -n 30` | Confirm `localhost:9317` upstream is healthy in web sidecar |
| `consul reload` returns 403 | Token lacks `agent:write` | Use bootstrap token `REDACTED_BOOTSTRAP_TOKEN` |

---

## File Reference

| File | Purpose | Apply where |
|---|---|---|
| `deploy/ec2/setup.sh` | Applies all vm-dc config entries in order | EC2: `bash deploy/ec2/setup.sh` |
| `deploy/ec2/web.hcl` | Consul service def for web (otel-collector upstream) | EC2: `/etc/consul.d/web.hcl` |
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
| `deploy/consul/peering-setup.md` | Full peering setup sequence (rebuild from scratch) | Reference |
