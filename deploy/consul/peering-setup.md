# Consul Cluster Peering Setup Guide

Full sequence for establishing Consul cluster peering between **ocp-dc** (ROSA /
OpenShift, Consul Enterprise 2.0.1+ent on Kubernetes) and **vm-dc** (EC2,
Consul Enterprise 2.0.1+ent on a VM).

This guide covers everything needed to rebuild the environment from scratch.

---

## Architecture

```
ocp-dc (ROSA)                          vm-dc (EC2 aws-vm-node-1)
─────────────────────────────────      ──────────────────────────────────
frontend  (tracing-demo ns)            web      → api
checkout                               client   → web (9095)
payment                                otelcol-agent
inventory
cart
catalog
otel-collector  (tracing-demo ns)
mesh-gateway    (consul ns)            mesh-gateway (port 21000)
consul-server-0 (consul ns)            consul agent (port 8500/8502)
```

Traffic flow for failover demo:
```
vm-dc client → Envoy:9095 → [web healthy] → web → api
                           → [web critical, SamenessGroup] → mesh-gateway:21000
                                                           → ocp-dc mesh-gateway
                                                           → frontend
```

---

## Prerequisites

- Consul Enterprise license installed on both sides
- Both Consul agents/servers running and healthy
- Mesh gateway deployed on both sides
- `kubectl` pointed at ocp-dc cluster
- SSH access to EC2 instance

### Key tokens (vm-dc)

| Role              | Token                                  |
|-------------------|----------------------------------------|
| Bootstrap/mgmt    | `REDACTED_BOOTSTRAP_TOKEN` |
| web service       | `REDACTED_WEB_TOKEN` |
| api service       | `REDACTED_API_TOKEN` |
| mesh-gateway      | `REDACTED_MGW_TOKEN` |

---

## Step 1 — Establish Cluster Peering

Peering is initiated from ocp-dc (acceptor) → vm-dc (dialer).

### 1a — Generate peering token on ocp-dc

```bash
# On your Mac (kubectl pointed at ocp-dc)
kubectl exec -n consul consul-server-0 -c consul -- \
  consul peering generate-token -name vm-dc \
  -http-addr https://localhost:8501 \
  -ca-file /consul/tls/ca/tls.crt \
  -token <bootstrap-token> \
  > /tmp/peering-token-vm-dc.txt

cat /tmp/peering-token-vm-dc.txt
```

### 1b — Establish peering from vm-dc using the token

```bash
# On EC2
export CONSUL_HTTP_TOKEN=REDACTED_BOOTSTRAP_TOKEN
export CONSUL_HTTP_ADDR=http://127.0.0.1:8500

consul peering establish -name ocp-dc -peering-token <paste-token-here>
```

### 1c — Verify peering is ACTIVE on both sides

```bash
# On EC2
curl -s -H "X-Consul-Token: $CONSUL_HTTP_TOKEN" \
  http://127.0.0.1:8500/v1/peering/ocp-dc | jq '.State'
# Expected: "ACTIVE"

# On Mac
kubectl exec -n consul consul-server-0 -c consul -- \
  curl -sk https://localhost:8501/v1/peering/vm-dc \
  -H "X-Consul-Token: <bootstrap-token>" | jq '.State'
# Expected: "ACTIVE"
```

---

## Step 2 — Create Namespaces on vm-dc

vm-dc is Consul Enterprise but has no mirrored namespaces. Create the
`tracing-demo` namespace so imported peer services from ocp-dc
(which live in `tracing-demo` on ocp-dc) have a local namespace to land in.

```bash
# On EC2
export CONSUL_HTTP_TOKEN=REDACTED_BOOTSTRAP_TOKEN

curl -s -X PUT -H "X-Consul-Token: $CONSUL_HTTP_TOKEN" \
  http://127.0.0.1:8500/v1/namespace \
  -d '{"Name":"tracing-demo"}' | jq '{Name:.Name}'
```

---

## Step 3 — Apply Service Definitions on EC2

```bash
# On EC2 — copy all service definitions
sudo cp /path/to/repo/deploy/ec2/web.hcl    /etc/consul.d/web.hcl
sudo cp /path/to/repo/deploy/ec2/api.hcl    /etc/consul.d/api.hcl
sudo cp /path/to/repo/deploy/ec2/client.hcl /etc/consul.d/client.hcl

consul reload -token REDACTED_BOOTSTRAP_TOKEN
```

---

## Step 4 — Apply All Consul Config Entries

Use the setup script which applies everything in the correct order:

```bash
# On EC2
cd /path/to/repo
bash deploy/ec2/setup.sh
```

Or apply individually — see `deploy/ec2/setup.sh` for the full sequence.

---

## Step 5 — Apply ocp-dc Kubernetes Resources

Apply in this order:

```bash
# On Mac (kubectl → ocp-dc)

# 5a — Export otel-collector and frontend to vm-dc peer
kubectl apply -f deploy/consul/exported-services-ocp-dc-failover.yaml -n tracing-demo

# 5b — Allow vm-dc services to reach otel-collector and frontend
kubectl apply -f deploy/consul/service-intentions-otel-collector.yaml -n tracing-demo

# 5c — SamenessGroup in default namespace
#      Declares frontend in ocp-dc and web in vm-dc as the same logical service
kubectl apply -f deploy/consul/sameness-group-frontend.yaml -n default
```

### Verify ocp-dc resources are synced

```bash
kubectl get exportedservices -n tracing-demo
kubectl get samenessgroups -n default
kubectl get serviceintentions -n tracing-demo
# All should show SYNCED: True
```

---

## Step 6 — Verify End-to-End

### 6a — Confirm frontend is imported into vm-dc catalog

```bash
# On EC2
export CONSUL_HTTP_TOKEN=REDACTED_BOOTSTRAP_TOKEN

# Should return "ACTIVE" and show frontend in ImportedServices
curl -s -H "X-Consul-Token: $CONSUL_HTTP_TOKEN" \
  http://127.0.0.1:8500/v1/peering/ocp-dc \
  | jq '{State:.State, Imported:.StreamStatus.ImportedServices}'

# Should return a service entry with Status: passing
curl -s -H "X-Consul-Token: $CONSUL_HTTP_TOKEN" \
  "http://127.0.0.1:8500/v1/health/service/frontend?peer=ocp-dc&ns=tracing-demo" \
  | jq '.[0] | {ID:.Service.ID, Status:.Checks[].Status}'
```

### 6b — Confirm client sidecar has failover cluster

```bash
# Find the client sidecar admin port
ss -tlnp | grep -E "190[0-9][0-9]"

# Check clusters — should show web (local) + frontend.ocp-dc (peer failover)
curl -s http://localhost:<admin-port>/clusters \
  | grep "observability_name" | sed 's/::observability_name::.*//'
```

---

## Step 7 — Run the Failover Demo

### 7a — Start services on EC2

```bash
sudo systemctl start web
sudo pkill -f vm-client 2>/dev/null

SERVICE_NAME=client PORT=9080 UPSTREAM_URI=http://localhost:9095 \
  /usr/local/bin/vm-client > /tmp/vm-client.log 2>&1 &

curl -s http://localhost:9080/health
```

### 7b — Terminal 1: traffic loop

```bash
while true; do
  result=$(curl -s --max-time 3 http://localhost:9080/ \
    | jq -r '.upstream_calls | to_entries[0].value.name // "error"')
  echo "$(date +%H:%M:%S) served_by=$result"
  sleep 1
done
```

### 7c — Terminal 2: trigger failover

```bash
# Stop web — Consul health check fails within 10s, SamenessGroup routes to ocp-dc
sudo systemctl stop web

# Restore
sudo systemctl start web
```

Expected output in Terminal 1:
```
19:25:37 served_by=web
19:25:38 served_by=web
19:25:43 served_by=          ← health check failing (10s window)
19:25:53 served_by=frontend  ← SamenessGroup failover active
19:25:54 served_by=frontend
```

---

## Troubleshooting

| Symptom | Check | Fix |
|---|---|---|
| `frontend` not in ImportedServices | `kubectl get exportedservices -n tracing-demo` | Re-apply `exported-services-ocp-dc-failover.yaml` with `namespace: tracing-demo` on each service |
| `frontend` imported but health check empty | `curl .../v1/health/service/frontend?peer=ocp-dc` returns `[]` | Create `tracing-demo` namespace on vm-dc (Step 2) |
| SamenessGroup `members must be unique` | Remove `partition: default` from Members — `IncludeLocal: true` already covers it | Use `includeLocal: true` with only `peer:` entries in members |
| Failover cluster shows `failed_eds_health` | `curl localhost:<port>/clusters \| grep frontend` | Verify SamenessGroup synced on both sides; bounce client sidecar |
| Client sidecar fails to start (port conflict) | `ss -tlnp \| grep envoy` | Kill stale envoy processes; use `-admin-bind 127.0.0.1:190XX` with a free port |
| `consul reload` returns 403 | Token lacks `agent:write` | Use bootstrap token `REDACTED_BOOTSTRAP_TOKEN` |
| Envoy sidecar exits with TLS error | `cat /tmp/envoy-client.log` | Add `-grpc-addr 127.0.0.1:8502` to `consul connect envoy` command |
| OTel spans not reaching Tempo | `journalctl -u otelcol -n 30` | Confirm `localhost:9317` upstream is healthy in web sidecar |

---

## File Reference

| File | Purpose | Apply where |
|---|---|---|
| `deploy/ec2/setup.sh` | Applies all vm-dc config entries in order | EC2: `bash deploy/ec2/setup.sh` |
| `deploy/ec2/web.hcl` | Consul service def for web (with otel-collector upstream) | EC2: `/etc/consul.d/web.hcl` |
| `deploy/ec2/api.hcl` | Consul service def for api | EC2: `/etc/consul.d/api.hcl` |
| `deploy/ec2/client.hcl` | Consul service def for client (upstream: web:9095) | EC2: `/etc/consul.d/client.hcl` |
| `deploy/ec2/sameness-group-web.hcl` | SamenessGroup: web↔frontend across peers | EC2: `consul config write` |
| `deploy/ec2/exported-services-vm-dc.hcl` | Export web+api to ocp-dc peer | EC2: `consul config write` |
| `deploy/ec2/service-intentions-vm-dc.hcl` | Allow web+api to reach otel-collector | EC2: `consul config write` |
| `deploy/ec2/service-intentions-vm-dc-failover.hcl` | Allow ocp-dc frontend to reach vm-dc web | EC2: `consul config write` |
| `deploy/consul/exported-services-ocp-dc-failover.yaml` | Export otel-collector+frontend to vm-dc | ocp-dc: `kubectl apply -n tracing-demo` |
| `deploy/consul/service-intentions-otel-collector.yaml` | Allow vm-dc peers to reach otel-collector | ocp-dc: `kubectl apply -n tracing-demo` |
| `deploy/consul/sameness-group-frontend.yaml` | SamenessGroup: frontend↔web across peers | ocp-dc: `kubectl apply -n default` |
